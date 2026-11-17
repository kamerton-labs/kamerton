// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/internal/history"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// seedPastIncident closes an earlier "requests stopped" incident of db with a note.
func seedPastIncident(t *testing.T, s *Server) string {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	s.history.Now = func() time.Time { return now }
	start := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)
	inc := triage.Incident{ID: "shop:db", System: "shop", Roots: []string{"db"}, StartsAt: start,
		RootAlerts: []triage.Alert{{Name: "KamertonRequestsStopped", Service: "db", Severity: "critical", Title: "Requests stopped arriving", StartsAt: start}}}
	st, err := s.history.Observe(ctx, history.Observation{System: "shop", Incidents: []triage.Incident{inc}})
	if err != nil {
		t.Fatal(err)
	}
	id := st.OpenRecord("shop", "shop:db").ID
	if _, err := s.history.Observe(ctx, history.Observation{System: "shop", Ended: map[string]time.Time{history.EndKey("KamertonRequestsStopped", "db"): start.Add(312 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	cause, fix := "disk full on the db node", "freed the WAL volume"
	if _, err := s.history.SetNote(ctx, id, history.NoteInput{Cause: &cause, Fix: &fix}, history.SourceTelegram); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAlertsPreviousAndHistoryID(t *testing.T) {
	srv, s, _, _, _ := hookServer(t, false)
	past := seedPastIncident(t, s)
	cookie := sessionCookie(func() *http.Response { r, _ := login(t, srv, "admin", "pw"); return r }())
	_, body := call(t, "GET", srv.URL+"/api/v1/alerts", "", withCookie(cookie))
	var out struct {
		Incidents []struct {
			ID        string `json:"id"`
			HistoryID string `json:"historyId"`
			Previous  *struct {
				ID              string `json:"id"`
				StartedAt       string `json:"startedAt"`
				DurationSeconds int64  `json:"durationSeconds"`
				Cause           string `json:"cause"`
				Fix             string `json:"fix"`
				Count30d        int    `json:"count30d"`
			} `json:"previous"`
		} `json:"incidents"`
	}
	decode(t, body, &out)
	if len(out.Incidents) != 1 {
		t.Fatalf("incidents %s", body)
	}
	inc := out.Incidents[0]
	if !history.ValidID(inc.HistoryID) || inc.HistoryID == past {
		t.Errorf("historyId %q", inc.HistoryID)
	}
	p := inc.Previous
	if p == nil || p.ID != past || p.StartedAt != "2026-11-06T03:00:00Z" || p.DurationSeconds != 312 || p.Cause != "disk full on the db node" ||
		p.Fix != "freed the WAL volume" || p.Count30d != 1 {
		t.Errorf("previous %+v", p)
	}
	// Polling again keeps the same record.
	_, body2 := call(t, "GET", srv.URL+"/api/v1/systems/shop/alerts", "", withCookie(cookie))
	if !strings.Contains(body2, `"historyId":"`+inc.HistoryID+`"`) {
		t.Errorf("second poll %s", body2)
	}
}

func TestIncidentHistoryAPI(t *testing.T) {
	srv, s, _, _, _ := hookServer(t, false)
	past := seedPastIncident(t, s)
	cookie := sessionCookie(func() *http.Response { r, _ := login(t, srv, "admin", "pw"); return r }())
	call(t, "GET", srv.URL+"/api/v1/alerts", "", withCookie(cookie)) // records the live incident

	if resp, _ := call(t, "GET", srv.URL+"/api/v1/incidents/history", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without login: %d", resp.StatusCode)
	}
	type rec struct {
		ID         string   `json:"id"`
		System     string   `json:"system"`
		Roots      []string `json:"roots"`
		RootAlerts []struct {
			Name, Service, Title, Severity string
		} `json:"rootAlerts"`
		Affected        []string `json:"affected"`
		StartedAt       string   `json:"startedAt"`
		EndedAt         *string  `json:"endedAt"`
		DurationSeconds *int64   `json:"durationSeconds"`
		Open            bool     `json:"open"`
		Note            *struct {
			Cause, Fix, UpdatedAt, Source string
		} `json:"note"`
		FollowUpTo any `json:"followUpTo"`
		Asked      any `json:"asked"`
	}
	var out struct {
		Incidents []rec `json:"incidents"`
	}
	_, body := call(t, "GET", srv.URL+"/api/v1/incidents/history?system=shop", "", withCookie(cookie))
	decode(t, body, &out)
	if len(out.Incidents) != 2 {
		t.Fatalf("history %s", body)
	}
	open, closed := out.Incidents[0], out.Incidents[1]
	if !open.Open || open.EndedAt != nil || open.DurationSeconds != nil || open.Roots[0] != "db" || open.Affected[0] != "api" || open.StartedAt != "2026-11-16T09:18:00Z" {
		t.Errorf("open %+v", open)
	}
	if closed.ID != past || closed.Open || closed.EndedAt == nil || *closed.DurationSeconds != 312 || closed.Note == nil || closed.Note.Source != "telegram" ||
		len(closed.RootAlerts) != 1 || closed.RootAlerts[0].Title != "Requests stopped arriving" || closed.RootAlerts[0].Severity != "critical" {
		t.Errorf("closed %+v", closed)
	}
	if closed.FollowUpTo != nil || closed.Asked != nil || strings.Contains(body, "lastSeen") {
		t.Errorf("internal fields leak: %s", body)
	}
	for q, n := range map[string]int{"?service=api": 1, "?service=db&limit=1": 1, "?system=other": 0, "?service=nope": 0, "": 2} {
		_, body := call(t, "GET", srv.URL+"/api/v1/incidents/history"+q, "", withCookie(cookie))
		var o struct {
			Incidents []rec `json:"incidents"`
		}
		decode(t, body, &o)
		if len(o.Incidents) != n || o.Incidents == nil {
			t.Errorf("%s: %s", q, body)
		}
	}
	if resp, _ := call(t, "GET", srv.URL+"/api/v1/incidents/history?limit=x", "", withCookie(cookie)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad limit: %d", resp.StatusCode)
	}

	url := srv.URL + "/api/v1/incidents/history/" + open.ID + "/note"
	note := `{"cause": "  replica lag ", "fix": "failed over to the standby\n"}`
	if resp, _ := call(t, "PUT", url, note, withCookie(cookie)); resp.StatusCode != http.StatusForbidden {
		t.Errorf("without CSRF header: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "PUT", url, note, xrw); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without login: %d", resp.StatusCode)
	}
	resp, body := call(t, "PUT", url, note, withCookie(cookie), xrw)
	var updated rec
	decode(t, body, &updated)
	if resp.StatusCode != http.StatusOK || updated.ID != open.ID || updated.Note == nil || updated.Note.Cause != "replica lag" ||
		updated.Note.Fix != "failed over to the standby" || updated.Note.Source != "ui" || updated.Note.UpdatedAt == "" {
		t.Errorf("PUT: %d %s", resp.StatusCode, body)
	}
	for name, tc := range map[string]struct {
		url, body string
		code      int
	}{
		"too long fix":   {url, `{"fix": "` + strings.Repeat("x", history.MaxFix+1) + `"}`, http.StatusBadRequest},
		"too long cause": {url, `{"cause": "` + strings.Repeat("я", history.MaxCause+1) + `"}`, http.StatusBadRequest},
		"not json":       {url, `{`, http.StatusBadRequest},
		"unknown id":     {srv.URL + "/api/v1/incidents/history/0123456789abcdef/note", `{"fix": "x"}`, http.StatusNotFound},
		"malformed id":   {srv.URL + "/api/v1/incidents/history/shop:db/note", `{"fix": "x"}`, http.StatusNotFound},
	} {
		if resp, body := call(t, "PUT", tc.url, tc.body, withCookie(cookie), xrw); resp.StatusCode != tc.code {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
}

func TestRunbookExperience(t *testing.T) {
	srv, s, _, _, _ := hookServer(t, false)
	past := seedPastIncident(t, s)
	cookie := sessionCookie(func() *http.Response { r, _ := login(t, srv, "admin", "pw"); return r }())
	var out struct {
		Experience []struct {
			ID              string `json:"id"`
			StartedAt       string `json:"startedAt"`
			DurationSeconds int64  `json:"durationSeconds"`
			Cause           string `json:"cause"`
			Fix             string `json:"fix"`
		} `json:"experience"`
		Markdown string `json:"markdown"`
	}
	for _, path := range []string{"/api/v1/systems/shop/runbooks/db", "/api/v1/runbooks/db"} {
		_, body := call(t, "GET", srv.URL+path, "", withCookie(cookie))
		decode(t, body, &out)
		if len(out.Experience) != 1 || out.Experience[0].ID != past || out.Experience[0].Fix != "freed the WAL volume" ||
			out.Experience[0].Cause != "disk full on the db node" || out.Experience[0].DurationSeconds != 312 || out.Markdown == "" {
			t.Errorf("%s: %s", path, body)
		}
	}
	_, body := call(t, "GET", srv.URL+"/api/v1/systems/shop/runbooks/api", "", withCookie(cookie))
	if !strings.Contains(body, `"experience":[]`) {
		t.Errorf("no experience: %s", body)
	}
}
