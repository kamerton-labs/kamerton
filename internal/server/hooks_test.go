// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/followup"
)

type staticToken struct {
	mu  sync.Mutex
	tok string
}

func (s *staticToken) Get(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok, nil
}

func (s *staticToken) set(t string) {
	s.mu.Lock()
	s.tok = t
	s.mu.Unlock()
}

type recordingTelegram struct {
	mu   sync.Mutex
	sent []string
}

func (r *recordingTelegram) SendFollowUp(_ context.Context, _ v1alpha1.SecretKeyRef, _ int64, html string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, html)
	return nil
}

const hookShopYAML = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
spec:
  services:
    - name: api
      type: http
      critical: true
      dependsOn: [db]
    - name: db
      type: database
  notifications:
    receivers:
      - name: oncall
        telegram:
          chatID: -100123
          botTokenSecret: {name: kamerton-telegram, key: bot-token}
    routing:
      critical: [oncall]
`

// mutedAM lists the root alert and a caller alert muted by an inhibit rule.
type mutedAM struct{}

func (mutedAM) alerts() []alertmanager.Alert {
	root := alertmanager.Alert{Fingerprint: "f1",
		Labels:      map[string]string{"alertname": "KamertonRequestsStopped", "service": "db", "severity": "critical", "kamerton_system": "shop", "kamerton_managed": "true"},
		Annotations: map[string]string{"kamerton_title": "Requests stopped arriving"}, StartsAt: time.Date(2026, 11, 16, 9, 18, 0, 0, time.UTC)}
	root.Status.State = "active"
	muted := alertmanager.Alert{Fingerprint: "f2",
		Labels:      map[string]string{"alertname": "KamertonErrorRateHigh", "service": "api", "severity": "critical", "kamerton_system": "shop", "kamerton_managed": "true"},
		Annotations: map[string]string{"kamerton_title": "Too many failed requests"}, StartsAt: time.Date(2026, 11, 16, 9, 19, 0, 0, time.UTC)}
	muted.Status.State = "suppressed"
	muted.Status.InhibitedBy = []string{"f1"}
	return []alertmanager.Alert{root, muted}
}

func (m mutedAM) ActiveAlerts(context.Context, map[string]string) ([]alertmanager.Alert, error) {
	return m.alerts()[:1], nil
}

func (m mutedAM) AlertsWithInhibited(context.Context, map[string]string) ([]alertmanager.Alert, error) {
	return m.alerts(), nil
}

func hookServer(t *testing.T, withFollowUp bool) (*httptest.Server, *Server, *staticToken, *recordingTelegram, *followup.Service) {
	t.Helper()
	tok := &staticToken{tok: "s3cret-token"}
	tg := &recordingTelegram{}
	opts := Options{
		UI: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}}, UIBuilt: true,
		Auth:         NewAuth("admin", "pw", "k"),
		Alertmanager: mutedAM{},
	}
	opts.Sets = nil
	var fu *followup.Service
	if withFollowUp {
		fu = &followup.Service{Telegram: tg, Sleep: func(context.Context, time.Duration) {}}
		opts.FollowUp, opts.WebhookToken = fu, tok
	} else {
		opts.FollowUpUnavailable = "Follow-up messages work only in a cluster."
	}
	s := New(opts)
	res := description.Validate([]byte(hookShopYAML))
	if !res.Valid {
		t.Fatalf("invalid: %v", res.Errors)
	}
	s.SetDescription(res.Document, []byte(hookShopYAML), "test")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, tok, tg, fu
}

const hookBody = `{"version":"4","status":"firing","receiver":"kamerton-webhook","groupLabels":{"alertname":"KamertonRequestsStopped","service":"db"},
"commonLabels":{"kamerton_system":"shop","service":"db"},
"alerts":[{"status":"firing","fingerprint":"f1","startsAt":"2026-11-16T09:18:00Z",
"labels":{"alertname":"KamertonRequestsStopped","service":"db","severity":"critical","kamerton_system":"shop","kamerton_managed":"true"},
"annotations":{"kamerton_title":"Requests stopped arriving"}}]}`

func TestAlertmanagerHookAuth(t *testing.T) {
	srv, _, _, tg, fu := hookServer(t, true)
	url := srv.URL + AlertmanagerHookPath
	for name, opts := range map[string][]reqOpt{
		"no token":     nil,
		"wrong token":  {withHeader("Authorization", "Bearer nope")},
		"basic scheme": {withHeader("Authorization", "Basic s3cret-token")},
		"empty bearer": {withHeader("Authorization", "Bearer ")},
	} {
		if resp, body := call(t, "POST", url, hookBody, opts...); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	resp, body := call(t, "POST", url, hookBody, withHeader("Authorization", "Bearer s3cret-token"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("authorized call: %d %s", resp.StatusCode, body)
	}
	fu.Wait()
	if len(tg.sent) != 1 || !strings.Contains(tg.sent[0], "Also affected: api.") {
		t.Errorf("follow-up = %v", tg.sent)
	}
	if resp, _ := call(t, "POST", srv.URL+"/api/v1/systems", "x", withHeader("Authorization", "Bearer s3cret-token")); resp.StatusCode != http.StatusForbidden {
		t.Errorf("the webhook token opens other endpoints: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "GET", url, ""); resp.StatusCode == http.StatusOK {
		t.Error("GET on the webhook succeeds")
	}
	for name, b := range map[string]string{"not json": "{", "unknown status": `{"status":"maybe"}`} {
		if resp, body := call(t, "POST", url, b, withHeader("Authorization", "Bearer s3cret-token")); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
}

func TestAlertmanagerHookRotatedToken(t *testing.T) {
	srv, s, tok, _, fu := hookServer(t, true)
	url := srv.URL + AlertmanagerHookPath
	resolved := strings.Replace(hookBody, `"status":"firing","receiver"`, `"status":"resolved","receiver"`, 1)
	if resp, _ := call(t, "POST", url, resolved, withHeader("Authorization", "Bearer s3cret-token")); resp.StatusCode != http.StatusOK {
		t.Fatalf("first call: %d", resp.StatusCode)
	}
	tok.set("rotated")
	s.hookMu.Lock()
	s.hookAt = time.Now().Add(-time.Minute) // the cached token is older than the re-read limit
	s.hookMu.Unlock()
	if resp, _ := call(t, "POST", url, resolved, withHeader("Authorization", "Bearer rotated")); resp.StatusCode != http.StatusOK {
		t.Errorf("rotated token: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "POST", url, resolved, withHeader("Authorization", "Bearer s3cret-token")); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old token still accepted: %d", resp.StatusCode)
	}
	fu.Wait()
}

func TestAlertmanagerHookUnavailable(t *testing.T) {
	srv, _, _, _, _ := hookServer(t, false)
	resp, body := call(t, "POST", srv.URL+AlertmanagerHookPath, hookBody, withHeader("Authorization", "Bearer s3cret-token"))
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "not_available") {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
	cookie := sessionCookie(func() *http.Response { r, _ := login(t, srv, "admin", "pw"); return r }())
	_, body = call(t, "GET", srv.URL+"/api/v1/settings", "", withCookie(cookie))
	var st struct {
		FollowUp struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
		} `json:"followUp"`
	}
	decode(t, body, &st)
	if st.FollowUp.Available || !strings.Contains(st.FollowUp.Reason, "cluster") {
		t.Errorf("settings followUp = %+v", st.FollowUp)
	}
}

func TestAlertsIncidents(t *testing.T) {
	srv, _, _, _, _ := hookServer(t, false)
	cookie := sessionCookie(func() *http.Response { r, _ := login(t, srv, "admin", "pw"); return r }())
	for _, path := range []string{"/api/v1/alerts", "/api/v1/systems/shop/alerts"} {
		resp, body := call(t, "GET", srv.URL+path, "", withCookie(cookie))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
		var out struct {
			Configured bool `json:"configured"`
			Alerts     []struct {
				Service  string `json:"service"`
				Muted    bool   `json:"muted"`
				Incident string `json:"incident"`
				Title    string `json:"title"`
			} `json:"alerts"`
			Muted []struct {
				Service  string `json:"service"`
				Muted    bool   `json:"muted"`
				Incident string `json:"incident"`
			} `json:"muted"`
			Incidents []struct {
				ID         string   `json:"id"`
				System     string   `json:"system"`
				Roots      []string `json:"roots"`
				Affected   []string `json:"affected"`
				StartsAt   string   `json:"startsAt"`
				RootAlerts []struct {
					Name  string `json:"name"`
					Title string `json:"title"`
					Muted bool   `json:"muted"`
				} `json:"rootAlerts"`
				AffectedAlerts []struct {
					Name  string `json:"name"`
					Muted bool   `json:"muted"`
				} `json:"affectedAlerts"`
			} `json:"incidents"`
		}
		decode(t, body, &out)
		if len(out.Alerts) != 1 || out.Alerts[0].Service != "db" || out.Alerts[0].Muted || out.Alerts[0].Incident != "shop:db" || out.Alerts[0].Title != "Requests stopped arriving" {
			t.Errorf("%s alerts = %+v", path, out.Alerts)
		}
		if len(out.Muted) != 1 || out.Muted[0].Service != "api" || !out.Muted[0].Muted || out.Muted[0].Incident != "shop:db" {
			t.Errorf("%s muted = %+v", path, out.Muted)
		}
		if len(out.Incidents) != 1 {
			t.Fatalf("%s incidents = %s", path, body)
		}
		inc := out.Incidents[0]
		if inc.ID != "shop:db" || inc.System != "shop" || strings.Join(inc.Roots, ",") != "db" || strings.Join(inc.Affected, ",") != "api" ||
			inc.StartsAt != "2026-11-16T09:18:00Z" || len(inc.RootAlerts) != 1 || inc.RootAlerts[0].Muted || len(inc.AffectedAlerts) != 1 || !inc.AffectedAlerts[0].Muted {
			t.Errorf("%s incident = %+v", path, inc)
		}
	}
}

func TestAlertsIncidentEvidence(t *testing.T) {
	srv, _, _, _, fu := hookServer(t, true)
	if resp, _ := call(t, "POST", srv.URL+AlertmanagerHookPath, hookBody, withHeader("Authorization", "Bearer s3cret-token")); resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook: %d", resp.StatusCode)
	}
	fu.Wait()
	cookie := sessionCookie(func() *http.Response { r, _ := login(t, srv, "admin", "pw"); return r }())
	_, body := call(t, "GET", srv.URL+"/api/v1/systems/shop/alerts", "", withCookie(cookie))
	var out struct {
		Incidents []struct {
			ID       string          `json:"id"`
			Roots    []string        `json:"roots"`
			Evidence json.RawMessage `json:"evidence"`
		} `json:"incidents"`
	}
	decode(t, body, &out)
	if len(out.Incidents) != 1 || out.Incidents[0].ID != "shop:db" {
		t.Fatalf("incidents: %s", body)
	}
	var ev map[string]any
	if err := json.Unmarshal(out.Incidents[0].Evidence, &ev); err != nil {
		t.Fatalf("evidence %s: %v", out.Incidents[0].Evidence, err)
	}
	collected, _ := ev["collectedAt"].(string)
	if _, err := time.Parse(time.RFC3339, collected); err != nil || ev["cause"] != "db: Requests stopped arriving since 09:18 UTC" || ev["likely"] != false {
		t.Errorf("evidence = %v", ev)
	}
	for k := range ev {
		switch k {
		case "collectedAt", "cause", "likely", "changes", "operation", "operationFailures", "logLine", "tracesURL", "explains":
		default:
			t.Errorf("unexpected evidence field %q", k)
		}
	}

	srv, _, _, _, _ = hookServer(t, false)
	cookie = sessionCookie(func() *http.Response { r, _ := login(t, srv, "admin", "pw"); return r }())
	if _, body := call(t, "GET", srv.URL+"/api/v1/alerts", "", withCookie(cookie)); strings.Contains(body, `"evidence"`) {
		t.Errorf("evidence without follow-up: %s", body)
	}
}
