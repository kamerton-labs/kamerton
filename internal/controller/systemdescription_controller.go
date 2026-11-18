// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package controller reconciles SystemDescription resources into rules and routing.
package controller

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/deployer"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

const (
	ConditionValid     = "Valid"
	ConditionGenerated = "Generated"
	ConditionApplied   = "Applied"
	// ConditionChannels is False while a receiver's Secret is missing and its channel is off.
	ConditionChannels = "ChannelsConnected"
)

// missingSecretRetry is how soon a system with a missing channel Secret is looked at again:
// Kamerton may not watch Secrets, so it polls.
const missingSecretRetry = 30 * time.Second

// TODO: watch owned objects so manual drift is corrected before the next resync.
type SystemDescriptionReconciler struct {
	client.Client
	Applier deployer.Applier
	Options generator.Options
	// Sets supplies extra labels for the PrometheusRule and AlertmanagerConfig; nil means none.
	Sets *stack.Sets
	// SetsNamespace and SetsConfigMap name the ConfigMap holding the sets;
	// a change of it re-reconciles every system. Empty: not watched.
	SetsNamespace string
	SetsConfigMap string
	// Secrets reads Secrets without a cache (a cache would need list and watch on all of them).
	// Nil: references are not checked.
	Secrets client.Reader
}

// RBAC for the Helm chart is maintained by hand in
// deploy/helm/kamerton/templates/rbac.yaml; keep these markers in sync.
// +kubebuilder:rbac:groups=kamerton.dev,resources=systemdescriptions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kamerton.dev,resources=systemdescriptions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules;alertmanagerconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// Telegram and email setup and the follow-up webhook token (namespaced Role in the chart, Secret restricted by name):
// +kubebuilder:rbac:groups="",namespace=kamerton,resources=secrets,resourceNames=kamerton-telegram;kamerton-smtp;kamerton-webhook,verbs=get;update;patch;delete
// +kubebuilder:rbac:groups="",namespace=kamerton,resources=secrets,verbs=create
// Access token of the git repository with descriptions, entered in the UI:
// +kubebuilder:rbac:groups="",namespace=kamerton,resources=secrets,resourceNames=kamerton-git,verbs=get;update;patch;delete
// Discovery of programs already running in the cluster (read-only, chart value stacks.discovery.enabled):
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheuses;alertmanagers,verbs=get;list
// Evidence for follow-up messages (read-only, chart value incidents.followUp.workloadRead):
// +kubebuilder:rbac:groups="",resources=pods;events,verbs=get;list
// +kubebuilder:rbac:groups=apps,resources=replicasets;statefulsets,verbs=get;list

func (r *SystemDescriptionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var sd v1alpha1.SystemDescription
	if err := r.Get(ctx, req.NamespacedName, &sd); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	setCond := func(t string, ok bool, reason, msg string) {
		status := metav1.ConditionFalse
		if ok {
			status = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&sd.Status.Conditions, metav1.Condition{
			Type: t, Status: status, Reason: reason, Message: truncate(msg, 1024), ObservedGeneration: sd.Generation,
		})
	}
	finish := func(err error) (ctrl.Result, error) {
		sd.Status.ObservedGeneration = sd.Generation
		if uerr := r.Status().Update(ctx, &sd); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{}, err
	}

	var errs []string
	for _, is := range description.ValidateSpec(&sd.Spec) {
		if is.Severity == description.SeverityError {
			errs = append(errs, is.Path+": "+is.Message)
		}
	}
	if len(errs) > 0 {
		setCond(ConditionValid, false, "ValidationFailed", strings.Join(errs, "; "))
		meta.RemoveStatusCondition(&sd.Status.Conditions, ConditionGenerated)
		meta.RemoveStatusCondition(&sd.Status.Conditions, ConditionApplied)
		// Invalid input is not retried; a new generation triggers reconcile.
		return finish(nil)
	}
	setCond(ConditionValid, true, "Valid", "description is valid")

	opts := r.Options
	if r.Secrets != nil {
		opts.SecretMissing = func(ref v1alpha1.SecretKeyRef) bool {
			return SecretMissing(ctx, r.Secrets, sd.Namespace, ref)
		}
	}
	bundle, err := generator.Generate(&sd, opts)
	if err != nil {
		setCond(ConditionGenerated, false, "GenerationFailed", err.Error())
		return finish(err)
	}
	sd.Status.GeneratedRules = int32(bundle.RuleCount)
	setCond(ConditionGenerated, true, "Generated", fmt.Sprintf("%d alerting rules", bundle.RuleCount))

	var extra deployer.ExtraLabels
	setNote := ""
	if r.Sets != nil {
		set, unknown, err := r.Sets.Resolve(ctx, sd.Spec.Stack)
		if err != nil {
			// Applying without the set's labels could make Prometheus drop
			// the rules: retry instead.
			setCond(ConditionApplied, false, "StackUnavailable", err.Error())
			return finish(err)
		}
		extra.Rule, extra.AlertmanagerConfig = set.RuleLabels, set.AlertmanagerConfigLabels
		if unknown {
			setNote = fmt.Sprintf("; set of programs %q not found, using the built-in set", sd.Spec.Stack)
		}
	}
	objs, err := deployer.ObjectsWithLabels(bundle, sd.Namespace, extra)
	if err != nil {
		setCond(ConditionApplied, false, "BuildFailed", err.Error())
		return finish(err)
	}
	// Generated objects belong to the description: deleting a system from
	// the UI removes its rules and routing through garbage collection.
	if sd.UID != "" {
		isController := true
		for _, o := range objs {
			o.SetOwnerReferences([]metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(), Kind: "SystemDescription",
				Name: sd.Name, UID: sd.UID, Controller: &isController,
			}})
		}
	}
	if err := r.Applier.Apply(ctx, objs); err != nil {
		log.Error(err, "apply failed")
		setCond(ConditionApplied, false, "ApplyFailed", err.Error())
		return finish(err)
	}
	setCond(ConditionApplied, true, "Applied", fmt.Sprintf("%d objects applied%s", len(objs), setNote))
	if len(bundle.MissingSecrets) == 0 {
		setCond(ConditionChannels, true, "Connected", "every channel has its Secret")
		return finish(nil)
	}
	var off []string
	for _, m := range bundle.MissingSecrets {
		off = append(off, fmt.Sprintf("%s (%s): Secret %s, key %s not found", m.Receiver, m.Channel, m.Secret, m.Key))
	}
	msg := "channels left out until their Secrets exist: " + strings.Join(off, "; ")
	log.Info(msg)
	setCond(ConditionChannels, false, "SecretMissing", msg)
	res, err := finish(nil)
	if err == nil {
		res.RequeueAfter = missingSecretRetry
	}
	return res, err
}

// SecretMissing is true only when the Secret or its key is known to be absent: a Secret Kamerton
// may not read stays in the configuration.
func SecretMissing(ctx context.Context, r client.Reader, ns string, ref v1alpha1.SecretKeyRef) bool {
	var s corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s)
	if apierrors.IsNotFound(err) {
		return true
	}
	if err != nil {
		return false
	}
	_, ok := s.Data[ref.Key]
	return !ok
}

func (r *SystemDescriptionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SystemDescription{}).
		Named("systemdescription")
	if r.SetsConfigMap != "" {
		b = b.Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.allSystems),
			builder.WithPredicates(predicate.NewPredicateFuncs(r.isSetsConfigMap)))
	}
	return b.Complete(r)
}

func (r *SystemDescriptionReconciler) isSetsConfigMap(o client.Object) bool {
	return o.GetNamespace() == r.SetsNamespace && o.GetName() == r.SetsConfigMap
}

func (r *SystemDescriptionReconciler) allSystems(ctx context.Context, _ client.Object) []reconcile.Request {
	var list v1alpha1.SystemDescriptionList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "list systems after a change of the sets of programs")
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for _, sd := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: sd.Namespace, Name: sd.Name}})
	}
	return out
}

// truncate shortens s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - 3
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
