// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"context"
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

const shopYAML = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
spec:
  services:
    - name: checkout
      type: grpc
      critical: true
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

// condition is what the error-rate expression returns: present at the given minutes.
func condition(minutes ...int) model.Matrix {
	return model.Matrix{firing(model.Metric{"service_name": "checkout"}, minutes...)}
}

// history: eight short firings of the error-rate rule and one long one.
func history() model.Matrix {
	var minutes []int
	for i := 0; i < 8; i++ {
		minutes = append(minutes, 60*(i+1), 60*(i+1)+1)
	}
	minutes = append(minutes, span(700, 720)...)
	return model.Matrix{firing(alertLabels("KamertonErrorRateHigh", "checkout"), minutes...)}
}

// shortRuns returns eight condition runs of n minutes each, at the minutes of history().
func shortRuns(n int) []int {
	var out []int
	for i := 0; i < 8; i++ {
		out = append(out, span(60*(i+1)-n+1, 60*(i+1))...)
	}
	return out
}

func analyze(t *testing.T, q *fakeProm, sd *v1alpha1.SystemDescription, fb map[Key]Counts) *Report {
	t.Helper()
	a := &Analyzer{Prom: q, Now: func() time.Time { return end.Add(30 * time.Second) }}
	rep, err := a.Analyze(context.Background(), sd, 24*time.Hour, "1d", fb)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func ruleOf(rep *Report, alert string) *RuleReport {
	for i := range rep.Rules {
		if rep.Rules[i].Alert == alert {
			return &rep.Rules[i]
		}
	}
	return nil
}

func TestSuggestPrefersTheSmallestChange(t *testing.T) {
	q := &fakeProm{firing: history(), exprs: []exprAnswer{
		// Both a pending period of 2m and the first higher threshold would qualify.
		{"> 0.063\n", condition(span(695, 720)...)},
		{"> 0.05\n", condition(append(shortRuns(2), span(695, 720)...)...)},
	}}
	rep := analyze(t, q, shop(t, shopYAML), nil)
	r := ruleOf(rep, "KamertonErrorRateHigh")
	if r == nil || r.Fired != 9 || r.Short != 8 || r.LongestSeconds != 21*60 || !r.Noisy || r.Service != "checkout" {
		t.Fatalf("rule = %+v", r)
	}
	s := r.Suggestion
	if s == nil {
		t.Fatal("no suggestion")
	}
	if s.Field != "for" || s.Current != "1m" || s.Suggested != "2m" || s.Unit != "" || s.WouldFire != 1 || s.WouldMiss != 0 || s.Path != "errorRate.for" {
		t.Errorf("suggestion = %+v", s)
	}
	if q.other != 1 {
		t.Errorf("a longer pending period needs no new query; queries = %d", q.other)
	}
}

func TestSuggestThresholdWhenForDoesNotHelp(t *testing.T) {
	q := &fakeProm{firing: history(), exprs: []exprAnswer{
		// Runs of 4 minutes survive a pending period of 2m and 3m.
		{"> 0.063\n", condition(span(695, 720)...)},
		{"> 0.05\n", condition(append(shortRuns(4), span(695, 720)...)...)},
	}}
	s := ruleOf(analyze(t, q, shop(t, shopYAML), nil), "KamertonErrorRateHigh").Suggestion
	if s == nil || s.Field != "threshold" || s.Current != "0.05" || s.Suggested != "0.063" || s.Format != "share" || s.Path != "errorRate.threshold" || s.WouldFire != 1 || s.WouldMiss != 0 {
		t.Fatalf("suggestion = %+v", s)
	}
}

func TestNoSuggestionThatMissesALongFiring(t *testing.T) {
	q := &fakeProm{firing: history(), exprs: []exprAnswer{
		// The backtest never sees the long firing: every candidate would miss it.
		{"> 0.05\n", condition(shortRuns(2)...)},
	}}
	r := ruleOf(analyze(t, q, shop(t, shopYAML), nil), "KamertonErrorRateHigh")
	if r.Suggestion != nil {
		t.Fatalf("suggested %+v although it misses the long firing", r.Suggestion)
	}
	if !r.Noisy {
		t.Error("still noisy")
	}
	if q.other > 1+maxThresholdCandidates {
		t.Errorf("queries = %d, bound %d", q.other, 1+maxThresholdCandidates)
	}
}

func TestNoSuggestionWhenNotNoisy(t *testing.T) {
	m := alertLabels("KamertonErrorRateHigh", "checkout")
	q := &fakeProm{firing: model.Matrix{firing(m, 10, 11, 300, 301)}}
	r := ruleOf(analyze(t, q, shop(t, shopYAML), nil), "KamertonErrorRateHigh")
	if r == nil || r.Noisy || r.Suggestion != nil || q.other != 0 {
		t.Fatalf("rule %+v, queries %d", r, q.other)
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
	if r := ruleOf(rep, "KamertonOperationErrorRateHigh"); r.Operation != "" {
		t.Errorf("the aggregated per-operation rule has no operation: %+v", r)
	}
	if r := ruleOf(rep, "KamertonOperationLatencyHigh"); r.Operation != "PlaceOrder" {
		t.Errorf("a rule of one operation keeps it: %+v", r)
	}
	r := ruleOf(rep, "KamertonLatencyHigh")
	if r.Fired != 0 || r.LastFiredAt != nil || r.Feedback.Noise != 3 || !r.Noisy {
		t.Errorf("feedback-only rule = %+v", r)
	}
}

func TestNoisyDefinition(t *testing.T) {
	cases := []struct {
		name string
		st   Stats
		fb   Counts
		want bool
	}{
		{"flapping", Stats{Fired: 40, Short: 37}, Counts{}, true},
		{"few firings", Stats{Fired: 4, Short: 4}, Counts{}, false},
		{"half short is not most", Stats{Fired: 10, Short: 5}, Counts{}, false},
		{"long firings", Stats{Fired: 10, Short: 2}, Counts{}, false},
		{"people say noise", Stats{Fired: 1}, Counts{Noise: 2}, true},
		{"one vote is not enough", Stats{Fired: 1}, Counts{Noise: 1}, false},
		{"people say useful", Stats{Fired: 40, Short: 37}, Counts{Useful: 3, Noise: 1}, false},
		{"tie", Stats{Fired: 40, Short: 37}, Counts{Useful: 2, Noise: 2}, true},
	}
	for _, c := range cases {
		if got := Noisy(c.st, c.fb); got != c.want {
			t.Errorf("%s: Noisy = %v", c.name, got)
		}
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
		for _, c := range Candidates(generator.Tunables(sd, "checkout", alert, "")) {
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
	share := Candidates([]generator.Tunable{{Field: "threshold", Format: generator.FormatShare, Current: "0.9"}})
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
		for _, c := range Candidates(generator.Tunables(sd, "checkout", alert, op)) {
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
	if got := *cp.Spec.Services[0].Operations[0].Latency.ThresholdMs; got != 450 {
		t.Errorf("applied %v", got)
	}
	if *sd.Spec.Services[0].Operations[0].Latency.ThresholdMs != 300 {
		t.Error("the original description changed")
	}
}

func TestNoSuggestionWhenTheBacktestDoesNotSeeTheHistory(t *testing.T) {
	q := &fakeProm{firing: history()}
	r := ruleOf(analyze(t, q, shop(t, shopYAML), nil), "KamertonErrorRateHigh")
	if r.Suggestion != nil || q.other != 1 {
		t.Fatalf("suggestion %+v from an empty backtest, queries %d", r.Suggestion, q.other)
	}
}
