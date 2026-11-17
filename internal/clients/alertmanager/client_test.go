// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package alertmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
	if len(asked) != 2 || asked[0] != "true/false" || asked[1] != "false/false" {
		t.Errorf("queries = %v", asked)
	}
	var _ MutedReader = c
}
