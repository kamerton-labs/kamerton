// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql/promqltest"
)

func noDataTarget() SpanTarget {
	return SpanTarget{
		CallsMetric: "calls", ServiceLabel: "service_name", OperationLabel: "span_name", SpanKindLabel: "span_kind",
		SpanKinds: []string{"SPAN_KIND_SERVER", "SPAN_KIND_CONSUMER"}, ServiceValue: "pay", Window: 2 * time.Minute,
	}
}

func oneLine(expr string) string { return strings.Join(strings.Fields(expr), " ") }

func runPromQL(t *testing.T, script string) {
	t.Helper()
	promqltest.RunTest(t, script, promqltest.NewTestEngine(t, false, 0, promqltest.DefaultMaxSamplesPerQuery))
}

func TestSystemNoDataRule(t *testing.T) {
	r := SystemNoData(SystemNoDataParams{
		Meta:   Meta{Service: "ignored", Severity: "critical", For: time.Hour, Labels: map[string]string{"kamerton_system": "shop"}},
		Target: spanTarget(), After: 3 * time.Minute, Services: []string{"frontend", "checkout"}, MinRequestsPerSecond: 0.02,
	})
	mustValidate(t, r)
	if _, ok := r.Labels[LabelService]; ok {
		t.Errorf("the system alert has a service label: %v", r.Labels)
	}
	if r.Alert != AlertTelemetryMissing || r.Labels[LabelSignal] != string(SignalNoData) || r.Labels["kamerton_system"] != "shop" || r.Labels[LabelSeverity] != "critical" {
		t.Errorf("alert %s labels %v", r.Alert, r.Labels)
	}
	if time.Duration(r.For) != NoDataPending {
		t.Errorf("for = %s, want %s", r.For, NoDataPending)
	}
	want := `count(
  count_over_time((group by (service_name) (traces_span_metrics_calls_total{service_name=~"frontend|checkout"}))[1d:10m])
)
unless on ()
group(
  present_over_time(traces_span_metrics_calls_total{service_name=~"frontend|checkout"}[3m])
)
and on ()
(
  (
    avg_over_time((sum (rate(traces_span_metrics_calls_total{service_name=~"frontend|checkout"}[10m])))[2h:10m] offset 6d23h)
  and on ()
    count_over_time((sum (rate(traces_span_metrics_calls_total{service_name=~"frontend|checkout"}[10m])))[2h:10m] offset 6d23h) >= 12
  ) > 0.02
or on ()
  (
    (
      avg_over_time((sum (rate(traces_span_metrics_calls_total{service_name=~"frontend|checkout"}[10m])))[2h:10m] offset 23h)
    and on ()
      count_over_time((sum (rate(traces_span_metrics_calls_total{service_name=~"frontend|checkout"}[10m])))[2h:10m] offset 23h) >= 12
    ) > 0.02
  unless on ()
    count_over_time((group by () (traces_span_metrics_calls_total{service_name=~"frontend|checkout"}))[1d:1h] offset 1w)
  )
)`
	if r.Expr != want {
		t.Errorf("expr:\n%s\nwant:\n%s", r.Expr, want)
	}
	a := r.Annotations
	if a[AnnotationTitle] != "Kamerton cannot see the system: no data arrives" ||
		a[AnnotationValue] != `no span metrics from any of {{ $value | printf "%.0f" }} services for over 3m; other alerts cannot fire` ||
		!strings.Contains(a["description"], "other alerts of this system will not fire until data returns") {
		t.Errorf("english annotations: %v", a)
	}

	ru := SystemNoData(SystemNoDataParams{Meta: Meta{Severity: "critical", Language: LanguageRussian}, Target: spanTarget(), After: 5 * time.Minute, Services: []string{"a"}})
	mustValidate(t, ru)
	if ru.Annotations[AnnotationTitle] != "Kamerton не видит систему: данные не поступают" ||
		ru.Annotations[AnnotationValue] != `ни один из {{ $value | printf "%.0f" }} сервисов не присылает span-метрики дольше 5m, остальные оповещения не сработают` {
		t.Errorf("russian annotations: %v", ru.Annotations)
	}
	if !strings.Contains(ru.Expr, `{service_name="a"}`) {
		t.Errorf("one service is matched exactly: %s", ru.Expr)
	}
}

func TestServiceNoDataRule(t *testing.T) {
	r := ServiceNoData(ServiceNoDataParams{Meta: meta(), Target: spanTarget(), After: 3 * time.Minute, Others: []string{"frontend", "payment"}, MinRequestsPerSecond: 0.05})
	mustValidate(t, r)
	if r.Alert != AlertServiceTelemetryMissing || r.Labels[LabelService] != "checkout" || r.Labels[LabelSignal] != string(SignalNoData) || time.Duration(r.For) != NoDataPending {
		t.Errorf("alert %s for %s labels %v", r.Alert, r.For, r.Labels)
	}
	want := `group by (service_name) (
  count_over_time((group by (service_name) (traces_span_metrics_calls_total{service_name="checkout"}))[1d:10m])
)
unless on (service_name)
  group by (service_name) (present_over_time(traces_span_metrics_calls_total{service_name="checkout"}[3m]))
and on ()
  group(present_over_time(traces_span_metrics_calls_total{service_name=~"frontend|payment"}[3m]))
and on (service_name)
(
  (
    avg_over_time((sum by (service_name) (rate(traces_span_metrics_calls_total{service_name="checkout"}[10m])))[2h:10m] offset 6d23h)`
	if !strings.HasPrefix(r.Expr, want) || !strings.Contains(r.Expr, ") > 0.05\nor on (service_name)") ||
		!strings.HasSuffix(r.Expr, `count_over_time((group by (service_name) (traces_span_metrics_calls_total{service_name="checkout"}))[1d:1h] offset 1w)
  )
)`) {
		t.Errorf("expr:\n%s\nwant prefix:\n%s", r.Expr, want)
	}
	tp := TrafficParams{Meta: meta(), Target: spanTarget(), Threshold: 0.1, MinRequestsPerSecond: 0.02, SystemServices: []string{"frontend", "checkout", "payment"}}
	withTraffic := ServiceNoData(ServiceNoDataParams{Meta: meta(), Target: spanTarget(), After: 3 * time.Minute, Others: []string{"frontend", "payment"}, MinRequestsPerSecond: 0.02, Traffic: &tp})
	mustValidate(t, withTraffic)
	if !strings.HasPrefix(withTraffic.Expr, r.Expr[:strings.Index(r.Expr, "and on (service_name)\n(")]) ||
		!strings.Contains(withTraffic.Expr, "\nunless on (service_name)\n(\n  (\n    avg_over_time(") || !strings.Contains(withTraffic.Expr, "[1d:1h] offset 5m))\n)\n)") {
		t.Errorf("a stop the traffic rule reports is left to it:\n%s", withTraffic.Expr)
	}
	if r.Annotations[AnnotationTitle] != "No data from the service" || r.Annotations[AnnotationValue] != "no span metrics for over 3m" ||
		r.Annotations["summary"] != "No telemetry from checkout" {
		t.Errorf("english annotations: %v", r.Annotations)
	}
	m := meta()
	m.Language = LanguageRussian
	ru := ServiceNoData(ServiceNoDataParams{Meta: m, Target: spanTarget(), After: 4 * time.Minute, Others: []string{"frontend"}})
	if ru.Annotations[AnnotationTitle] != "От сервиса не поступают данные" || ru.Annotations[AnnotationValue] != "нет span-метрик дольше 4m" {
		t.Errorf("russian annotations: %v", ru.Annotations)
	}
}
