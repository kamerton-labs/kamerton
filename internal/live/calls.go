// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package live

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// The calls between two services, as Tempo's metrics generator counts them from traces.
const (
	callsMetric       = "traces_service_graph_request_total"
	callsFailedMetric = "traces_service_graph_request_failed_total"
	callsServerBucket = "traces_service_graph_request_server_seconds_bucket"
	clientLabel       = "client"
	serverLabel       = "server"
)

// Edge is the calls from one service to another now; names are the telemetry's.
type Edge struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Requests   *float64 `json:"requests"`
	ErrorRatio *float64 `json:"errorRatio"`
}

// EdgesNow reads every pair of services that called each other over NowWindow. Empty when the
// metrics generator is off.
func EdgesNow(ctx context.Context, prom prometheus.Querier, at time.Time) []Edge {
	w := rules.FormatDuration(NowWindow)
	all, _ := prom.Query(ctx, fmt.Sprintf("sum by (%s, %s) (rate(%s[%s]))", clientLabel, serverLabel, callsMetric, w), at)
	failed, _ := prom.Query(ctx, fmt.Sprintf("sum by (%s, %s) (rate(%s[%s]))", clientLabel, serverLabel, callsFailedMetric, w), at)
	key := func(m model.Metric) string { return string(m[clientLabel]) + "\x00" + string(m[serverLabel]) }
	fails := map[string]float64{}
	if v, ok := failed.(model.Vector); ok {
		for _, s := range v {
			fails[key(s.Metric)] = float64(s.Value)
		}
	}
	out := []Edge{}
	if v, ok := all.(model.Vector); ok {
		for _, s := range v {
			e := Edge{From: string(s.Metric[clientLabel]), To: string(s.Metric[serverLabel]), Requests: num(float64(s.Value))}
			if s.Value > 0 {
				e.ErrorRatio = num(fails[key(s.Metric)] / float64(s.Value))
			}
			out = append(out, e)
		}
	}
	return out
}

// CallSeries are the calls from one service to another over a range.
type CallSeries struct {
	Timestamps []int64   `json:"timestamps"`
	Step       int       `json:"step"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Requests   *Chart    `json:"requests,omitempty"`
	Errors     *Chart    `json:"errors,omitempty"`
	Latency    *Chart    `json:"latency,omitempty"`
}

// Calls reads the calls from client to server (telemetry names) over [end-r, end].
func Calls(ctx context.Context, prom prometheus.Querier, client, server string, end time.Time, r time.Duration) CallSeries {
	step := StepFor(r)
	end = end.Truncate(step)
	start := end.Add(-r)
	s := CallSeries{Step: int(step / time.Second), Start: start, End: end}
	for t := start; !t.After(end); t = t.Add(step) {
		s.Timestamps = append(s.Timestamps, t.Unix())
	}
	index := make(map[int64]int, len(s.Timestamps))
	for i, ts := range s.Timestamps {
		index[ts] = i
	}
	sel := rules.Matchers{}.Eq(clientLabel, client).Eq(serverLabel, server)
	w := rules.FormatDuration(rateWindow(step))
	nw := rules.FormatDuration(NowWindow)
	line := func(expr, label string) []Line {
		m, err := prom.QueryRange(ctx, expr, start, end, step)
		if err != nil || len(m) == 0 {
			return nil
		}
		l := Line{Label: label, Values: make([]*float64, len(s.Timestamps))}
		for _, p := range m[0].Values {
			if i, ok := index[p.Timestamp.Unix()]; ok {
				l.Values[i] = num(float64(p.Value))
			}
		}
		return []Line{l}
	}
	reqs := func(win string) string { return fmt.Sprintf("sum(rate(%s[%s]))", sel.Selector(callsMetric), win) }
	errs := func(win string) string {
		return fmt.Sprintf("(sum(rate(%[1]s[%[3]s])) or 0 * sum(rate(%[2]s[%[3]s]))) / sum(rate(%[2]s[%[3]s]))", sel.Selector(callsFailedMetric), sel.Selector(callsMetric), win)
	}
	lat := func(q, win string) string {
		return fmt.Sprintf("1000 * histogram_quantile(%s, sum by (le) (rate(%s[%s])))", q, sel.Selector(callsServerBucket), win)
	}
	if l := line(reqs(w), "requests"); l != nil {
		s.Requests = &Chart{Lines: l, Now: scalar(ctx, prom, reqs(nw), end)}
	}
	if l := line(errs(w), "errors"); l != nil {
		s.Errors = &Chart{Lines: l, Now: scalar(ctx, prom, errs(nw), end)}
	}
	if l := line(lat("0.95", w), "p95"); l != nil {
		s.Latency = &Chart{Lines: l, Now: scalar(ctx, prom, lat("0.95", nw), end)}
	}
	return s
}

// FailedCallsQuery is the TraceQL of the calls from client to server that failed: a step of
// the server, right under a step of the client, that ended with an error.
func FailedCallsQuery(client, server string) string {
	return fmt.Sprintf("{resource.service.name=%s} > {resource.service.name=%s && status=error}", TraceQLString(client), TraceQLString(server))
}
