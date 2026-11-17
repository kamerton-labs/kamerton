// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/history"
	"github.com/kamerton-labs/kamerton/internal/stack"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = history.MaxRecords
)

// attachHistory records the live incidents of the systems this response
// covers and adds each one's history ID and earlier incident. A system counts
// as complete (its other open records have ended) only when its
// Alertmanager answered.
func (s *Server) attachHistory(ctx context.Context, list []*descstore.Snapshot, one *descstore.Snapshot, partial []string,
	bySystem map[string][]triage.Alert, incidents []incidentView) {
	failed := map[string]bool{}
	for _, u := range partial {
		failed[u] = true
	}
	snaps := list
	if one != nil {
		snaps = []*descstore.Snapshot{one}
	}
	byID := map[string][]triage.Incident{}
	for _, v := range incidents {
		byID[v.System] = append(byID[v.System], v.Incident)
	}
	var obs []history.Observation
	for _, snap := range snaps {
		if snap == nil || snap.SD == nil {
			continue
		}
		name := snap.SD.Name
		c := s.clientsFor(ctx, stackOf(snap))
		complete := c.AM != nil && !failed[c.Set.URL(stack.KindAlertmanager)]
		if !complete && len(bySystem[name]) == 0 {
			continue
		}
		obs = append(obs, history.Observation{System: name, Incidents: byID[name], Complete: complete})
	}
	if len(obs) == 0 {
		return
	}
	st, err := s.history.Observe(ctx, obs...)
	if err != nil {
		s.log.Warn("incident history unavailable", "error", err)
		return
	}
	for i := range incidents {
		v := &incidents[i]
		if r := st.OpenRecord(v.System, v.ID); r != nil {
			v.HistoryID = r.ID
		} else if p := s.history.AftermathOf(st, v.System, v.Incident); p != nil {
			// The tail of an earlier incident has no record of its own.
			v.HistoryID, v.AftermathOf = p.ID, p.ID
		}
		v.Previous = s.history.RecallFrom(st, v.System, v.Incident)
	}
}

// systemName maps a system ID or name to the name records carry.
func (s *Server) systemName(ctx context.Context, id string) string {
	list, err := s.opts.Descriptions.List(ctx)
	if err != nil {
		return id
	}
	for _, snap := range list {
		if snap.ID == id && snap.SD != nil {
			return snap.SD.Name
		}
	}
	return id
}

func (s *Server) incidentHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := history.Filter{Service: q.Get("service"), Limit: defaultHistoryLimit}
	if sys := q.Get("system"); sys != "" {
		f.System = s.systemName(r.Context(), sys)
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive number")
			return
		}
		f.Limit = min(n, maxHistoryLimit)
	}
	list, err := s.history.List(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusBadGateway, "state_unavailable", err.Error())
		return
	}
	out := make([]history.View, 0, len(list))
	for _, rec := range list {
		out = append(out, rec.View())
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": out})
}

func (s *Server) incidentNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !history.ValidID(id) {
		writeError(w, http.StatusNotFound, "not_found", "no such incident in the history")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var in history.NoteInput
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "expected {\"cause\": \"...\", \"fix\": \"...\"}")
		return
	}
	rec, err := s.history.SetNote(r.Context(), id, in, history.SourceUI)
	switch {
	case errors.Is(err, history.ErrTooLong):
		writeError(w, http.StatusBadRequest, "too_long",
			"cause may have at most "+strconv.Itoa(history.MaxCause)+" characters and fix at most "+strconv.Itoa(history.MaxFix))
		return
	case errors.Is(err, history.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such incident in the history")
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, "state_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec.View())
}
