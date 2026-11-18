// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/deployer"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

func reconcileExample(t *testing.T, mutate func(*v1alpha1.SystemDescription)) (*v1alpha1.SystemDescription, *deployer.DryRunApplier) {
	return reconcileWithSets(t, nil, mutate)
}

func reconcileWithSets(t *testing.T, sets *stack.Sets, mutate func(*v1alpha1.SystemDescription)) (*v1alpha1.SystemDescription, *deployer.DryRunApplier) {
	t.Helper()
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sd.Namespace = "kamerton"
	sd.Generation = 1
	if mutate != nil {
		mutate(sd)
	}
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sd).WithStatusSubresource(sd).Build()
	applier := &deployer.DryRunApplier{}
	r := &SystemDescriptionReconciler{Client: c, Applier: applier, Sets: sets}
	key := types.NamespacedName{Namespace: "kamerton", Name: sd.Name}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.SystemDescription
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	return &got, applier
}

func TestReconcileValid(t *testing.T) {
	got, applier := reconcileExample(t, nil)
	for _, c := range []string{ConditionValid, ConditionGenerated, ConditionApplied} {
		if !meta.IsStatusConditionTrue(got.Status.Conditions, c) {
			t.Errorf("condition %s not true: %+v", c, got.Status.Conditions)
		}
	}
	if got.Status.GeneratedRules == 0 || len(applier.Applied) == 0 {
		t.Errorf("nothing generated/applied: rules=%d objs=%d", got.Status.GeneratedRules, len(applier.Applied))
	}
}

func TestReconcileInvalid(t *testing.T) {
	got, applier := reconcileExample(t, func(sd *v1alpha1.SystemDescription) {
		sd.Spec.Services[0].DependsOn = []string{"does-not-exist"}
	})
	c := meta.FindStatusCondition(got.Status.Conditions, ConditionValid)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != "ValidationFailed" {
		t.Fatalf("expected Valid=False, got %+v", got.Status.Conditions)
	}
	if len(applier.Applied) != 0 {
		t.Error("invalid description must not be applied")
	}
}

// The labels of the system's set go on its PrometheusRule and
// AlertmanagerConfig; an unknown set falls back to the built-in one.
func TestReconcileSetLabels(t *testing.T) {
	sets := &stack.Sets{Store: &stack.MemoryStateStore{}}
	ctx := context.Background()
	name := "Команда А"
	if _, err := sets.Create(ctx, stack.Input{ID: "team-a", Name: &name, URLs: map[string]string{stack.KindPrometheus: "http://prometheus-operated.team-a:9090"},
		RuleLabels: &map[string]string{"team": "a"}, AlertmanagerConfigLabels: &map[string]string{"alertmanager": "team-a"}}); err != nil {
		t.Fatal(err)
	}
	labelsOf := func(applier *deployer.DryRunApplier, kind string) map[string]string {
		for _, o := range applier.Applied {
			if o.GetKind() == kind {
				return o.GetLabels()
			}
		}
		t.Fatalf("no %s applied", kind)
		return nil
	}
	got, applier := reconcileWithSets(t, sets, func(sd *v1alpha1.SystemDescription) { sd.Spec.Stack = "team-a" })
	if l := labelsOf(applier, "PrometheusRule"); l["team"] != "a" || l["alertmanager"] != "" {
		t.Errorf("PrometheusRule: %v", l)
	}
	if l := labelsOf(applier, "AlertmanagerConfig"); l["alertmanager"] != "team-a" || l["team"] != "" {
		t.Errorf("AlertmanagerConfig: %v", l)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, ConditionApplied) {
		t.Errorf("not applied: %+v", got.Status.Conditions)
	}

	got, applier = reconcileWithSets(t, sets, func(sd *v1alpha1.SystemDescription) { sd.Spec.Stack = "gone" })
	if l := labelsOf(applier, "PrometheusRule"); l["team"] != "" {
		t.Errorf("unknown set must not carry another set's labels: %v", l)
	}
	c := meta.FindStatusCondition(got.Status.Conditions, ConditionApplied)
	if c == nil || c.Status != metav1.ConditionTrue || !strings.Contains(c.Message, `"gone" not found`) {
		t.Errorf("unknown set must be applied with a note: %+v", c)
	}
}

func TestSetsConfigMapEnqueuesSystems(t *testing.T) {
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	a := &v1alpha1.SystemDescription{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "a"}}
	b := &v1alpha1.SystemDescription{ObjectMeta: metav1.ObjectMeta{Namespace: "billing", Name: "b"}}
	r := &SystemDescriptionReconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(a, b).Build(), SetsNamespace: "kamerton", SetsConfigMap: "kamerton-stacks"}
	cm := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "kamerton", Name: "kamerton-stacks"}}
	if !r.isSetsConfigMap(cm) {
		t.Error("the sets ConfigMap must be watched")
	}
	if r.isSetsConfigMap(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "kamerton", Name: "kamerton-email-state"}}) {
		t.Error("other ConfigMaps must not trigger reconciles")
	}
	reqs := r.allSystems(context.Background(), cm)
	if len(reqs) != 2 {
		t.Errorf("both systems must be re-reconciled: %v", reqs)
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	s := strings.Repeat("я", 10) // two bytes each
	for n := 4; n <= len(s); n++ {
		got := truncate(s, n)
		if len(got) > n || !utf8.ValidString(got) {
			t.Errorf("truncate(%d) = %q (%d bytes)", n, got, len(got))
		}
	}
	if truncate("short", 10) != "short" {
		t.Error("short strings must stay as they are")
	}
}

func TestReconcileLeavesOutChannelWithoutSecret(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sd.Namespace = "kamerton"
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	// The mail password exists, the bot token does not.
	smtp := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kamerton", Name: "kamerton-smtp"},
		Data:       map[string][]byte{"password": []byte("x")},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sd, smtp).WithStatusSubresource(sd).Build()
	applier := &deployer.DryRunApplier{}
	r := &SystemDescriptionReconciler{Client: c, Applier: applier, Secrets: c}
	key := types.NamespacedName{Namespace: "kamerton", Name: sd.Name}

	reconcile := func() (ctrl.Result, map[string]any) {
		t.Helper()
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range applier.Applied {
			if o.GetKind() == "AlertmanagerConfig" {
				return res, o.Object["spec"].(map[string]any)
			}
		}
		t.Fatal("no AlertmanagerConfig applied")
		return res, nil
	}
	channels := func(spec map[string]any) (telegram, email int) {
		for _, raw := range spec["receivers"].([]any) {
			recv := raw.(map[string]any)
			if _, ok := recv["telegramConfigs"]; ok {
				telegram++
			}
			if _, ok := recv["emailConfigs"]; ok {
				email++
			}
		}
		return
	}

	res, spec := reconcile()
	if tg, mail := channels(spec); tg != 0 || mail == 0 {
		t.Errorf("want Telegram left out and email kept, got telegram=%d email=%d", tg, mail)
	}
	if rules, _ := spec["inhibitRules"].([]any); len(rules) == 0 {
		t.Error("inhibit rules must survive a missing channel Secret")
	}
	if res.RequeueAfter != missingSecretRetry {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, missingSecretRetry)
	}
	var got v1alpha1.SystemDescription
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, ConditionChannels)
	if cond == nil || cond.Status != metav1.ConditionFalse || !strings.Contains(cond.Message, "kamerton-telegram") {
		t.Errorf("condition %s = %+v", ConditionChannels, cond)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, ConditionApplied) {
		t.Errorf("Applied must stay true: %+v", got.Status.Conditions)
	}

	token := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kamerton", Name: "kamerton-telegram"},
		Data:       map[string][]byte{"bot-token": []byte("x")},
	}
	if err := c.Create(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	res, spec = reconcile()
	if tg, _ := channels(spec); tg == 0 {
		t.Error("Telegram must come back once its Secret exists")
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want none", res.RequeueAfter)
	}
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, ConditionChannels) {
		t.Errorf("condition %s not true: %+v", ConditionChannels, got.Status.Conditions)
	}
}
