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
