// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"

	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

func generateYAML(t *testing.T, doc string) *Bundle {
	t.Helper()
	res := description.Validate([]byte(doc))
	if !res.Valid {
		t.Fatalf("invalid: %+v", res.Errors)
	}
	b, err := Generate(res.Document, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// trafficRules returns the "requests stopped arriving" rules by service.
func trafficRules(b *Bundle) map[string]rulefmt.Rule {
	out := map[string]rulefmt.Rule{}
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			if r.Alert == rules.AlertRequestsStopped {
				out[r.Labels[rules.LabelService]] = r
			}
		}
	}
	return out
}

func TestTrafficRuleDefaultsToCriticalHTTPAndGRPC(t *testing.T) {
	b := loadExample(t)
	got := trafficRules(b)
	// Critical http/grpc services of the OTel Demo get the rule; quiet
	// non-critical ones (email, accounting, fraud-detection…), the queue and
	// the database do not.
	for _, s := range []string{"frontend-proxy", "frontend", "checkout", "payment", "cart", "product-catalog"} {
		r, ok := got[s]
		if !ok {
			t.Errorf("%s: no traffic rule", s)
			continue
		}
		if r.Labels[rules.LabelSeverity] != "critical" || time.Duration(r.For) != 2*time.Minute {
			t.Errorf("%s: severity %s for %s", s, r.Labels[rules.LabelSeverity], r.For)
		}
		if !strings.Contains(r.Expr, "[2m]") || !strings.Contains(r.Expr, "0.1 * (") || !strings.Contains(r.Expr, ") > 0.02)") {
			t.Errorf("%s: defaults (window 2m, 10%%, 0.02/s) not in expression:\n%s", s, r.Expr)
		}
	}
	for _, s := range []string{"email", "accounting", "fraud-detection", "currency", "orders", "valkey-cart"} {
		if _, ok := got[s]; ok {
			t.Errorf("%s: traffic rule must be off by default", s)
		}
	}
	// The telemetry guard lists every service of the system.
	if !strings.Contains(got["payment"].Expr, `service_name=~"frontend-proxy|frontend|checkout|payment|cart|product-catalog|currency|ad|recommendation|shipping|quote|email|orders|fraud-detection|accounting|valkey-cart"`) {
		t.Errorf("guard does not list the system's services:\n%s", got["payment"].Expr)
	}
	// Metric and label names come from spec.telemetry.
	if !strings.Contains(got["payment"].Expr, `traces_span_metrics_calls_total{service_name="payment", span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER", span_name!~"flagd\\.evaluation\\.v2\\.Service/EventStream"}`) {
		t.Errorf("selector:\n%s", got["payment"].Expr)
	}
}

const trafficDoc = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  telemetry:
    spans: {callsMetric: calls_total, serviceLabel: svc, spanKindLabel: kind, serverSpanKinds: [SERVER]}
  defaults:
    rateWindow: 3m
    trafficDrop: {enabled: true, threshold: 0.2, minRequestsPerSecond: 0.5, for: 4m}
  services:
    - name: api
      type: http
      telemetryName: api-server
    - name: worker
      type: grpc
      critical: true
      trafficDrop:
        enabled: false
    - name: billing
      type: grpc
      trafficDrop:
        threshold: 0
        severity: critical
        message: {title: "{service}: no orders since {since}", text: "Now {value}"}
    - name: db
      type: database
    - name: proxy-db
      type: database
      trafficDrop: {}
    - name: events
      type: queue
      queue: {topic: events, maxLag: 10}
`

func TestTrafficRuleLayersAndTypes(t *testing.T) {
	b := generateYAML(t, trafficDoc)
	got := trafficRules(b)
	if _, ok := got["worker"]; ok {
		t.Error("worker: turned off on the service")
	}
	if _, ok := got["db"]; ok {
		t.Error("db: database services get the rule only when set on the service")
	}
	if _, ok := got["events"]; ok {
		t.Error("events: queue services get the rule only when set on the service")
	}
	api, ok := got["api"]
	if !ok {
		t.Fatal("api: defaults.trafficDrop.enabled turns the rule on for non-critical services")
	}
	if api.Labels[rules.LabelSeverity] != "warning" || time.Duration(api.For) != 4*time.Minute {
		t.Errorf("api: severity %s, for %s", api.Labels[rules.LabelSeverity], api.For)
	}
	for _, want := range []string{
		`calls_total{svc="api-server", kind=~"SERVER"}[3m]`, // telemetry mapping, telemetryName, rate window
		"0.2 * (", ") > 0.5)",
		`svc=~"api-server|worker|billing|db|proxy-db|events"`,
		"[3m] offset 3m",
	} {
		if !strings.Contains(api.Expr, want) {
			t.Errorf("api expression lacks %q:\n%s", want, api.Expr)
		}
	}
	if _, ok := got["proxy-db"]; !ok {
		t.Error("proxy-db: trafficDrop on a database service turns the rule on")
	}
	bill := got["billing"]
	if bill.Labels[rules.LabelSeverity] != "critical" || !strings.Contains(bill.Expr, "<= on (svc)\n  0 * (") {
		t.Errorf("billing: severity %s\n%s", bill.Labels[rules.LabelSeverity], bill.Expr)
	}
	if bill.Annotations[rules.AnnotationThreshold] != "no calls at all" {
		t.Errorf("billing threshold: %q", bill.Annotations[rules.AnnotationThreshold])
	}
	if bill.Annotations[AnnotationMessageTitle] != "billing: no orders since {since}" ||
		!strings.HasPrefix(bill.Annotations[AnnotationMessageText], `Now {{ $value | printf "%.1f" }} per min (normally {{ range query`) {
		t.Errorf("team's text: %v", bill.Annotations)
	}
	// The runbook lists the rule.
	if !strings.Contains(b.Runbooks["billing.md"], "### KamertonRequestsStopped") {
		t.Errorf("runbook:\n%s", b.Runbooks["billing.md"])
	}
}

func TestTrafficRuleNeedsSpanMetrics(t *testing.T) {
	doc := strings.Replace(trafficDoc, "callsMetric: calls_total", `callsMetric: "-"`, 1)
	if got := trafficRules(generateYAML(t, doc)); len(got) != 0 {
		t.Errorf("no span metrics, no traffic rules: %v", got)
	}
}
