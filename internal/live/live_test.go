// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package live

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

func system() *v1alpha1.SystemDescription {
	return &v1alpha1.SystemDescription{
		Spec: v1alpha1.SystemDescriptionSpec{Services: []v1alpha1.Service{
			{Name: "checkout", Type: v1alpha1.ServiceTypeGRPC, SLO: &v1alpha1.SLO{Objective: 99.5, Window: "7d"}},
			{Name: "orders", Type: v1alpha1.ServiceTypeQueue, Queue: &v1alpha1.QueueSpec{Topic: "orders", ConsumerGroups: []string{"accounting"}}},
		}},
	}
}

func TestQueriesAreTheOnesTheRulesUse(t *testing.T) {
	sd := system()
	q := For(sd, &sd.Spec.Services[0])
	for name, got := range map[string]string{
		"requests": q.Requests(5*time.Minute, false),
		"errors":   q.ErrorRatio(5*time.Minute, true),
		"latency":  q.Latency(0.95, 5*time.Minute, false),
		"memory":   q.Memory(),
	} {
		if got == "" || !strings.Contains(got, `"checkout`) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if e := q.ErrorRatio(5*time.Minute, true); !strings.Contains(e, "by (span_name)") || !strings.Contains(e, "STATUS_CODE_ERROR") || !strings.Contains(e, " or 0 * ") {
		t.Errorf("error ratio per operation: %s", e)
	}
	if l := q.Latency(0.5, time.Minute, false); !strings.HasPrefix(l, "histogram_quantile(0.5, sum by (le)") {
		t.Errorf("latency: %s", l)
	}
	if q.QueueLag() != "" {
		t.Error("checkout is not a queue")
	}
	oq := For(sd, &sd.Spec.Services[1])
	if lag := oq.QueueLag(); !strings.Contains(lag, `topic="orders"`) || !strings.Contains(lag, `"accounting"`) {
		t.Errorf("queue lag: %s", lag)
	}
}

// fakeProm answers every query with the same number, or nothing for queries it is told to fail.
type fakeProm struct {
	value float64
	asked []string
}

func (f *fakeProm) Query(_ context.Context, q string, ts time.Time) (model.Value, error) {
	f.asked = append(f.asked, q)
	return model.Vector{&model.Sample{Metric: model.Metric{"span_name": "PlaceOrder"}, Value: model.SampleValue(f.value), Timestamp: model.TimeFromUnixNano(ts.UnixNano())}}, nil
}

func (f *fakeProm) QueryRange(_ context.Context, q string, start, end time.Time, step time.Duration) (model.Matrix, error) {
	f.asked = append(f.asked, q)
	ss := &model.SampleStream{Metric: model.Metric{"pod": "checkout-1"}}
	for t := start; !t.After(end); t = t.Add(step) {
		ss.Values = append(ss.Values, model.SamplePair{Timestamp: model.TimeFromUnixNano(t.UnixNano()), Value: model.SampleValue(f.value)})
	}
	return model.Matrix{ss}, nil
}

func TestNumbersAndBudget(t *testing.T) {
	sd := system()
	p := &fakeProm{value: 0.002}
	n := NumbersNow(context.Background(), p, sd, time.Now())
	if c := n["checkout"]; c.Requests == nil || *c.ErrorRatio != 0.002 {
		t.Errorf("checkout now: %+v", c)
	}
	slos := SLOs(context.Background(), p, sd, time.Now())
	if len(slos) != 1 || slos[0].Window != "7d" || *slos[0].Availability != 0.998 {
		t.Fatalf("SLO: %+v", slos)
	}
	// 0.2% of 0.5% spent: 60% left.
	if got := *slos[0].BudgetLeft; got < 0.599 || got > 0.601 {
		t.Errorf("budget left: %v", got)
	}
}

func TestServiceSeriesAlignsToTheChart(t *testing.T) {
	sd := system()
	s := ServiceSeries(context.Background(), &fakeProm{value: 3}, sd, &sd.Spec.Services[0], time.Unix(1_800_000_000, 0), time.Hour)
	if s.Step != 30 || len(s.Timestamps) != 121 {
		t.Fatalf("step %d, points %d", s.Step, len(s.Timestamps))
	}
	if s.Requests == nil || len(s.Requests.Lines) != 1 || s.Requests.Lines[0].Values[0] == nil || *s.Requests.Now != 3 {
		t.Errorf("requests: %+v", s.Requests)
	}
	if s.Latency == nil || len(s.Latency.Lines) != 3 || s.Latency.Lines[1].Label != "p95" {
		t.Errorf("latency: %+v", s.Latency)
	}
	if s.Memory == nil || s.Memory.Lines[0].Label != "checkout-1" {
		t.Errorf("memory: %+v", s.Memory)
	}
	if len(s.Operations) != 1 || s.Operations[0].Name != "PlaceOrder" {
		t.Errorf("operations: %+v", s.Operations)
	}
}

func TestSearchTraceAndLogs(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.String())
		switch {
		case r.URL.Path == "/api/search":
			_, _ = w.Write([]byte(`{"traces":[{"traceID":"abc0000000000001","rootServiceName":"<root span not yet received>","startTimeUnixNano":"1000000000","durationMs":7200000,
				"spanSet":{"spans":[{"spanID":"00000000000000aa","name":"Charge","startTimeUnixNano":"1700000000000000000","durationNanos":"12500000"}]}}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v2/traces/"):
			_, _ = w.Write([]byte(`{"trace":{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"payment"}}]},
				"scopeSpans":[{"spans":[
					{"spanId":"AAAAAAAAAAE=","name":"Charge","kind":"SPAN_KIND_SERVER","startTimeUnixNano":"2000000000","endTimeUnixNano":"2050000000",
					 "status":{"code":"STATUS_CODE_ERROR","message":"invalid token"},"attributes":[{"key":"app.user","value":{"intValue":"7"}}],
					 "events":[{"name":"exception","timeUnixNano":"2040000000","attributes":[{"key":"exception.message","value":{"stringValue":"boom"}}]}]},
					{"spanId":"AAAAAAAAAAI=","parentSpanId":"AAAAAAAAAAE=","name":"db","kind":"SPAN_KIND_CLIENT","startTimeUnixNano":"2010000000","endTimeUnixNano":"2020000000","status":{}}
				]}]}]}}`))
		case r.URL.Path == "/loki/api/v1/query_range":
			_, _ = w.Write([]byte(`{"data":{"result":[{"stream":{"service_name":"payment","severity_text":"ERROR","span_id":"0000000000000001"},"values":[["2045000000","charge failed"]]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	hits, err := Search(ctx, srv.URL, `{status=error}`, time.Unix(0, 0), time.Unix(100, 0), 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search: %v %+v", err, hits)
	}
	h := hits[0]
	// The step's own time, not the start of a session hours long; no root when Tempo has none.
	if h.Span != "Charge" || h.SpanID != "00000000000000aa" || h.SpanMs != 12.5 || h.Start.Unix() != 1_700_000_000 || h.RootService != "" {
		t.Errorf("hit: %+v", h)
	}

	tr, err := GetTrace(ctx, srv.URL, "abc0000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if tr.ID != "0000000000000000abc0000000000001" || tr.DurationMs != 50 || len(tr.Spans) != 2 {
		t.Fatalf("trace: %+v", tr)
	}
	root, child := tr.Spans[0], tr.Spans[1]
	if root.ID != "0000000000000001" || !root.Error || root.Status != "invalid token" || root.Kind != "server" || root.Attrs[0].Value != "7" {
		t.Errorf("root: %+v", root)
	}
	if child.Parent != root.ID || child.StartMs != 10 || child.DurationMs != 10 {
		t.Errorf("child: %+v", child)
	}
	if len(root.Events) != 1 || root.Events[0].AtMs != 40 || root.Events[0].Attrs[0].Value != "boom" {
		t.Errorf("events: %+v", root.Events)
	}

	logs, err := TraceLogs(ctx, srv.URL, "", tr.ID, tr.Start, tr.Start.Add(time.Minute))
	if err != nil || len(logs) != 1 || logs[0].Level != "error" || logs[0].SpanID != "0000000000000001" || logs[0].Service != "payment" {
		t.Fatalf("logs: %v %+v", err, logs)
	}
	if q := asked[len(asked)-1]; !strings.Contains(q, "trace_id%3D%220000000000000000abc0000000000001%22") {
		t.Errorf("the logs are looked up by the full trace ID: %s", q)
	}

	if _, err := GetTrace(ctx, srv.URL, "not-an-id"); err != ErrNotFound {
		t.Errorf("a bad ID: %v", err)
	}
}
