// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"
)

var (
	start = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	end   = start.Add(24 * time.Hour)
)

func at(minute int) model.Time {
	return model.TimeFromUnixNano(start.Add(time.Duration(minute) * time.Minute).UnixNano())
}

func alertLabels(alert, service string) model.Metric {
	return model.Metric{"alertname": model.LabelValue(alert), "alertstate": "firing", "service": model.LabelValue(service),
		"service_name": model.LabelValue(service), "kamerton_system": "shop", "kamerton_managed": "true", "severity": "critical"}
}

// firing returns a sampled firing series present at the given minutes.
func firing(m model.Metric, minutes ...int) *model.SampleStream {
	s := &model.SampleStream{Metric: m}
	for _, x := range minutes {
		s.Values = append(s.Values, model.SamplePair{Timestamp: at(x), Value: 1})
	}
	return s
}

// activeAt returns the ALERTS_FOR_STATE series: value = activation time for each minute.
func activeAt(m model.Metric, values map[int]float64) *model.SampleStream {
	lbls := model.Metric{}
	for k, v := range m {
		if k != "alertstate" {
			lbls[k] = v
		}
	}
	s := &model.SampleStream{Metric: lbls}
	for x := 0; x <= 24*60; x++ {
		if v, ok := values[x]; ok {
			s.Values = append(s.Values, model.SamplePair{Timestamp: at(x), Value: model.SampleValue(v)})
		}
	}
	return s
}

func span(from, to int) []int {
	var out []int
	for m := from; m <= to; m++ {
		out = append(out, m)
	}
	return out
}

func TestEpisodesFlappingAndLong(t *testing.T) {
	m := alertLabels("KamertonErrorRateHigh", "checkout")
	minutes := append(append(append([]int{10, 11}, 20), span(30, 32)...), span(100, 130)...)
	eps := Episodes(model.Matrix{firing(m, minutes...)}, nil, start, end, Step)
	if len(eps) != 4 {
		t.Fatalf("episodes = %+v", eps)
	}
	want := []time.Duration{2 * time.Minute, time.Minute, 3 * time.Minute, 31 * time.Minute}
	for i, e := range eps {
		if e.Duration() != want[i] {
			t.Errorf("episode %d lasted %s, want %s", i, e.Duration(), want[i])
		}
		if e.Labels["alertstate"] != "" || e.Labels["alertname"] != "KamertonErrorRateHigh" {
			t.Errorf("labels %v", e.Labels)
		}
	}
	st := Summarize(eps)
	if st.Fired != 4 || st.Short != 3 || st.Longest != 31*time.Minute || !st.LastFiredAt.Equal(start.Add(99*time.Minute)) {
		t.Errorf("stats = %+v", st)
	}
	if !eps[3].Long() || eps[2].Long() {
		t.Error("long firing")
	}
}

func TestEpisodesAcrossWindowEdges(t *testing.T) {
	m := alertLabels("KamertonLatencyHigh", "checkout")
	last := 24 * 60
	eps := Episodes(model.Matrix{firing(m, 0, 1, 2), firing(alertLabels("KamertonLatencyHigh", "payment"), last-1, last)}, nil, start, end, Step)
	if len(eps) != 2 {
		t.Fatalf("episodes = %+v", eps)
	}
	first, second := eps[0], eps[1]
	if !first.Clipped || first.Short() || !first.Start.Equal(start) {
		t.Errorf("a firing that began before the window is not short: %+v", first)
	}
	if !second.Ongoing || second.Short() {
		t.Errorf("a firing still on at the end is not short: %+v", second)
	}
	if st := Summarize(eps); st.Fired != 2 || st.Short != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestEpisodesGaps(t *testing.T) {
	m := alertLabels("KamertonErrorRateHigh", "checkout")
	minutes := append(span(10, 20), span(41, 50)...)
	same := map[int]float64{}
	for _, x := range minutes {
		same[x] = 1000
	}
	eps := Episodes(model.Matrix{firing(m, minutes...)}, model.Matrix{activeAt(m, same)}, start, end, Step)
	if len(eps) != 1 || eps[0].Duration() != 41*time.Minute {
		t.Fatalf("a gap in the data with the same activation is one firing: %+v", eps)
	}

	changed := map[int]float64{}
	for _, x := range minutes {
		changed[x] = 1000
		if x > 40 {
			changed[x] = 2000
		}
	}
	if eps := Episodes(model.Matrix{firing(m, minutes...)}, model.Matrix{activeAt(m, changed)}, start, end, Step); len(eps) != 2 {
		t.Errorf("a new activation is a new firing: %+v", eps)
	}
	if eps := Episodes(model.Matrix{firing(m, minutes...)}, nil, start, end, Step); len(eps) != 2 {
		t.Errorf("without ALERTS_FOR_STATE a gap ends the firing: %+v", eps)
	}

	back := map[int]float64{}
	for _, x := range span(10, 20) {
		back[x] = 1000
		if x >= 15 {
			back[x] = 3000
		}
	}
	if eps := Episodes(model.Matrix{firing(m, span(10, 20)...)}, model.Matrix{activeAt(m, back)}, start, end, Step); len(eps) != 2 {
		t.Errorf("resolved and fired again within a step: %+v", eps)
	}
}

type countingQuerier struct {
	fakeProm
	calls []string
}

func (c *countingQuerier) QueryRange(ctx context.Context, q string, s, e time.Time, step time.Duration) (model.Matrix, error) {
	c.calls = append(c.calls, q)
	return c.fakeProm.QueryRange(ctx, q, s, e, step)
}

func TestHistoryQueriesAreBounded(t *testing.T) {
	var series model.Matrix
	for _, svc := range []string{"a", "b", "c", "d", "e", "f"} {
		series = append(series, firing(alertLabels("KamertonErrorRateHigh", svc), 5, 6))
	}
	q := &countingQuerier{fakeProm: fakeProm{firing: series}}
	eps, err := History(context.Background(), q, "shop", start, end, Step)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 6 || len(q.calls) != 2 {
		t.Fatalf("episodes %d, queries %v", len(eps), q.calls)
	}
	f, a := HistoryQueries("shop", Step)
	if !strings.HasPrefix(f, "max_over_time(ALERTS{") || !strings.Contains(f, `alertstate="firing"`) || !strings.Contains(f, `kamerton_system="shop"`) || !strings.HasSuffix(f, "[1m])") {
		t.Errorf("firing query %s", f)
	}
	if !strings.HasPrefix(a, "max_over_time(ALERTS_FOR_STATE{") || strings.Contains(a, "alertstate") {
		t.Errorf("activation query %s", a)
	}
}
