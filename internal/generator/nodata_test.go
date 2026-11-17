// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

func mustDescription(t *testing.T, doc string) *v1alpha1.SystemDescription {
	t.Helper()
	res := description.Validate([]byte(doc))
	if !res.Valid {
		t.Fatalf("invalid: %+v", res.Errors)
	}
	return res.Document
}

func byService(b *Bundle, alert string) map[string]rulefmt.Rule {
	out := map[string]rulefmt.Rule{}
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			if r.Alert == alert {
				out[r.Labels[rules.LabelService]] = r
			}
		}
	}
	return out
}

func TestSystemNoDataRuleOfTheDemo(t *testing.T) {
	b := loadExample(t)
	if g := b.RuleGroups.Groups[0]; g.Name != "kamerton/otel-demo" || len(g.Rules) != 1 {
		t.Fatalf("first group %s with %d rules", g.Name, len(g.Rules))
	}
	r := b.RuleGroups.Groups[0].Rules[0]
	if r.Alert != rules.AlertTelemetryMissing || r.Labels[rules.LabelSeverity] != "critical" || r.Labels[LabelSystem] != "otel-demo" ||
		r.Labels[LabelEnvironment] != "demo" || time.Duration(r.For) != rules.NoDataPending {
		t.Errorf("rule %s for %s labels %v", r.Alert, r.For, r.Labels)
	}
	if _, ok := r.Labels[rules.LabelService]; ok {
		t.Errorf("system rule has a service label: %v", r.Labels)
	}
	for _, want := range []string{`service_name=~"frontend-proxy|frontend|checkout|payment|cart|product-catalog|currency|ad|recommendation|shipping|quote|email|orders|fraud-detection|accounting|valkey-cart"}`, "[3m]", "[1d:10m]"} {
		if !strings.Contains(r.Expr, want) {
			t.Errorf("expression lacks %s:\n%s", want, r.Expr)
		}
	}
	if r.Annotations["runbook_url"] != "http://kamerton.local/systems/otel-demo/runbooks/_system/KamertonTelemetryMissing" {
		t.Errorf("runbook link %q", r.Annotations["runbook_url"])
	}
	if !strings.Contains(r.Annotations[AnnotationMessageText], "Until data returns no other alert of this system can fire") ||
		!strings.Contains(r.Annotations[AnnotationMessageText], "for 3m") || !strings.Contains(r.Annotations[AnnotationMessageAction], "out of order sample") {
		t.Errorf("default message: %v", r.Annotations)
	}
	md := b.Runbooks[SystemRunbook+".md"]
	if !strings.Contains(md, "# Runbook: system otel-demo") || !strings.Contains(md, "### KamertonTelemetryMissing") || !strings.Contains(md, "clock skew on the nodes") {
		t.Errorf("system runbook:\n%s", md)
	}
}

func TestServiceNoDataRulesOfTheDemo(t *testing.T) {
	b := loadExample(t)
	got := byService(b, rules.AlertServiceTelemetryMissing)
	if len(got) != 14 {
		t.Errorf("%d per-service no-data rules, want 14 (http and grpc services)", len(got))
	}
	for _, s := range []string{"orders", "valkey-cart"} {
		if _, ok := got[s]; ok {
			t.Errorf("%s: a %s gets no-data only when set on the service", s, "queue or database")
		}
	}
	// Severity follows the traffic rule where it is on (critical http/grpc), else warning.
	for s, want := range map[string]string{"checkout": "critical", "payment": "critical", "currency": "warning", "email": "warning"} {
		if r := got[s]; r.Labels[rules.LabelSeverity] != want {
			t.Errorf("%s: severity %s, want %s", s, r.Labels[rules.LabelSeverity], want)
		}
	}
	r := got["checkout"]
	if strings.Contains(r.Expr, `"checkout|`) || strings.Contains(r.Expr, `|checkout|`) || !strings.Contains(r.Expr, `service_name=~"frontend-proxy|frontend|payment|`) {
		t.Errorf("others must leave out the service itself:\n%s", r.Expr)
	}
	if r.Annotations["runbook_url"] != "http://kamerton.local/runbooks/checkout/KamertonServiceTelemetryMissing" ||
		!strings.Contains(r.Annotations[AnnotationMessageText], "Kamerton cannot see this service") {
		t.Errorf("annotations: %v", r.Annotations)
	}
	if md := b.Runbooks["checkout.md"]; !strings.Contains(md, "### KamertonServiceTelemetryMissing") || !strings.Contains(md, "### KamertonNoReadyPods") {
		t.Errorf("service runbook lacks the new alerts:\n%s", md)
	}
	// The traffic rule leaves services without data to the no-data rule.
	if tr := trafficRules(b)["checkout"]; !strings.Contains(tr.Expr, "and on (service_name)\n  group by (service_name) (present_over_time(traces_span_metrics_calls_total{service_name=\"checkout\"}[3m]))") {
		t.Errorf("traffic rule without the presence condition:\n%s", tr.Expr)
	}
}

const noDataDoc = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  defaults:
    noData: {after: 5m}
  noData:
    after: 4m
    message: {title: "Pipeline down"}
  notifications:
    language: ru
  services:
    - name: api
      type: http
      critical: true
      noData: {enabled: false}
    - name: worker
      type: grpc
      noData: {severity: critical}
      noReadyPods: {for: 2m, severity: warning, message: {text: "Поды {service} не готовы"}}
    - name: db
      type: database
      noData: {}
      noReadyPods: {enabled: false}
`

func TestNoDataAndNoReadyPodsSettings(t *testing.T) {
	b := generateYAML(t, noDataDoc)
	sys := b.RuleGroups.Groups[0].Rules[0]
	if !strings.Contains(sys.Expr, "[4m]") || sys.Annotations[AnnotationMessageTitle] != "Pipeline down" ||
		!strings.Contains(sys.Annotations[AnnotationMessageText], "Kamerton не видит эту систему") ||
		sys.Annotations[rules.AnnotationTitle] != "Kamerton не видит систему: данные не поступают" {
		t.Errorf("system rule: %s %v", sys.Expr, sys.Annotations)
	}
	nd := byService(b, rules.AlertServiceTelemetryMissing)
	if _, ok := nd["api"]; ok {
		t.Error("api: turned off, still generated")
	}
	if r := nd["worker"]; r.Labels[rules.LabelSeverity] != "critical" || !strings.Contains(r.Expr, "[5m]") ||
		r.Annotations[rules.AnnotationValue] != "нет span-метрик дольше 5m" {
		t.Errorf("worker: %v\n%s", r.Labels, r.Expr)
	}
	if r, ok := nd["db"]; !ok || r.Labels[rules.LabelSeverity] != "warning" {
		t.Errorf("db: noData set on a database turns it on: %v", r.Labels)
	}
	// With its no-data alert off, the traffic rule of api counts vanished series as zero again.
	if tr := trafficRules(b)["api"]; strings.Contains(tr.Expr, "present_over_time") {
		t.Errorf("api traffic rule keeps the presence condition:\n%s", tr.Expr)
	}

	pods := byService(b, rules.AlertNoReadyPods)
	if _, ok := pods["db"]; ok {
		t.Error("db: no-ready-pods turned off, still generated")
	}
	if r := pods["api"]; r.Labels[rules.LabelSeverity] != "critical" || time.Duration(r.For) != 30*time.Second || r.Annotations[rules.AnnotationTitle] != "Нет готовых подов" ||
		!strings.Contains(r.Expr, `kube_pod_status_ready{pod=~"api-([a-z0-9]+-[a-z0-9]{5}|[0-9]+)", condition="true"}`) {
		t.Errorf("api: for %s labels %v\n%s", r.For, r.Labels, r.Expr)
	}
	if r := pods["worker"]; r.Labels[rules.LabelSeverity] != "warning" || time.Duration(r.For) != 2*time.Minute || r.Annotations[AnnotationMessageText] != "Поды worker не готовы" {
		t.Errorf("worker: for %s labels %v annotations %v", r.For, r.Labels, r.Annotations)
	}
	if md := b.Runbooks[SystemRunbook+".md"]; !strings.Contains(md, "# Runbook: система shop") || !strings.Contains(md, "расхождение часов на узлах") {
		t.Errorf("russian system runbook:\n%s", md)
	}
}

func TestNoDataSwitchesOff(t *testing.T) {
	b := generateYAML(t, `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: solo}
spec:
  noData: {enabled: false}
  telemetry:
    containers: {readyMetric: "-"}
  services:
    - name: api
      type: http
      critical: true
`)
	if n := rules.Count(b.RuleGroups); len(byService(b, rules.AlertTelemetryMissing))+len(byService(b, rules.AlertNoReadyPods)) != 0 {
		t.Errorf("switched off, still generated (%d rules)", n)
	}
	// One service: no "others" to compare with, so no per-service rule either.
	if len(byService(b, rules.AlertServiceTelemetryMissing)) != 0 {
		t.Error("a single service got a per-service no-data rule")
	}
	if _, ok := b.Runbooks[SystemRunbook+".md"]; ok {
		t.Error("system runbook without system rules")
	}
}

func TestNoReadyPodsOfTheDemo(t *testing.T) {
	b := loadExample(t)
	got := byService(b, rules.AlertNoReadyPods)
	if len(got) != 16 {
		t.Errorf("%d no-ready-pods rules, want one per service (16)", len(got))
	}
	if r := got["orders"]; !strings.Contains(r.Expr, `kube_pod_status_ready{namespace="otel-demo", pod=~"kafka-.*", condition="true"}`) {
		t.Errorf("orders: workload locator not used:\n%s", r.Expr)
	}
	if r := got["currency"]; r.Labels[rules.LabelSeverity] != "warning" || r.Labels[rules.LabelSignal] != string(rules.SignalAvailability) {
		t.Errorf("currency: %v", r.Labels)
	}
}

func TestDependencyChecks(t *testing.T) {
	res := mustDescription(t, noDataDoc)
	checks := DependencyChecks(res, &res.Spec.Services[1])
	var names []string
	for _, c := range checks {
		names = append(names, c.Alert)
		if err := rules.ValidateExpr(c.Expr); err != nil {
			t.Errorf("%s: %v", c.Alert, err)
		}
	}
	if strings.Join(names, ",") != "KamertonNoReadyPods,KamertonRequestsStopped,KamertonErrorRateHigh" {
		t.Errorf("checks = %v", names)
	}
	// The traffic check of worker exists although its rule is off (worker is not critical).
	if strings.Contains(checks[1].Expr, "present_over_time") {
		t.Errorf("the traffic check must count vanished series as no calls:\n%s", checks[1].Expr)
	}
	db := DependencyChecks(res, &res.Spec.Services[2])
	if len(db) != 3 || db[0].Alert != rules.AlertNoReadyPods {
		t.Errorf("db checks: %+v", db)
	}
}
