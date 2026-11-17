// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
	"github.com/prometheus/prometheus/promql/parser"
)

func spanTarget() SpanTarget {
	return SpanTarget{
		CallsMetric:       "traces_span_metrics_calls_total",
		BucketMetric:      "traces_span_metrics_duration_milliseconds_bucket",
		ServiceLabel:      "service_name",
		OperationLabel:    "span_name",
		SpanKindLabel:     "span_kind",
		StatusCodeLabel:   "status_code",
		ErrorStatusValue:  "STATUS_CODE_ERROR",
		SpanKinds:         []string{"SPAN_KIND_SERVER", "SPAN_KIND_CONSUMER"},
		ServiceValue:      "checkout",
		ExcludeOperations: []string{`flagd\.evaluation\.v2\.Service/EventStream`},
		Window:            5 * time.Minute,
	}
}

func podTarget() PodTarget {
	return PodTarget{
		MemoryMetric:   "container_memory_working_set_bytes",
		CPUMetric:      "container_cpu_usage_seconds_total",
		RestartsMetric: "kube_pod_container_status_restarts_total",
		NamespaceLabel: "namespace",
		PodLabel:       "pod",
		Namespace:      "otel-demo",
		PodRegex:       "checkout-.*",
		Window:         5 * time.Minute,
	}
}

func meta() Meta {
	return Meta{Service: "checkout", Severity: "critical", For: 5 * time.Minute, Labels: map[string]string{"environment": "demo"}}
}

func mustValidate(t *testing.T, rs ...rulefmt.Rule) string {
	t.Helper()
	out, err := RenderAndValidate(rulefmt.RuleGroups{Groups: []rulefmt.RuleGroup{{Name: "test", Rules: rs}}})
	if err != nil {
		t.Fatalf("generated rules rejected by Prometheus rulefmt: %v\n%s", err, out)
	}
	p := parser.NewParser(parser.Options{})
	for _, r := range rs {
		e, err := p.ParseExpr(r.Expr)
		if err != nil {
			t.Fatalf("parse %q: %v", r.Expr, err)
		}
		if e.Type() != parser.ValueTypeVector {
			t.Fatalf("expression of %s is %s, want vector", r.Alert, e.Type())
		}
	}
	return string(out)
}

func TestErrorRate(t *testing.T) {
	r := ErrorRate(ErrorRateParams{Meta: meta(), Target: spanTarget(), Threshold: 0.05, MinRequestsPerSecond: 0.1})
	mustValidate(t, r)

	want := `(
(
  sum by (service_name) (rate(traces_span_metrics_calls_total{service_name="checkout", span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER", span_name!~"flagd\\.evaluation\\.v2\\.Service/EventStream", status_code="STATUS_CODE_ERROR"}[5m]))
/
  sum by (service_name) (rate(traces_span_metrics_calls_total{service_name="checkout", span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER", span_name!~"flagd\\.evaluation\\.v2\\.Service/EventStream"}[5m]))
) > 0.05
)
and on (service_name)
(
  sum by (service_name) (rate(traces_span_metrics_calls_total{service_name="checkout", span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER", span_name!~"flagd\\.evaluation\\.v2\\.Service/EventStream"}[5m])) > 0.1
)`
	if r.Expr != want {
		t.Errorf("unexpected expression:\n%s\nwant:\n%s", r.Expr, want)
	}
	if r.Alert != AlertErrorRate || r.Labels[LabelService] != "checkout" || r.Labels[LabelSeverity] != "critical" || r.Labels["environment"] != "demo" {
		t.Errorf("unexpected alert/labels: %s %v", r.Alert, r.Labels)
	}
	if r.For.String() != "5m" {
		t.Errorf("for = %s", r.For)
	}
}

func TestErrorRatePerOperation(t *testing.T) {
	r := ErrorRate(ErrorRateParams{Meta: meta(), Target: spanTarget(), Threshold: 0.05, MinRequestsPerSecond: 0.01, PerOperation: true})
	mustValidate(t, r)
	if r.Alert != AlertOperationErrorRate {
		t.Errorf("alert = %s", r.Alert)
	}
	if !strings.Contains(r.Expr, "sum by (service_name, span_name)") || !strings.Contains(r.Expr, "and on (service_name, span_name)") {
		t.Errorf("per-operation expression must group by operation:\n%s", r.Expr)
	}
}

func TestErrorRateSingleOperation(t *testing.T) {
	tg := spanTarget()
	tg.Operation = "oteldemo.CheckoutService/PlaceOrder"
	r := ErrorRate(ErrorRateParams{Meta: meta(), Target: tg, Threshold: 0.01, MinRequestsPerSecond: 0.01})
	mustValidate(t, r)
	if !strings.Contains(r.Expr, `span_name="oteldemo.CheckoutService/PlaceOrder"`) {
		t.Errorf("expected exact operation matcher:\n%s", r.Expr)
	}
	if strings.Contains(r.Expr, "flagd") {
		t.Errorf("exclusions must not apply to an explicit operation:\n%s", r.Expr)
	}
	if r.Labels[LabelOperation] != tg.Operation {
		t.Errorf("operation label = %q", r.Labels[LabelOperation])
	}
}

func TestLatency(t *testing.T) {
	r := Latency(LatencyParams{Meta: meta(), Target: spanTarget(), Quantile: 0.95, ThresholdMs: 500, MinRequestsPerSecond: 0.1})
	mustValidate(t, r)
	if !strings.HasPrefix(r.Expr, `histogram_quantile(0.95, sum by (service_name, le) (rate(traces_span_metrics_duration_milliseconds_bucket{`) {
		t.Errorf("unexpected expression:\n%s", r.Expr)
	}
	if !strings.Contains(r.Expr, "[5m]))) > 500\n") {
		t.Errorf("threshold must be in milliseconds:\n%s", r.Expr)
	}

	sec := spanTarget()
	sec.DurationInSeconds = true
	sec.BucketMetric = "http_server_duration_seconds_bucket"
	r = Latency(LatencyParams{Meta: meta(), Target: sec, Quantile: 0.99, ThresholdMs: 250})
	mustValidate(t, r)
	if !strings.Contains(r.Expr, "> 0.25\n") {
		t.Errorf("threshold must be converted to seconds:\n%s", r.Expr)
	}
}

func TestSaturation(t *testing.T) {
	mem := Memory(MemoryParams{Meta: meta(), Target: podTarget(), Bytes: 512 * 1024 * 1024})
	cpu := CPU(CPUParams{Meta: meta(), Target: podTarget(), Cores: 0.5})
	rst := Restarts(RestartsParams{Meta: meta(), Target: podTarget(), MaxPerHour: 3})
	mustValidate(t, mem, cpu, rst)

	if want := `max by (namespace, pod) (container_memory_working_set_bytes{namespace="otel-demo", pod=~"checkout-.*"}) > 536870912`; mem.Expr != want {
		t.Errorf("memory expr:\n%s\nwant:\n%s", mem.Expr, want)
	}
	if want := `max by (namespace, pod) (rate(container_cpu_usage_seconds_total{namespace="otel-demo", pod=~"checkout-.*"}[5m])) > 0.5`; cpu.Expr != want {
		t.Errorf("cpu expr:\n%s\nwant:\n%s", cpu.Expr, want)
	}
	if want := `sum by (namespace, pod) (increase(kube_pod_container_status_restarts_total{namespace="otel-demo", pod=~"checkout-.*"}[1h])) > 3`; rst.Expr != want {
		t.Errorf("restarts expr:\n%s\nwant:\n%s", rst.Expr, want)
	}
}

func TestQueueLag(t *testing.T) {
	r := QueueLag(QueueLagParams{
		Meta:   meta(),
		Target: QueueTarget{LagMetric: "kafka_consumer_group_lag_ratio", GroupLabel: "group", TopicLabel: "topic", Topic: "orders", Groups: []string{"fraud-detection", "accounting"}},
		MaxLag: 100,
	})
	mustValidate(t, r)
	if want := `max by (group, topic) (kafka_consumer_group_lag_ratio{topic="orders", group=~"fraud-detection|accounting"}) > 100`; r.Expr != want {
		t.Errorf("queue expr:\n%s\nwant:\n%s", r.Expr, want)
	}
}

func TestSLOBurnRate(t *testing.T) {
	rs := SLOBurnRate(SLOParams{Meta: meta(), Target: spanTarget(), Objective: 99.5, Window: 30 * 24 * time.Hour})
	mustValidate(t, rs...)
	if len(rs) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rs))
	}
	if !strings.Contains(rs[0].Expr, "/ 0.005\n) > 14.4") || !strings.Contains(rs[0].Expr, "[1h]") || !strings.Contains(rs[0].Expr, "[5m]") {
		t.Errorf("fast burn must use factor 14.4 over 1h and 5m:\n%s", rs[0].Expr)
	}
	if !strings.Contains(rs[1].Expr, ") > 6\n") || !strings.Contains(rs[1].Expr, "[6h]") || !strings.Contains(rs[1].Expr, "[30m]") {
		t.Errorf("slow burn must use factor 6 over 6h and 30m:\n%s", rs[1].Expr)
	}
	if rs[0].Labels[LabelSeverity] != "critical" || rs[1].Labels[LabelSeverity] != "warning" {
		t.Errorf("severities: %v / %v", rs[0].Labels, rs[1].Labels)
	}
}

func TestQuotingOfHostileValues(t *testing.T) {
	tg := spanTarget()
	tg.ServiceValue = `we"ird\service`
	r := ErrorRate(ErrorRateParams{Meta: meta(), Target: tg, Threshold: 0.1})
	mustValidate(t, r)
}

func TestValidateRejectsBrokenRules(t *testing.T) {
	bad := []string{
		"groups:\n- name: x\n  rules:\n  - alert: A\n    expr: sum(rate(foo[5m])\n",
		"groups:\n- name: x\n  rules:\n  - alert: A\n    expr: up\n    annotations:\n      summary: '{{ $labels.x '\n",
		"groups:\n- name: x\n  rules:\n  - alert: A\n    expr: up\n    unknown_field: 1\n",
	}
	for _, b := range bad {
		if err := Validate([]byte(b)); err == nil {
			t.Errorf("expected error for:\n%s", b)
		}
	}
}
