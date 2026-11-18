// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package alertmanager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActiveAlerts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query()["filter"]; len(got) != 1 || got[0] != `kamerton_managed="true"` {
			t.Errorf("filter = %v", got)
		}
		_, _ = w.Write([]byte(`[{"fingerprint":"abc","labels":{"alertname":"KamertonErrorRateHigh","service":"checkout"},"annotations":{},"startsAt":"2026-01-01T00:00:00Z","status":{"state":"active"}}]`))
	}))
	defer srv.Close()

	alerts, err := New(srv.URL).ActiveAlerts(context.Background(), map[string]string{"kamerton_managed": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Labels["service"] != "checkout" || alerts[0].Status.State != "active" {
		t.Fatalf("unexpected alerts: %+v", alerts)
	}
}

func TestAlertsWithInhibited(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("inhibited")+"/"+r.URL.Query().Get("silenced"))
		_, _ = w.Write([]byte(`[{"fingerprint":"a","labels":{"service":"payment"},"status":{"state":"active"}},
			{"fingerprint":"b","labels":{"service":"checkout"},"status":{"state":"suppressed","inhibitedBy":["a"],"silencedBy":[]}}]`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	alerts, err := c.AlertsWithInhibited(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 || alerts[0].Inhibited() || !alerts[1].Inhibited() {
		t.Fatalf("alerts = %+v", alerts)
	}
	if _, err := c.ActiveAlerts(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != "true/true" || asked[1] != "false/false" {
		t.Errorf("queries = %v", asked)
	}
	var _ MutedReader = c
}

func TestTailSilences(t *testing.T) {
	var posted silenceBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/alerts":
			_, _ = w.Write([]byte(`[{"fingerprint":"tail","labels":{"service":"checkout"},"status":{"state":"suppressed","silencedBy":["mine"]}},
				{"fingerprint":"theirs","labels":{"service":"cart"},"status":{"state":"suppressed","silencedBy":["other"]}},
				{"fingerprint":"both","labels":{"service":"ad"},"status":{"state":"suppressed","silencedBy":["mine","other"]}},
				{"fingerprint":"live","labels":{"service":"payment"},"status":{"state":"active"}}]`))
		case r.URL.Path == "/api/v2/silences" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"id":"mine","createdBy":"kamerton","status":{"state":"active"}},
				{"id":"other","createdBy":"alice","status":{"state":"active"}}]`))
		case r.URL.Path == "/api/v2/silences" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &posted)
			_, _ = w.Write([]byte(`{"silenceID":"new"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL)

	// Only alerts under Kamerton's own silences come back, marked as the tail.
	alerts, err := c.AlertsWithInhibited(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 || alerts[0].Fingerprint != "tail" || !alerts[0].Tail || !alerts[0].Muted() || alerts[1].Fingerprint != "live" || alerts[1].Muted() {
		t.Fatalf("alerts = %+v", alerts)
	}

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	id, err := c.SetSilence(context.Background(), Silence{ID: "old", Labels: map[string]string{"service": "checkout", "alertname": "X"}, EndsAt: now.Add(10 * time.Minute), Comment: "tail"}, now)
	if err != nil || id != "new" {
		t.Fatalf("SetSilence = %q, %v", id, err)
	}
	if posted.ID != "old" || posted.CreatedBy != TailSilenceAuthor || len(posted.Matchers) != 2 || posted.Matchers[0].Name != "alertname" ||
		!posted.Matchers[1].IsEqual || posted.Matchers[1].IsRegex || !posted.EndsAt.Equal(now.Add(10*time.Minute)) {
		t.Errorf("posted = %+v", posted)
	}
}
