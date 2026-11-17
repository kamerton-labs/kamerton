// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/email"
)

// ---- Email ----
//
// No handler here ever writes the SMTP password to a response or a log. It
// arrives only in PUT /email/server and goes to the SMTP check and the
// password store (a Kubernetes Secret).

func (s *Server) emailReady(w http.ResponseWriter) bool {
	if s.opts.Email == nil {
		writeError(w, http.StatusServiceUnavailable, "not_available", s.emailUnavailable())
		return false
	}
	return true
}

func (s *Server) emailUnavailable() string {
	if s.opts.EmailUnavailable != "" {
		return s.opts.EmailUnavailable
	}
	return "Email setup from the UI is not available."
}

func (s *Server) emailStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Email == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": s.emailUnavailable()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	st, err := s.opts.Email.Status(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "email_state", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "status": st})
}

func (s *Server) emailSetServer(w http.ResponseWriter, r *http.Request) {
	if !s.emailReady(w) {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req email.ServerInput
	if err := json.Unmarshal(body, &req); err != nil {
		// Do not echo the decoder error: it may quote the input.
		writeError(w, http.StatusBadRequest, "bad_request", "expected JSON {\"smarthost\", \"from\", \"username\", \"password\"}")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	st, err := s.opts.Email.SetServer(ctx, req)
	req.Password = ""
	if err != nil {
		writeEmailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server": st})
}

func (s *Server) emailDeleteServer(w http.ResponseWriter, r *http.Request) {
	if !s.emailReady(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.opts.Email.DeleteServer(ctx); err != nil {
		writeEmailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func decodeRecipient(w http.ResponseWriter, r *http.Request) (email.RecipientInput, bool) {
	var req email.RecipientInput
	body, ok := readBody(w, r)
	if !ok {
		return req, false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "expected JSON {\"name\": \"...\", \"to\": [\"...\"]}")
		return req, false
	}
	return req, true
}

var recipientIDRe = regexp.MustCompile(`^[a-f0-9]{1,32}$`)

func recipientID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !recipientIDRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "unknown_recipient", "no such recipient")
		return "", false
	}
	return id, true
}

func (s *Server) emailAddRecipient(w http.ResponseWriter, r *http.Request) {
	if !s.emailReady(w) {
		return
	}
	req, ok := decodeRecipient(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rc, err := s.opts.Email.AddRecipient(ctx, req)
	if err != nil {
		writeEmailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recipient": rc})
}

func (s *Server) emailUpdateRecipient(w http.ResponseWriter, r *http.Request) {
	if !s.emailReady(w) {
		return
	}
	id, ok := recipientID(w, r)
	if !ok {
		return
	}
	req, ok := decodeRecipient(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rc, err := s.opts.Email.UpdateRecipient(ctx, id, req)
	if err != nil {
		writeEmailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recipient": rc})
}

func (s *Server) emailDeleteRecipient(w http.ResponseWriter, r *http.Request) {
	if !s.emailReady(w) {
		return
	}
	id, ok := recipientID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.opts.Email.DeleteRecipient(ctx, id); err != nil {
		writeEmailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) emailTest(w http.ResponseWriter, r *http.Request) {
	if !s.emailReady(w) {
		return
	}
	id, ok := recipientID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := s.opts.Email.SendTest(ctx, id)
	if err != nil {
		writeEmailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeEmailErr(w http.ResponseWriter, err error) {
	var inv *descstore.InvalidError
	switch {
	case errors.Is(err, email.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), email.ErrInvalidInput.Error()+": "))
	case errors.Is(err, email.ErrCheckFailed):
		writeError(w, http.StatusUnprocessableEntity, "smtp_check_failed", strings.TrimPrefix(err.Error(), email.ErrCheckFailed.Error()+": "))
	case errors.Is(err, email.ErrNoServer):
		writeError(w, http.StatusConflict, "no_server", "connect a mail server first")
	case errors.Is(err, email.ErrUnknownRecipient):
		writeError(w, http.StatusNotFound, "unknown_recipient", "no such recipient")
	case errors.Is(err, descstore.ErrNotWritable):
		writeError(w, http.StatusConflict, "not_writable", err.Error())
	case errors.As(err, &inv):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": inv.Error(), "code": "invalid", "errors": inv.Result.Errors})
	default:
		writeError(w, http.StatusBadGateway, "save_failed", email.DescribeError(err))
	}
}
