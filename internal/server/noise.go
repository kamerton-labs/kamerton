// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/kamerton-labs/kamerton/internal/noise"
)

func (s *Server) noiseSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, true)
	if snap == nil {
		return
	}
	window, name, err := noise.ParseWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	fb, err := s.feedback.Counts(ctx, snap.Name())
	if err != nil {
		s.log.Warn("noise feedback unavailable", "system", snap.ID, "error", err)
		fb = map[noise.Key]noise.Counts{}
	}
	a := &noise.Analyzer{Prom: s.clientsFor(ctx, stackOf(snap)).Prom, Generator: s.opts.Generator, Logger: s.log}
	rep, err := a.Analyze(ctx, snap.SD, window, name, fb)
	switch {
	case errors.Is(err, noise.ErrNotConfigured):
		rep = noise.Unavailable(name, "Prometheus is not configured for the set of programs of this system, so there is no alert history.", fb)
	case err != nil:
		rep = noise.Unavailable(name, "Prometheus did not answer: "+err.Error(), fb)
	}
	writeJSON(w, http.StatusOK, rep)
}

type feedbackRequest struct {
	Alert     string `json:"alert"`
	Service   string `json:"service"`
	Operation string `json:"operation"`
	Verdict   string `json:"verdict"`
}

var alertNamePattern = regexp.MustCompile(`^Kamerton[A-Za-z0-9]{1,120}$`)

const maxOperationLength = 256

func (s *Server) noiseFeedback(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, true)
	if snap == nil {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req feedbackRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	known := req.Service == ""
	for _, svc := range snap.SD.Spec.Services {
		known = known || svc.Name == req.Service
	}
	switch {
	case req.Verdict != noise.VerdictUseful && req.Verdict != noise.VerdictNoise:
		writeError(w, http.StatusBadRequest, "bad_request", noise.ErrBadVerdict.Error())
		return
	case !alertNamePattern.MatchString(req.Alert):
		writeError(w, http.StatusBadRequest, "bad_request", "alert must be the name of a Kamerton alert")
		return
	case !known:
		writeError(w, http.StatusBadRequest, "bad_request", "no such service in this system: "+req.Service)
		return
	case len(req.Operation) > maxOperationLength:
		writeError(w, http.StatusBadRequest, "bad_request", "operation is too long")
		return
	}
	k := noise.KeyOf(snap.SD, req.Alert, req.Service, req.Operation)
	c, err := s.feedback.Record(r.Context(), snap.Name(), k, req.Verdict)
	if err != nil {
		writeError(w, http.StatusBadGateway, "state_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true, "alert": k.Alert, "service": k.Service, "operation": k.Operation, "feedback": c})
}
