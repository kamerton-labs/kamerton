// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/generator"
)

// fakeProm answers the history queries with firing/active and any other
// range query with the first matrix whose key the expression contains
// (the minimum traffic of 0.05 calls/s appears in every error-rate rule).
type fakeProm struct {
	firing, active model.Matrix
	exprs          []exprAnswer

	mu    sync.Mutex
	other int
}

type exprAnswer struct {
	contains string
	m        model.Matrix
}

func (f *fakeProm) Query(context.Context, string, time.Time) (model.Value, error) {
	return nil, errors.New("unused")
}

func (f *fakeProm) QueryRange(_ context.Context, q string, _, _ time.Time, _ time.Duration) (model.Matrix, error) {
	switch {
	case strings.HasPrefix(q, "max_over_time(ALERTS{"):
		return f.firing, nil
	case strings.HasPrefix(q, "max_over_time(ALERTS_FOR_STATE{"):
		return f.active, nil
	}
	f.mu.Lock()
	f.other++
	f.mu.Unlock()
	for _, a := range f.exprs {
		if strings.Contains(q, a.contains) {
			return a.m, nil
		}
	}
	return model.Matrix{}, nil
}

// frontend calls checkout, checkout calls payment; all critical.
const shopYAML = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
spec:
  services:
    - name: frontend
      type: http
      critical: true
      dependsOn: [checkout]
    - name: payment
      type: grpc
      critical: true
    - name: checkout
      type: grpc
      critical: true
      dependsOn: [payment]
      errorRate:
        threshold: 0.05
        for: 1m
      operations:
        - name: PlaceOrder
          latency:
            thresholdMs: 300
`

func shop(t *testing.T, yaml string) *v1alpha1.SystemDescription {
	t.Helper()
	res := description.Validate([]byte(yaml))
	if !res.Valid {
		t.Fatalf("invalid description: %+v", res.Errors)
	}
	return res.Document
}

func warningShop(t *testing.T) *v1alpha1.SystemDescription {
	return shop(t, strings.Replace(shopYAML, "      critical: true\n      dependsOn: [payment]", "      dependsOn: [payment]", 1))
}

// condition is what the error-rate expression returns: present at the given minutes.
func condition(minutes ...int) model.Matrix {
	return model.Matrix{firing(model.Metric{"service_name": "checkout"}, minutes...)}
}

// flapping: n short firings of checkout's error-rate rule (two samples each,
// every 30 minutes) and, when long, one firing of 21 minutes.
func flapping(n int, long bool) model.Matrix {
	var minutes []int
	for i := 0; i < n; i++ {
		minutes = append(minutes, 30*(i+1), 30*(i+1)+1)
	}
	if long {
		minutes = append(minutes, span(1300, 1320)...)
	}
	return model.Matrix{firing(alertLabels("KamertonErrorRateHigh", "checkout"), minutes...)}
}

// runs returns condition runs of length minutes at the places of flapping(n).
func runs(n, length int) []int {
	var out []int
	for i := 0; i < n; i++ {
		out = append(out, span(30*(i+1)+2-length, 30*(i+1)+1)...)
	}
	return out
}

var longRun = span(1295, 1320)

func analyze(t *testing.T, q *fakeProm, sd *v1alpha1.SystemDescription, fb map[Key]Counts) *Report {
	t.Helper()
	a := &Analyzer{Prom: q, Now: func() time.Time { return end.Add(30 * time.Second) }}
	rep, err := a.Analyze(context.Background(), sd, 24*time.Hour, "1d", fb)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func ruleOf(rep *Report, alert, service string) *RuleReport {
	for i := range rep.Rules {
		if rep.Rules[i].Alert == alert && rep.Rules[i].Service == service {
			return &rep.Rules[i]
		}
	}
	return nil
}

func errorRule(rep *Report) *RuleReport { return ruleOf(rep, "KamertonErrorRateHigh", "checkout") }

func TestSuggestPrefersASmallThreshold(t *testing.T) {
	q := &fakeProm{firing: flapping(30, true), exprs: []exprAnswer{
		// A pending period of 2m would qualify too, but adds a minute of delay.
		{"> 0.063\n", condition(longRun...)},
		{"> 0.05\n", condition(append(runs(30, 2), longRun...)...)},
	}}
	rep := analyze(t, q, shop(t, shopYAML), nil)
	r := errorRule(rep)
	if r == nil || r.Fired != 31 || r.Short != 30 || r.Consequences != 0 || r.LongestSeconds != 21*60 || !r.Noisy {
		t.Fatalf("rule = %+v", r)
	}
	s := r.Suggestion
	if s == nil || s.Field != "threshold" || s.Current != "0.05" || s.Suggested != "0.063" || s.DelaySeconds != 0 || s.WouldFire != 1 || s.WouldMiss != 0 || s.Path != "errorRate.threshold" || s.Format != "share" {
		t.Fatalf("suggestion = %+v", s)
	}
	raw, _ := json.Marshal(r)
	for _, field := range []string{`"consequences":0`, `"delaySeconds":0`, `"wouldFire":1`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("JSON lacks %s: %s", field, raw)
		}
	}
}

func TestSuggestForWithItsDelay(t *testing.T) {
	q := &fakeProm{firing: flapping(30, true), exprs: []exprAnswer{
		// Small threshold changes do not remove the short firings.
		{"> 0.05\n", condition(append(runs(30, 2), longRun...)...)},
	}}
	s := errorRule(analyze(t, q, shop(t, shopYAML), nil)).Suggestion
	if s == nil || s.Field != "for" || s.Current != "1m" || s.Suggested != "2m" || s.Unit != "" || s.DelaySeconds != 60 || s.Path != "errorRate.for" {
		t.Fatalf("suggestion = %+v", s)
	}
	if q.other != 3 {
		t.Errorf("the rule and two small thresholds need a query each, a longer for none; queries = %d", q.other)
	}
}

func TestCriticalRulesKeepAShortFor(t *testing.T) {
	// Runs of 3 minutes survive a pending period of 2m and 3m; only 5m removes them.
	q := func() *fakeProm {
		return &fakeProm{firing: flapping(30, true), exprs: []exprAnswer{{"> 0.05\n", condition(append(runs(30, 4), longRun...)...)}}}
	}
	if s := errorRule(analyze(t, q(), shop(t, shopYAML), nil)).Suggestion; s != nil {
		t.Errorf("a critical rule got for %s", s.Suggested)
	}
	s := errorRule(analyze(t, q(), warningShop(t), nil)).Suggestion
	if s == nil || s.Field != "for" || s.Suggested != "5m" || s.DelaySeconds != 240 {
		t.Fatalf("warning rule suggestion = %+v", s)
	}
}

func TestNoSuggestionThatMissesALongFiring(t *testing.T) {
	q := &fakeProm{firing: flapping(30, true), exprs: []exprAnswer{
		// The backtest never sees the long firing: every candidate would miss it.
		{"> 0.05\n", condition(runs(30, 2)...)},
	}}
	r := errorRule(analyze(t, q, shop(t, shopYAML), nil))
	if r.Suggestion != nil || !r.Noisy {
		t.Fatalf("rule %+v, suggestion %+v", r, r.Suggestion)
	}
	if q.other > 1+maxThresholdCandidates {
		t.Errorf("queries = %d, bound %d", q.other, 1+maxThresholdCandidates)
	}
}

func TestNoSuggestionThatSwitchesTheRuleOff(t *testing.T) {
	q := &fakeProm{firing: flapping(12, false), exprs: []exprAnswer{{"> 0.05\n", condition(runs(12, 2)...)}}}
	r := errorRule(analyze(t, q, shop(t, shopYAML), nil))
	if !r.Noisy || r.Suggestion != nil {
		t.Fatalf("12 firings: rule %+v, suggestion %+v", r, r.Suggestion)
	}
	q = &fakeProm{firing: flapping(MinFiredForSilence+5, false), exprs: []exprAnswer{{"> 0.05\n", condition(runs(MinFiredForSilence+5, 2)...)}}}
	s := errorRule(analyze(t, q, shop(t, shopYAML), nil)).Suggestion
	if s == nil || s.WouldFire != 0 || s.Suggested != "2m" {
		t.Fatalf("a rule that only flaps: %+v", s)
	}
}

func TestNoSuggestionWhenTheBacktestDoesNotSeeTheHistory(t *testing.T) {
	q := &fakeProm{firing: flapping(30, true)}
	r := errorRule(analyze(t, q, shop(t, shopYAML), nil))
	if r.Suggestion != nil || q.other != 1 {
		t.Fatalf("suggestion %+v from an empty backtest, queries %d", r.Suggestion, q.other)
	}
}

// outage: payment has no ready pods three times for 3 to 6 minutes; its
// callers fail meanwhile, and frontend once on its own.
func outage(paymentSeverity string) model.Matrix {
	pay := alertLabels("KamertonNoReadyPods", "payment")
	pay["severity"] = model.LabelValue(paymentSeverity)
	co := alertLabels("KamertonErrorRateHigh", "checkout")
	fe := alertLabels("KamertonOperationErrorRateHigh", "frontend")
	fe["operation"] = "GET /cart"
	return model.Matrix{
		firing(pay, append(append(span(100, 104), span(300, 305)...), span(500, 502)...)...),
		firing(co, append(append(span(101, 104), span(301, 306)...), span(501, 502)...)...),
		firing(fe, append(append(span(102, 105), span(302, 307)...), 900)...),
	}
}

func TestConsequencesOfADependencyOutage(t *testing.T) {
	rep := analyze(t, &fakeProm{firing: outage("critical")}, shop(t, shopYAML), nil)
	co := errorRule(rep)
	if co == nil || co.Fired != 0 || co.Short != 0 || co.Consequences != 3 || co.Noisy || co.Suggestion != nil {
		t.Errorf("checkout = %+v", co)
	}
	// Muted through the chain: checkout's alert, muted itself, still mutes frontend.
	fe := ruleOf(rep, "KamertonOperationErrorRateHigh", "frontend")
	if fe == nil || fe.Fired != 1 || fe.Consequences != 2 || fe.Operation != "" {
		t.Errorf("frontend = %+v", fe)
	}
	// A short real outage of a critical service is not noise.
	pay := ruleOf(rep, "KamertonNoReadyPods", "payment")
	if pay == nil || pay.Fired != 3 || pay.Short != 1 || pay.Consequences != 0 || pay.Noisy {
		t.Errorf("payment = %+v", pay)
	}

	if co := errorRule(analyze(t, &fakeProm{firing: outage("warning")}, shop(t, shopYAML), nil)); co.Fired != 3 || co.Consequences != 0 {
		t.Errorf("a warning does not mute a critical alert: %+v", co)
	}
	off := shop(t, shopYAML+"  notifications:\n    groupByCause: false\n")
	if co := errorRule(analyze(t, &fakeProm{firing: outage("critical")}, off, nil)); co.Fired != 3 || co.Consequences != 0 {
		t.Errorf("without grouping by cause every firing reached people: %+v", co)
	}
	cycle := shop(t, strings.Replace(shopYAML, "      critical: true\n    - name: checkout", "      critical: true\n      dependsOn: [checkout]\n    - name: checkout", 1))
	if co := errorRule(analyze(t, &fakeProm{firing: outage("critical")}, cycle, nil)); co.Fired != 3 || co.Consequences != 0 {
		t.Errorf("services in a cycle do not mute each other: %+v", co)
	}
}

// Payment goes down; checkout fails and stops calling email, which goes
// silent (its "no data" is starved, rules.LabelStarved).
func TestConsequencesOfStarvation(t *testing.T) {
	yaml := strings.Replace(shopYAML, "      dependsOn: [payment]\n", "      dependsOn: [payment, email]\n", 1) +
		"    - name: email\n      type: http\n"
	starved := alertLabels("KamertonServiceTelemetryMissing", "email")
	starved["kamerton_starved"] = "true"
	own := alertLabels("KamertonServiceTelemetryMissing", "email")
	firings := model.Matrix{
		firing(alertLabels("KamertonNoReadyPods", "payment"), span(100, 110)...),
		firing(alertLabels("KamertonErrorRateHigh", "checkout"), span(101, 110)...),
		firing(starved, span(106, 110)...),
		firing(own, span(300, 305)...),
		firing(alertLabels("KamertonErrorRateHigh", "email"), span(104, 110)...),
	}
	rep := analyze(t, &fakeProm{firing: firings}, shop(t, yaml), nil)
	if r := ruleOf(rep, "KamertonServiceTelemetryMissing", "email"); r == nil || r.Fired != 1 || r.Consequences != 1 {
		t.Errorf("email no data: the starved firing is a consequence, the other one reached people: %+v", r)
	}
	if r := ruleOf(rep, "KamertonErrorRateHigh", "email"); r == nil || r.Fired != 1 || r.Consequences != 0 {
		t.Errorf("email's own errors reach people: %+v", r)
	}
	off := shop(t, yaml+"  notifications:\n    groupByCause: false\n")
	if r := ruleOf(analyze(t, &fakeProm{firing: firings}, off, nil), "KamertonServiceTelemetryMissing", "email"); r == nil || r.Fired != 2 {
		t.Errorf("without grouping by cause nothing is muted: %+v", r)
	}
}

func TestConsequencesOfSystemNoData(t *testing.T) {
	sys := model.Metric{"alertname": "KamertonTelemetryMissing", "alertstate": "firing", "kamerton_system": "shop", "severity": "critical"}
	svc := alertLabels("KamertonServiceTelemetryMissing", "checkout")
	rep := analyze(t, &fakeProm{firing: model.Matrix{firing(sys, span(700, 710)...), firing(svc, append(span(702, 708), span(900, 903)...)...)}}, shop(t, shopYAML), nil)
	r := ruleOf(rep, "KamertonServiceTelemetryMissing", "checkout")
	if r == nil || r.Fired != 1 || r.Consequences != 1 {
		t.Errorf("per-service no data = %+v", r)
	}
	if r := ruleOf(rep, "KamertonTelemetryMissing", ""); r == nil || r.Fired != 1 {
		t.Errorf("system no data = %+v", r)
	}
}

func TestReportKeysAndFeedbackOnlyRules(t *testing.T) {
	sd := shop(t, shopYAML)
	agg := alertLabels("KamertonOperationErrorRateHigh", "checkout")
	agg["operation"] = "GetCart"
	own := alertLabels("KamertonOperationLatencyHigh", "checkout")
	own["operation"] = "PlaceOrder"
	q := &fakeProm{firing: model.Matrix{firing(agg, 5, 6), firing(own, 50)}}
	fb := map[Key]Counts{{Alert: "KamertonLatencyHigh", Service: "checkout"}: {Useful: 1, Noise: 3}}
	rep := analyze(t, q, sd, fb)
	if len(rep.Rules) != 3 || !rep.Available || rep.ShortUnder != "5m" || rep.Window != "1d" {
		t.Fatalf("report = %+v", rep)
	}
	if r := ruleOf(rep, "KamertonOperationErrorRateHigh", "checkout"); r.Operation != "" {
		t.Errorf("the aggregated per-operation rule has no operation: %+v", r)
	}
	if r := ruleOf(rep, "KamertonOperationLatencyHigh", "checkout"); r.Operation != "PlaceOrder" {
		t.Errorf("a rule of one operation keeps it: %+v", r)
	}
	r := ruleOf(rep, "KamertonLatencyHigh", "checkout")
	if r.Fired != 0 || r.LastFiredAt != nil || r.Feedback.Noise != 3 || !r.Noisy {
		t.Errorf("feedback-only rule = %+v", r)
	}
}

func TestNoisyDefinition(t *testing.T) {
	day, week := 24*time.Hour, 7*24*time.Hour
	cases := []struct {
		name   string
		st     Stats
		fb     Counts
		window time.Duration
		want   bool
	}{
		{"flapping all week", Stats{Fired: 40, Short: 37}, Counts{}, week, true},
		{"flapping all day", Stats{Fired: 12, Short: 10}, Counts{}, day, true},
		{"a bad day of short outages", Stats{Fired: 7, Short: 5}, Counts{}, day, false},
		{"six short in a day", Stats{Fired: 6, Short: 6}, Counts{}, day, false},
		{"a dozen in a week", Stats{Fired: 12, Short: 12}, Counts{}, week, false},
		{"many long firings", Stats{Fired: 30, Short: 20}, Counts{}, week, false},
		{"people say noise", Stats{Fired: 1}, Counts{Noise: 2}, day, true},
		{"one vote is not enough", Stats{Fired: 1}, Counts{Noise: 1}, day, false},
		{"people say useful", Stats{Fired: 40, Short: 37}, Counts{Useful: 3, Noise: 1}, week, false},
		{"tie", Stats{Fired: 40, Short: 37}, Counts{Useful: 2, Noise: 2}, week, true},
	}
	for _, c := range cases {
		if got := Noisy(c.st, c.fb, c.window); got != c.want {
			t.Errorf("%s: Noisy = %v", c.name, got)
		}
	}
}

func TestCandidateOrder(t *testing.T) {
	sd := shop(t, shopYAML)
	ts := generator.Tunables(sd, "checkout", "KamertonErrorRateHigh", "")
	var got []string
	for _, c := range Candidates(ts, "critical") {
		got = append(got, c.Value)
	}
	if strings.Join(got, " ") != "0.063 0.075 2m 3m 0.1 0.15" {
		t.Errorf("critical order %v", got)
	}
	got = nil
	for _, c := range Candidates(ts, "warning") {
		got = append(got, c.Value)
		if c.Value == "10m" && c.Delay != 9*time.Minute {
			t.Errorf("delay of 10m = %s", c.Delay)
		}
	}
	if strings.Join(got, " ") != "0.063 0.075 2m 3m 5m 10m 0.1 0.15" {
		t.Errorf("warning order %v", got)
	}
}

func TestCandidateValues(t *testing.T) {
	sd := shop(t, `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
spec:
  services:
    - name: checkout
      type: grpc
      saturation:
        memory: 512Mi
        maxRestartsPerHour: 3
        for: 5m
      customRules:
        - name: free-disk
          title: Little free disk
          condition: {metric: node_free_bytes, operator: lt, threshold: 100}
`)
	vals := func(alert string) map[string][]string {
		out := map[string][]string{}
		for _, c := range Candidates(generator.Tunables(sd, "checkout", alert, ""), "warning") {
			out[c.Tunable.Path] = append(out[c.Tunable.Path], c.Value)
		}
		return out
	}
	m := vals("KamertonMemoryHigh")
	if strings.Join(m["saturation.memory"], " ") != "640Mi 768Mi 1024Mi 1536Mi" || strings.Join(m["saturation.for"], " ") != "10m" {
		t.Errorf("memory candidates %v", m)
	}
	if r := vals("KamertonPodRestarting"); strings.Join(r["saturation.maxRestartsPerHour"], " ") != "4 5 6 9" || len(r) != 1 {
		t.Errorf("restarts candidates %v", r)
	}
	c := vals("KamertonCustomFreeDisk")
	if strings.Join(c["customRules[free-disk].condition.threshold"], " ") != "80 67 50 33" || strings.Join(c["customRules[free-disk].for"], " ") != "3m 5m 10m" {
		t.Errorf("custom candidates %v", c)
	}
	if got := vals("KamertonOOMKilled"); len(got) != 0 {
		t.Errorf("no candidates for a rule without a threshold: %v", got)
	}
	share := Candidates([]generator.Tunable{{Field: "threshold", Format: generator.FormatShare, Current: "0.9"}}, "warning")
	if len(share) != 1 || share[0].Value != "0.95" {
		t.Errorf("share near 1: %+v", share)
	}
}

func TestCandidatesApplyInDescriptionForm(t *testing.T) {
	sd := shop(t, shopYAML)
	for _, alert := range []string{"KamertonErrorRateHigh", "KamertonOperationLatencyHigh"} {
		op := ""
		if alert == "KamertonOperationLatencyHigh" {
			op = "PlaceOrder"
		}
		for _, c := range Candidates(generator.Tunables(sd, "checkout", alert, op), "warning") {
			cp := sd.DeepCopy()
			if err := c.Tunable.Apply(cp, "checkout", c.Value); err != nil {
				t.Fatalf("%s %s=%s: %v", alert, c.Tunable.Path, c.Value, err)
			}
			if _, err := generator.Generate(cp, generator.Options{}); err != nil {
				t.Fatalf("%s %s=%s: %v", alert, c.Tunable.Path, c.Value, err)
			}
		}
	}
	cp := sd.DeepCopy()
	ts := generator.Tunables(sd, "checkout", "KamertonOperationLatencyHigh", "PlaceOrder")
	if len(ts) != 2 || ts[0].Path != "operations[PlaceOrder].latency.thresholdMs" || ts[0].Current != "300" || ts[0].Unit != "ms" {
		t.Fatalf("tunables %+v", ts)
	}
	_ = ts[0].Apply(cp, "checkout", "450")
	if got := *cp.Spec.Services[2].Operations[0].Latency.ThresholdMs; got != 450 {
		t.Errorf("applied %v", got)
	}
	if *sd.Spec.Services[2].Operations[0].Latency.ThresholdMs != 300 {
		t.Error("the original description changed")
	}
}
