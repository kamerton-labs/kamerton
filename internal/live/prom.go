// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package live

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
)

// NowWindow is how far back "now" looks: the numbers on the map and in the headline.
const NowWindow = 5 * time.Minute

// num is a number that may be unknown: no data, or a signal that is off. JSON null then.
func num(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// scalar runs an instant query whose result is one number.
func scalar(ctx context.Context, prom prometheus.Querier, q string, at time.Time) *float64 {
	if q == "" {
		return nil
	}
	v, err := prom.Query(ctx, q, at)
	if err != nil {
		return nil
	}
	switch x := v.(type) {
	case model.Vector:
		if len(x) == 0 {
			return nil
		}
		return num(float64(x[0].Value))
	case *model.Scalar:
		return num(float64(x.Value))
	}
	return nil
}

// byLabel runs an instant query and reads one number per value of label.
func byLabel(ctx context.Context, prom prometheus.Querier, q, label string, at time.Time) map[string]*float64 {
	out := map[string]*float64{}
	if q == "" {
		return out
	}
	v, err := prom.Query(ctx, q, at)
	if err != nil {
		return out
	}
	if vec, ok := v.(model.Vector); ok {
		for _, s := range vec {
			out[string(s.Metric[model.LabelName(label)])] = num(float64(s.Value))
		}
	}
	return out
}

// Numbers are the request numbers of a service now.
type Numbers struct {
	Requests   *float64 `json:"requests"`
	ErrorRatio *float64 `json:"errorRatio"`
	P95        *float64 `json:"p95"`
}

// NumbersNow reads the numbers of every service at once, a few queries at a time.
func NumbersNow(ctx context.Context, prom prometheus.Querier, sd *v1alpha1.SystemDescription, at time.Time) map[string]Numbers {
	out := make(map[string]Numbers, len(sd.Spec.Services))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range sd.Spec.Services {
		svc := &sd.Spec.Services[i]
		q := For(sd, svc)
		if !q.requests {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			n := Numbers{
				Requests:   scalar(ctx, prom, q.Requests(NowWindow, false), at),
				ErrorRatio: scalar(ctx, prom, q.ErrorRatio(NowWindow, false), at),
				P95:        scalar(ctx, prom, q.Latency(0.95, NowWindow, false), at),
			}
			mu.Lock()
			out[svc.Name] = n
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// SLO is how a service stands against its objective over its window.
type SLO struct {
	Service string `json:"service"`
	// Objective in percent, as written in the description.
	Objective float64 `json:"objective"`
	Window    string  `json:"window"`
	// Availability is the share of requests that went through over the window, 0..1.
	Availability *float64 `json:"availability"`
	// BudgetLeft is the share of the error budget not spent yet, 1 untouched, 0 or less spent.
	BudgetLeft *float64 `json:"budgetLeft"`
}

// SLOs reads the services that have an objective.
func SLOs(ctx context.Context, prom prometheus.Querier, sd *v1alpha1.SystemDescription, at time.Time) []SLO {
	out := []SLO{}
	for i := range sd.Spec.Services {
		svc := &sd.Spec.Services[i]
		if svc.SLO == nil || svc.SLO.Objective <= 0 || svc.SLO.Objective >= 100 {
			continue
		}
		window := string(svc.SLO.Window)
		d, err := model.ParseDuration(window)
		if err != nil || d <= 0 {
			window, d = "30d", model.Duration(30*24*time.Hour)
		}
		s := SLO{Service: svc.Name, Objective: svc.SLO.Objective, Window: window}
		if share := scalar(ctx, prom, For(sd, svc).ErrorRatio(time.Duration(d), false), at); share != nil {
			s.Availability = num(1 - *share)
			s.BudgetLeft = num(1 - *share/(1-svc.SLO.Objective/100))
		}
		out = append(out, s)
	}
	return out
}

// Line is one series of a chart, its values at the chart's timestamps (null: no data).
type Line struct {
	Label  string     `json:"label"`
	Values []*float64 `json:"values"`
}

// Chart is one signal over the range.
type Chart struct {
	Lines []Line `json:"lines"`
	// Now is the value at the end of the range over the alert window; Usual is its median over
	// the day before the range.
	Now   *float64 `json:"now"`
	Usual *float64 `json:"usual"`
	// Limit is a line not to cross, such as the memory limit of the pods.
	Limit *float64 `json:"limit,omitempty"`
}

// Operation is one operation of a service over the range.
type Operation struct {
	Name       string   `json:"name"`
	Requests   *float64 `json:"requests"`
	ErrorRatio *float64 `json:"errorRatio"`
	P95        *float64 `json:"p95"`
}

// Series are the signals of one service over a range.
type Series struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Step       int       `json:"step"`
	Timestamps []int64   `json:"timestamps"`
	Requests   *Chart    `json:"requests,omitempty"`
	Errors     *Chart    `json:"errors,omitempty"`
	Latency    *Chart    `json:"latency,omitempty"`
	Memory     *Chart    `json:"memory,omitempty"`
	CPU        *Chart    `json:"cpu,omitempty"`
	Queue      *Chart    `json:"queue,omitempty"`
	// Restarts of the containers over the range.
	Restarts   *float64    `json:"restarts"`
	Operations []Operation `json:"operations"`
}

// StepFor keeps a chart at about 120 points, never finer than 15 seconds.
func StepFor(r time.Duration) time.Duration {
	step := (r / 120).Truncate(time.Second)
	if step < 15*time.Second {
		step = 15 * time.Second
	}
	return step
}

// rateWindow is long enough to always hold a few scrapes and short enough to follow the step.
func rateWindow(step time.Duration) time.Duration {
	return max(2*step, 2*time.Minute)
}

// usual is the median of a signal over the day before the range.
func usual(expr string, r time.Duration) string {
	if expr == "" {
		return ""
	}
	return "quantile_over_time(0.5, (" + expr + ")[1d:5m] offset " + model.Duration(r).String() + ")"
}

// ServiceSeries reads the signals of svc over [end-r, end].
func ServiceSeries(ctx context.Context, prom prometheus.Querier, sd *v1alpha1.SystemDescription, svc *v1alpha1.Service, end time.Time, r time.Duration) Series {
	step := StepFor(r)
	end = end.Truncate(step)
	start := end.Add(-r)
	s := Series{Start: start, End: end, Step: int(step / time.Second), Operations: []Operation{}}
	for t := start; !t.After(end); t = t.Add(step) {
		s.Timestamps = append(s.Timestamps, t.Unix())
	}
	q := For(sd, svc)
	w := rateWindow(step)
	index := make(map[int64]int, len(s.Timestamps))
	for i, ts := range s.Timestamps {
		index[ts] = i
	}
	lines := func(expr, label, fallback string) []Line {
		if expr == "" {
			return nil
		}
		m, err := prom.QueryRange(ctx, expr, start, end, step)
		if err != nil {
			return nil
		}
		out := make([]Line, 0, len(m))
		for _, ss := range m {
			l := Line{Label: string(ss.Metric[model.LabelName(label)]), Values: make([]*float64, len(s.Timestamps))}
			if l.Label == "" {
				l.Label = fallback
			}
			for _, p := range ss.Values {
				if i, ok := index[p.Timestamp.Unix()]; ok {
					l.Values[i] = num(float64(p.Value))
				}
			}
			out = append(out, l)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
		return out
	}

	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	if q.requests {
		run(func() {
			s.Requests = &Chart{Lines: lines(q.Requests(w, false), "", "requests"),
				Now: scalar(ctx, prom, q.Requests(NowWindow, false), end), Usual: scalar(ctx, prom, usual(q.Requests(NowWindow, false), r), end)}
		})
		run(func() {
			s.Errors = &Chart{Lines: lines(q.ErrorRatio(w, false), "", "errors"),
				Now: scalar(ctx, prom, q.ErrorRatio(NowWindow, false), end), Usual: scalar(ctx, prom, usual(q.ErrorRatio(NowWindow, false), r), end)}
		})
		run(func() {
			c := &Chart{Now: scalar(ctx, prom, q.Latency(0.95, NowWindow, false), end), Usual: scalar(ctx, prom, usual(q.Latency(0.95, NowWindow, false), r), end)}
			for _, p := range []struct {
				q     float64
				label string
			}{{0.5, "p50"}, {0.95, "p95"}, {0.99, "p99"}} {
				c.Lines = append(c.Lines, lines(q.Latency(p.q, w, false), "", p.label)...)
			}
			if len(c.Lines) > 0 || c.Now != nil {
				s.Latency = c
			}
		})
		run(func() {
			reqs := byLabel(ctx, prom, q.Requests(r, true), q.OperationLabel(), end)
			errs := byLabel(ctx, prom, q.ErrorRatio(r, true), q.OperationLabel(), end)
			p95 := byLabel(ctx, prom, q.Latency(0.95, r, true), q.OperationLabel(), end)
			for name, v := range reqs {
				s.Operations = append(s.Operations, Operation{Name: name, Requests: v, ErrorRatio: errs[name], P95: p95[name]})
			}
			sortOperations(s.Operations)
		})
	}
	if q.pods {
		run(func() {
			c := &Chart{Lines: lines(q.Memory(), q.PodLabel(), "memory")}
			if lim := q.MemoryLimit(); lim != "" {
				c.Limit = scalar(ctx, prom, "max("+lim+")", end)
			}
			if len(c.Lines) > 0 {
				s.Memory = c
			}
		})
		run(func() {
			if l := lines(q.CPU(w), q.PodLabel(), "cpu"); len(l) > 0 {
				s.CPU = &Chart{Lines: l}
			}
		})
		run(func() { s.Restarts = scalar(ctx, prom, q.Restarts(r), end) })
	}
	if q.queue {
		run(func() {
			if l := lines(q.QueueLag(), q.GroupLabel(), "lag"); len(l) > 0 {
				s.Queue = &Chart{Lines: l, Now: scalar(ctx, prom, "max("+q.QueueLag()+")", end)}
			}
		})
	}
	wg.Wait()
	return s
}

// sortOperations puts the failing first, then the busiest; the quiet tail is cut.
func sortOperations(ops []Operation) {
	val := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	sort.Slice(ops, func(i, j int) bool {
		ei, ej := val(ops[i].ErrorRatio) > 0, val(ops[j].ErrorRatio) > 0
		if ei != ej {
			return ei
		}
		if ei && val(ops[i].ErrorRatio) != val(ops[j].ErrorRatio) {
			return val(ops[i].ErrorRatio) > val(ops[j].ErrorRatio)
		}
		if val(ops[i].Requests) != val(ops[j].Requests) {
			return val(ops[i].Requests) > val(ops[j].Requests)
		}
		return ops[i].Name < ops[j].Name
	})
}
