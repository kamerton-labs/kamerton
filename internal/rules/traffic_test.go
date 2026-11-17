// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql/promqltest"
)

func trafficParams() TrafficParams {
	t := spanTarget()
	t.Window = 2 * time.Minute
	return TrafficParams{
		Meta: meta(), Target: t, Threshold: 0.1, MinRequestsPerSecond: 0.02,
		SystemServices: []string{"frontend", "checkout", "payment"},
	}
}

func TestTrafficStoppedRendersAndValidates(t *testing.T) {
	r := TrafficStopped(trafficParams())
	mustValidate(t, r)
	if r.Alert != AlertRequestsStopped || r.Labels[LabelSignal] != string(SignalTraffic) || r.Labels[LabelService] != "checkout" {
		t.Errorf("alert %s labels %v", r.Alert, r.Labels)
	}
	for _, want := range []string{
		// Incoming calls of the service only, without excluded operations.
		`sum by (service_name) (rate(traces_span_metrics_calls_total{service_name="checkout", span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER", span_name!~"flagd\\.evaluation\\.v2\\.Service/EventStream"}[2m]))`,
		// An absent series counts as zero.
		"or on (service_name)\n    0 * (",
		// Normal: average of 10m rates over a day, with at least an hour of history.
		`[10m])))[1d:10m])`,
		`[1d:10m]) >= 6`,
		"<= on (service_name)\n  0.1 * (",
		") > 0.02)",
		// Telemetry flowing for at least half of the system's services, now and one window ago.
		`service_name=~"frontend|checkout|payment"`,
		`>= 0.5 * count(count_over_time((group by (service_name) (`,
		`[2m] offset 2m)))`,
		`[1d:1h] offset 2m))`,
	} {
		if !strings.Contains(r.Expr, want) {
			t.Errorf("expression lacks %q:\n%s", want, r.Expr)
		}
	}
	if !strings.HasPrefix(r.Expr, "60 * (") {
		t.Errorf("value must be calls per minute:\n%s", r.Expr)
	}
	a := r.Annotations
	if a[AnnotationTitle] != "Requests stopped arriving" || a[AnnotationThreshold] != "10% of normal" {
		t.Errorf("annotations: %v", a)
	}
	if !strings.HasPrefix(a[AnnotationValue], `{{ $value | printf "%.1f" }} per min (normally {{ range query `+"`60 * (") {
		t.Errorf("value annotation: %s", a[AnnotationValue])
	}
	if a["summary"] != "Requests stopped arriving: checkout" || !strings.Contains(a["description"], "at or below 10% of the normal rate") || !strings.Contains(a["description"], "above 1.2/min") {
		t.Errorf("english annotations: %v", a)
	}
}

func TestTrafficStoppedLanguageAndEdgeCases(t *testing.T) {
	p := trafficParams()
	p.Language = LanguageRussian
	p.Threshold = 0
	r := TrafficStopped(p)
	mustValidate(t, r)
	if r.Annotations[AnnotationTitle] != "Запросы перестали поступать" || r.Annotations[AnnotationThreshold] != "ни одного запроса" ||
		!strings.Contains(r.Annotations[AnnotationValue], "в мин (обычно ") {
		t.Errorf("russian: %v", r.Annotations)
	}
	if !strings.Contains(r.Expr, "<= on (service_name)\n  0 * (") {
		t.Errorf("threshold 0 must compare with 0 (absent or exactly zero):\n%s", r.Expr)
	}

	// A backtick cannot be embedded in the query of the value annotation:
	// the normal rate is left out, the rule stays valid.
	p = trafficParams()
	p.Target.ExcludeOperations = []string{"odd`name"}
	r = TrafficStopped(p)
	mustValidate(t, r)
	if strings.Contains(r.Annotations[AnnotationValue], "query") {
		t.Errorf("value annotation with a backtick in the query: %s", r.Annotations[AnnotationValue])
	}
}

// trafficExpr is the rule for service "pay" of a system with pay, web and
// cart, on a metric named "calls", as one line (the promqltest syntax).
func trafficExpr(t *testing.T) string {
	t.Helper()
	r := TrafficStopped(TrafficParams{
		Meta: Meta{Service: "pay", Severity: "critical"},
		Target: SpanTarget{
			CallsMetric: "calls", ServiceLabel: "service_name", OperationLabel: "span_name", SpanKindLabel: "span_kind",
			SpanKinds: []string{"SPAN_KIND_SERVER", "SPAN_KIND_CONSUMER"}, ServiceValue: "pay", Window: 2 * time.Minute,
		},
		Threshold: 0.1, MinRequestsPerSecond: 0.02, SystemServices: []string{"pay", "web", "cart"},
	})
	mustValidate(t, r)
	return strings.Join(strings.Fields(r.Expr), " ")
}

// Two services with steady traffic for 6 hours (sampled every minute), used
// as the rest of the system in the scenarios below.
const others = `
  calls{service_name="web", span_kind="SPAN_KIND_SERVER"} 0+60x360
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER"} 0+30x360
`

// The scenarios run the generated expression in Prometheus' own PromQL engine
// (promqltest) on synthetic series. "pay" normally gets 6 calls a minute
// (0.1/s); unless said otherwise its calls stop at minute 180.
func TestTrafficStoppedScenarios(t *testing.T) {
	expr := trafficExpr(t)
	for _, tc := range []struct{ name, script string }{
		{"steady traffic: no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x360` + others + `
eval range from 1h to 6h step 1m ` + expr + `
`},
		{"counter stops growing: fires with value 0", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180 1080x180` + others + `
eval instant at 180m ` + expr + `

eval instant at 185m ` + expr + `
  {service_name="pay"} 0

eval instant at 300m ` + expr + `
  {service_name="pay"} 0
`},
		{"series disappears: counts as zero", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180` + others + `
eval instant at 185m ` + expr + `
  {service_name="pay"} 0
`},
		{"drops to 5% of normal: fires; to 50%: does not", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180 1080+0.3x60 1098+3x60` + others + `
eval instant at 200m ` + expr + `
  {service_name="pay"} 0.3

eval instant at 280m ` + expr + `
`},
		{"service that appeared 20 minutes ago: no normal, no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} _x150 0+6x20` + others + `
eval range from 171m to 200m step 1m ` + expr + `
`},
		{"service that never had traffic: no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0x360` + others + `
eval range from 1h to 6h step 1m ` + expr + `
`},
		{"quiet service below the minimum normal rate: no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+0.6x180 108x180` + others + `
eval range from 181m to 6h step 1m ` + expr + `
`},
		{"telemetry pipeline stops for everyone: no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER"} 0+6x180
  calls{service_name="web", span_kind="SPAN_KIND_SERVER"} 0+60x180
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER"} 0+30x180

eval range from 181m to 6h step 15s ` + expr + `
`},
		{"collector restart re-creates every series: no alert", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER", collector="a"} 0+6x180
  calls{service_name="web", span_kind="SPAN_KIND_SERVER", collector="a"} 0+60x180
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER", collector="a"} 0+30x180
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER", collector="b"} _x181 0+6x179
  calls{service_name="web", span_kind="SPAN_KIND_SERVER", collector="b"} _x181 0+60x179
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER", collector="b"} _x181 0+30x179

eval range from 1h to 6h step 15s ` + expr + `
`},
		{"three hours without data, then data returns: normal survives the gap, no alert while services come back", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER", collector="a"} 0+6x120
  calls{service_name="web", span_kind="SPAN_KIND_SERVER", collector="a"} 0+60x120
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER", collector="a"} 0+30x120
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER", collector="b"} _x302 0+6x58
  calls{service_name="web", span_kind="SPAN_KIND_SERVER", collector="b"} _x300 0+60x60
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER", collector="b"} _x300 0+30x60

eval range from 121m to 6h step 15s ` + expr + `
`},
		{"after a gap the service stays silent while the others return: fires", `
load 1m
  calls{service_name="pay", span_kind="SPAN_KIND_SERVER", collector="a"} 0+6x120
  calls{service_name="web", span_kind="SPAN_KIND_SERVER", collector="a"} 0+60x120
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER", collector="a"} 0+30x120
  calls{service_name="web", span_kind="SPAN_KIND_SERVER", collector="b"} _x300 0+60x60
  calls{service_name="cart", span_kind="SPAN_KIND_SERVER", collector="b"} _x300 0+30x60

eval instant at 301m ` + expr + `

eval instant at 306m ` + expr + `
  {service_name="pay"} 0
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			promqltest.RunTest(t, tc.script, promqltest.NewTestEngine(t, false, 0, promqltest.DefaultMaxSamplesPerQuery))
		})
	}
}
