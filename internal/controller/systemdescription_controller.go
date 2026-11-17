// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package controller reconciles SystemDescription resources: validate,
// generate, apply, and report the outcome in status conditions.
package controller

import (
	"context"
	"fmt"
	"strings"
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

// Condition types reported in status.
const (
	ConditionValid     = "Valid"
	ConditionGenerated = "Generated"
	ConditionApplied   = "Applied"
)

// SystemDescriptionReconciler reconciles a SystemDescription object.
//
// TODO: watch owned PrometheusRule/AlertmanagerConfig/ConfigMaps so manual
// drift is corrected immediately, not only on the next change of the
// description; periodic resync until then.
type SystemDescriptionReconciler struct {
	client.Client
	Applier deployer.Applier
	Options generator.Options
	// Sets resolves the system's set of programs: its extra labels go on
	// the PrometheusRule and AlertmanagerConfig. Nil: no extra labels.
	Sets *stack.Sets
	// SetsNamespace and SetsConfigMap name the ConfigMap holding the sets;
	// a change of it re-reconciles every system. Empty: not watched.
	SetsNamespace string
	SetsConfigMap string
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
// Discovery of programs already running in the cluster (read-only, chart value stacks.discovery.enabled):
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheuses;alertmanagers,verbs=get;list
// Evidence for follow-up messages (read-only, chart value incidents.followUp.workloadRead):
// +kubebuilder:rbac:groups="",resources=pods;events,verbs=get;list
// +kubebuilder:rbac:groups=apps,resources=replicasets;statefulsets,verbs=get;list

// Reconcile implements reconcile.Reconciler.
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

	bundle, err := generator.Generate(&sd, r.Options)
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
	return finish(nil)
}

// SetupWithManager registers the reconciler with the manager. A change of
// the sets ConfigMap re-reconciles every system: the systems using a changed
// set get its new labels (unchanged systems re-apply the same objects).
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

// allSystems lists every SystemDescription for re-reconciliation.
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
