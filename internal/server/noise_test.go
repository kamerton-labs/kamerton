// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/noise"
)

// alertHistory answers the ALERTS history query with one flapping rule.
type alertHistory struct{ fail bool }

func (alertHistory) Query(context.Context, string, time.Time) (model.Value, error) {
	return model.Vector{}, nil
}

func (a alertHistory) QueryRange(_ context.Context, q string, _, end time.Time, step time.Duration) (model.Matrix, error) {
	if a.fail {
		return nil, errors.New("connection refused")
	}
	if !strings.HasPrefix(q, "max_over_time(ALERTS{") {
		return model.Matrix{}, nil
	}
	s := &model.SampleStream{Metric: model.Metric{"alertname": "KamertonErrorRateHigh", "service": "api", "alertstate": "firing", "kamerton_system": "shop"}}
	for i := 0; i < 6; i++ {
		t := end.Add(-time.Duration(60-10*i) * time.Hour)
		s.Values = append(s.Values, model.SamplePair{Timestamp: model.TimeFromUnixNano(t.UnixNano()), Value: 1})
	}
	return model.Matrix{s}, nil
}

func noiseServer(t *testing.T, opts Options) string {
	t.Helper()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "shop.yaml"), []byte(shopYAML), 0o600)
	store, err := descstore.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts.Descriptions = store
	srv := httptest.NewServer(New(opts).Handler())
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1/systems/shop/noise"
}

func TestNoiseReport(t *testing.T) {
	url := noiseServer(t, Options{Prometheus: alertHistory{}})
	code, body := get(t, url+"?window=3d")
	var rep noise.Report
	decode(t, body, &rep)
	if code != http.StatusOK || !rep.Available || rep.Window != "3d" || rep.ShortUnder != "5m" || len(rep.Rules) != 1 {
		t.Fatalf("%d %s", code, body)
	}
	r := rep.Rules[0]
	if r.Alert != "KamertonErrorRateHigh" || r.Service != "api" || r.Fired != 6 || r.Short != 6 || !r.Noisy || r.LastFiredAt == nil {
		t.Errorf("rule %+v", r)
	}
	for _, field := range []string{`"operation":""`, `"longestSeconds":60`, `"feedback":{"useful":0,"noise":0}`} {
		if !strings.Contains(body, field) {
			t.Errorf("body lacks %s: %s", field, body)
		}
	}
	if code, _ := get(t, url+"?window=30d"); code != http.StatusBadRequest {
		t.Errorf("window 30d: %d", code)
	}
}

func TestNoiseUnavailable(t *testing.T) {
	for name, opts := range map[string]Options{"not configured": {}, "unreachable": {Prometheus: alertHistory{fail: true}}} {
		url := noiseServer(t, opts)
		if code, body := do(t, "POST", url+"/feedback", `{"alert":"KamertonLatencyHigh","service":"api","verdict":"noise"}`); code != http.StatusOK || !strings.Contains(body, `"noise":1`) {
			t.Fatalf("%s: feedback %d %s", name, code, body)
		}
		_, body := get(t, url)
		var rep noise.Report
		decode(t, body, &rep)
		if rep.Available || rep.Reason == "" || rep.Window != "7d" {
			t.Errorf("%s: %s", name, body)
		}
		if len(rep.Rules) != 1 || rep.Rules[0].Feedback.Noise != 1 || rep.Rules[0].Fired != 0 {
			t.Errorf("%s: rules with feedback are listed: %s", name, body)
		}
	}
}

func TestNoiseFeedback(t *testing.T) {
	url := noiseServer(t, Options{Prometheus: alertHistory{}}) + "/feedback"
	for _, bad := range []string{
		`{"alert":"KamertonErrorRateHigh","service":"api","verdict":"meh"}`,
		`{"alert":"rm -rf","service":"api","verdict":"noise"}`,
		`{"alert":"KamertonErrorRateHigh","service":"nope","verdict":"noise"}`,
		`{"alert":"KamertonErrorRateHigh","service":"api","verdict":"noise","operation":"` + strings.Repeat("x", 300) + `"}`,
		`not json`,
	} {
		if code, body := do(t, "POST", url, bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	for i := 0; i < 2; i++ {
		_, _ = do(t, "POST", url, `{"alert":"KamertonErrorRateHigh","service":"api","verdict":"useful"}`)
	}
	code, body := do(t, "POST", url, `{"alert":"KamertonOperationErrorRateHigh","service":"api","operation":"GET /cart","verdict":"noise"}`)
	if code != http.StatusOK || !strings.Contains(body, `"operation":""`) {
		t.Errorf("the aggregated per-operation rule has no operation: %d %s", code, body)
	}
	_, body = get(t, strings.TrimSuffix(url, "/feedback"))
	var rep noise.Report
	decode(t, body, &rep)
	for _, r := range rep.Rules {
		if r.Alert == "KamertonErrorRateHigh" && (r.Feedback != noise.Counts{Useful: 2} || r.Noisy) {
			t.Errorf("useful verdicts overrule the history: %+v", r)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"alert":"KamertonErrorRateHigh","service":"api","verdict":"noise"}`))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode == http.StatusOK {
		t.Errorf("a state-changing request needs the CSRF header: %v", resp.StatusCode)
	}
}
