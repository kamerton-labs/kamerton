// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/evidence"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

// ---- Alertmanager webhook (follow-up messages) ----
//
// Alertmanager posts every group of a system to this endpoint as well as to
// Telegram and email (see generator/webhook.go). It is called by
// Alertmanager, not by a browser: it needs no session and no CSRF header,
// and is authenticated by a bearer token that Kamerton generates into a
// Secret in its namespace; the AlertmanagerConfig references that Secret.

// AlertmanagerHookPath is the path of the webhook endpoint.
const AlertmanagerHookPath = "/api/v1/hooks/alertmanager"

// TokenSource returns the current webhook token ("" when none).
type TokenSource interface {
	Get(ctx context.Context) (string, error)
}

// hookToken returns the webhook token, re-read from the Secret at most every
// refresh (a rotated Secret is picked up without a restart).
func (s *Server) hookToken(ctx context.Context, refresh time.Duration) string {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	if s.hookTok != "" && time.Since(s.hookAt) < refresh {
		return s.hookTok
	}
	t, err := s.opts.WebhookToken.Get(ctx)
	if err != nil {
		s.log.Warn("webhook token unavailable", "error", err)
		return s.hookTok
	}
	s.hookTok, s.hookAt = t, time.Now()
	return t
}

func sameToken(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// hookAuthorized checks the bearer token. A mismatch re-reads the Secret
// once (at most every 10 seconds) in case it was rotated.
func (s *Server) hookAuthorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	scheme, got, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(got) == "" {
		return false
	}
	got = strings.TrimSpace(got)
	want := s.hookToken(r.Context(), 5*time.Minute)
	if want != "" && sameToken(got, want) {
		return true
	}
	want = s.hookToken(r.Context(), 10*time.Second)
	return want != "" && sameToken(got, want)
}

func (s *Server) followUpUnavailable() string {
	if s.opts.FollowUp != nil && s.opts.WebhookToken != nil {
		return ""
	}
	if s.opts.FollowUpUnavailable != "" {
		return s.opts.FollowUpUnavailable
	}
	return "Follow-up messages are not available in this installation."
}

func (s *Server) alertmanagerHook(w http.ResponseWriter, r *http.Request) {
	if reason := s.followUpUnavailable(); reason != "" {
		writeError(w, http.StatusNotFound, "not_available", reason)
		return
	}
	if !s.hookAuthorized(r) {
		s.log.Warn("webhook call with a wrong or missing token", "remote", clientAddr(r))
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var hook alertmanager.Webhook
	if err := json.Unmarshal(body, &hook); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "expected an Alertmanager webhook notification")
		return
	}
	if hook.Status != "firing" && hook.Status != "resolved" {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown notification status")
		return
	}
	s.opts.FollowUp.Handle(&hook)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// systemAlertsWithMuted reads the active alerts of a system, muted ones
// included, from the Alertmanager of its set (follow-up messages).
func (s *Server) systemAlertsWithMuted(ctx context.Context, sd *v1alpha1.SystemDescription) ([]alertmanager.Alert, error) {
	c := s.clientsFor(ctx, sd.Spec.Stack)
	if c.AM == nil {
		return nil, nil
	}
	filter := map[string]string{rules.LabelManaged: "true", generator.LabelSystem: sd.Name}
	if m, ok := c.AM.(alertmanager.MutedReader); ok {
		return m.AlertsWithInhibited(ctx, filter)
	}
	return c.AM.ActiveAlerts(ctx, filter)
}

// evidenceSources are the programs of a system's set that evidence is read
// from.
func (s *Server) evidenceSources(ctx context.Context, sd *v1alpha1.SystemDescription) evidence.Sources {
	c := s.clientsFor(ctx, sd.Spec.Stack)
	return evidence.Sources{Prom: c.Prom, LokiURL: c.Set.URL(stack.KindLoki)}
}
