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

func TestSystemNoDataRule(t *testing.T) {
	r := SystemNoData(SystemNoDataParams{
		Meta:   Meta{Service: "ignored", Severity: "critical", For: time.Hour, Labels: map[string]string{"kamerton_system": "shop"}},
		Target: spanTarget(), After: 3 * time.Minute, Services: []string{"frontend", "checkout"},
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
	r := ServiceNoData(ServiceNoDataParams{Meta: meta(), Target: spanTarget(), After: 3 * time.Minute, Others: []string{"frontend", "payment"}})
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
  group(present_over_time(traces_span_metrics_calls_total{service_name=~"frontend|payment"}[3m]))`
	if r.Expr != want {
		t.Errorf("expr:\n%s\nwant:\n%s", r.Expr, want)
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

func systemNoDataExpr(t *testing.T) string {
	t.Helper()
	r := SystemNoData(SystemNoDataParams{Meta: Meta{Severity: "critical"}, Target: noDataTarget(), After: 3 * time.Minute, Services: []string{"pay", "web", "cart"}})
	mustValidate(t, r)
	return oneLine(r.Expr)
}

func serviceNoDataExpr(t *testing.T) string {
	t.Helper()
	r := ServiceNoData(ServiceNoDataParams{Meta: Meta{Service: "pay", Severity: "warning"}, Target: noDataTarget(), After: 3 * time.Minute, Others: []string{"web", "cart"}})
	mustValidate(t, r)
	return oneLine(r.Expr)
}

// trafficGuardedExpr is the traffic rule as generated next to ServiceNoData.
func trafficGuardedExpr(t *testing.T) string {
	t.Helper()
	p := TrafficParams{
		Meta: Meta{Service: "pay", Severity: "critical"}, Target: noDataTarget(),
		Threshold: 0.1, MinRequestsPerSecond: 0.02, SystemServices: []string{"pay", "web", "cart"}, PresentWithin: 3 * time.Minute,
	}
	r := TrafficStopped(p)
	mustValidate(t, r)
	return oneLine(r.Expr)
}

func runPromQL(t *testing.T, script string) {
	t.Helper()
	promqltest.RunTest(t, script, promqltest.NewTestEngine(t, false, 0, promqltest.DefaultMaxSamplesPerQuery))
}

// Samples come once a minute, as from a Collector flushing span metrics; a
// series that ends models data that stopped arriving (remote write leaves no
// staleness marker). Unless said otherwise data stops at minute 180.
func TestSystemNoDataScenarios(t *testing.T) {
	expr := systemNoDataExpr(t)
	for _, tc := range []struct{ name, script string }{
		{"steady data: no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x360
  calls{service_name="web", span_kind="SPAN_KIND_CLIENT"} 0+60x360

eval range from 1h to 6h step 1m ` + expr + `
`},
		{"pipeline stops for every service: fires once nothing came for 3m, value is the services seen", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180
  calls{service_name="web", span_kind="SPAN_KIND_SERVER"} 0+60x180
  calls{service_name="cart", span_kind="SPAN_KIND_INTERNAL"} 0+30x180
  calls{service_name="other-system", span_kind="SPAN_KIND_SERVER"} 0+30x360

eval instant at 182m ` + expr + `

eval instant at 184m ` + expr + `
  {} 3

eval instant at 300m ` + expr + `
  {} 3
`},
		{"one service stops, the others send: no system alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180
  calls{service_name="web", span_kind="SPAN_KIND_SERVER"} 0+60x360

eval range from 1h to 6h step 1m ` + expr + `
`},
		{"system that never sent anything: no alert", `
load 1m
  calls{service_name="other-system", span_kind="SPAN_KIND_SERVER"} 0+30x360

eval range from 1h to 6h step 1m ` + expr + `
`},
		{"data stopped more than a day ago: the alert resolves on its own", `
load 10m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+60x6

eval instant at 2h ` + expr + `
  {} 1

eval instant at 1d ` + expr + `
  {} 1

eval instant at 26h ` + expr + `
`},
	} {
		t.Run(tc.name, func(t *testing.T) { runPromQL(t, tc.script) })
	}
}

// others is the rest of the system for the per-service scenarios: steady for 6 hours.
const noDataOthers = `
  calls{service_name="web", span_kind="SPAN_KIND_SERVER"} 0+60x360
  calls{service_name="cart", span_kind="SPAN_KIND_CLIENT"} 0+30x360
`

func TestServiceNoDataScenarios(t *testing.T) {
	expr := serviceNoDataExpr(t)
	for _, tc := range []struct{ name, script string }{
		{"steady data: no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x360` + noDataOthers + `
eval range from 1h to 6h step 1m ` + expr + `
`},
		{"only client spans of the service: still counts as data", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_CLIENT"} 0+6x360` + noDataOthers + `
eval range from 1h to 6h step 1m ` + expr + `
`},
		{"its data stops while the others send: fires after 3m", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180` + noDataOthers + `
eval instant at 182m ` + expr + `

eval instant at 184m ` + expr + `
  {service_name="pay"} 1

eval instant at 6h ` + expr + `
  {service_name="pay"} 1
`},
		{"everybody stops: left to the system alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180
  calls{service_name="web", span_kind="SPAN_KIND_SERVER"} 0+60x180
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER"} 0+30x180

eval range from 181m to 6h step 1m ` + expr + `
`},
		{"never instrumented or added to the description before sending: no alert", `
load 1m` + noDataOthers + `
eval range from 1h to 6h step 1m ` + expr + `
`},
		{"gone for more than a day (renamed or removed): resolves on its own", `
load 10m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+60x6
  calls{service_name="web", span_kind="SPAN_KIND_SERVER"} 0+60x200

eval instant at 2h ` + expr + `
  {service_name="pay"} 1

eval instant at 26h ` + expr + `
`},
		{"collector restart re-creates the series under a new label: no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER", collector="a"} 0+6x180
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER", collector="b"} _x182 0+6x178` + noDataOthers + `
eval range from 1h to 6h step 1m ` + expr + `
`},
	} {
		t.Run(tc.name, func(t *testing.T) { runPromQL(t, tc.script) })
	}
}

// The traffic rule and the per-service "no data" rule never fire at the same
// time: calls far below normal while series exist, or no series at all.
func TestTrafficAndNoDataAreComplementary(t *testing.T) {
	nodata, traffic := serviceNoDataExpr(t), trafficGuardedExpr(t)
	runPromQL(t, `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180 1080x180`+noDataOthers+`
eval instant at 185m `+traffic+`
  {service_name="pay"} 0

eval range from 1h to 6h step 1m `+nodata+`
`)
	runPromQL(t, `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180`+noDataOthers+`
eval range from 183m to 6h step 1m `+traffic+`

eval instant at 185m `+nodata+`
  {service_name="pay"} 1
`)
}
