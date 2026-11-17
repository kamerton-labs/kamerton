// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/telegram"
)

func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	ext := strings.TrimRight(s.opts.Generator.RunbookBaseURL, "/")
	reason := ""
	if s.opts.Telegram == nil {
		reason = s.opts.TelegramUnavailable
	}
	emailReason := ""
	if s.opts.Email == nil {
		emailReason = s.emailUnavailable()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"externalURL":  ext,
		"linksEnabled": ext != "",
		"mode":         s.opts.Descriptions.Mode(),
		"namespace":    s.opts.Namespace,
		"telegram":     map[string]any{"available": s.opts.Telegram != nil, "reason": reason},
		"email":        map[string]any{"available": s.opts.Email != nil, "reason": emailReason},
		"auth":         map[string]any{"enabled": s.opts.Auth != nil},
		// Grouping by cause works wherever Alertmanager runs the generated
		// inhibit rules; follow-up messages need the cluster (see hooks.go).
		"followUp": map[string]any{"available": s.followUpUnavailable() == "", "reason": s.followUpUnavailable()},
	})
}

type runbookAlert struct {
	Alert     string `json:"alert"`
	Severity  string `json:"severity"`
	Signal    string `json:"signal"`
	Operation string `json:"operation,omitempty"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Threshold string `json:"threshold,omitempty"`
	For       string `json:"for,omitempty"`
	Expr      string `json:"expr"`
}

// runbook renders the runbook of a service, the page behind runbook_url. Links in
// notifications carry only the service, so without ?system= it picks the first
// system that describes it, preferring one whose rules have that alert.
func (s *Server) runbook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("service")
	list, err := s.opts.Descriptions.List(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "description_unavailable", err.Error())
		return
	}
	want := r.URL.Query().Get("system")
	var match *descstore.Snapshot
	for _, snap := range list {
		if snap.SD == nil || (want != "" && snap.ID != want && snap.SD.Name != want) {
			continue
		}
		for _, svc := range snap.SD.Spec.Services {
			if svc.Name == name && match == nil {
				match = snap
			}
		}
		if name == generator.SystemRunbook && match == nil {
			match = snap
		}
	}
	if match == nil {
		if len(list) == 0 {
			writeError(w, http.StatusNotFound, "no_description", "no system description is loaded")
			return
		}
		writeError(w, http.StatusNotFound, "unknown_service", "service "+name+" is not in any system")
		return
	}
	s.runbookFor(w, match, name)
}

// runbookFor serves the runbook of a service, or with generator.SystemRunbook
// the one of the alerts about the whole system (service: {name, system: true}).
func (s *Server) runbookFor(w http.ResponseWriter, snap *descstore.Snapshot, name string) {
	found := name == generator.SystemRunbook
	var svcView map[string]any
	if found {
		svcView = map[string]any{"name": name, "system": true, "dependsOn": []string{}, "calledBy": []string{}}
	}
	for _, svc := range snap.SD.Spec.Services {
		if svc.Name == name {
			found = true
			svcView = map[string]any{
				"name": svc.Name, "type": svc.Type, "critical": svc.Critical, "description": svc.Description,
				"owner": svc.Owner, "dependsOn": svc.DependsOn, "runbookURL": svc.RunbookURL,
			}
			var callers []string
			for _, other := range snap.SD.Spec.Services {
				for _, d := range other.DependsOn {
					if d == svc.Name {
						callers = append(callers, other.Name)
					}
				}
			}
			sort.Strings(callers)
			svcView["calledBy"] = callers
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "unknown_service", "service "+name+" is not in the description")
		return
	}
	b, err := generator.Generate(snap.SD, s.opts.Generator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generation_failed", err.Error())
		return
	}
	alerts := []runbookAlert{}
	for _, g := range b.RuleGroups.Groups {
		for _, rl := range g.Rules {
			if svc := rl.Labels[rules.LabelService]; svc != name && (name != generator.SystemRunbook || svc != "") {
				continue
			}
			alerts = append(alerts, runbookAlert{
				Alert: rl.Alert, Severity: rl.Labels[rules.LabelSeverity], Signal: rl.Labels[rules.LabelSignal],
				Operation: rl.Labels[rules.LabelOperation], Title: rl.Annotations[rules.AnnotationTitle],
				Summary: rl.Annotations["summary"], Threshold: rl.Annotations[rules.AnnotationThreshold],
				For: rl.For.String(), Expr: rl.Expr,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"system": snap.SD.Name, "systemID": snap.ID, "service": svcView, "alerts": alerts, "markdown": b.Runbooks[name+".md"],
	})
}

// No handler here writes the bot token to a response or a log. It arrives only in
// PUT /telegram/token and goes straight to getMe and the token store.

func (s *Server) telegramReady(w http.ResponseWriter) bool {
	if s.opts.Telegram == nil {
		writeError(w, http.StatusServiceUnavailable, "not_available", s.opts.TelegramUnavailable)
		return false
	}
	return true
}

func (s *Server) telegramStatus(w http.ResponseWriter, r *http.Request) {
	s.telegramStatusFor(w, r, r.URL.Query().Get("system"))
}

func (s *Server) telegramStatusFor(w http.ResponseWriter, r *http.Request, system string) {
	if s.opts.Telegram == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": s.opts.TelegramUnavailable})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	st, err := s.opts.Telegram.StatusFor(ctx, system)
	if err != nil {
		writeError(w, http.StatusBadGateway, "telegram_state", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "status": st})
}

func (s *Server) telegramSetToken(w http.ResponseWriter, r *http.Request) {
	if !s.telegramReady(w) {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		// Do not echo the decoder error: it may quote the input.
		writeError(w, http.StatusBadRequest, "bad_request", "expected JSON {\"token\": \"...\"}")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	bot, err := s.opts.Telegram.SetToken(ctx, req.Token)
	req.Token = ""
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"bot": bot})
	case errors.Is(err, telegram.ErrMalformedToken), errors.Is(err, telegram.ErrNotBot), telegram.IsUnauthorized(err):
		writeError(w, http.StatusBadRequest, "invalid_token", telegram.DescribeError(err))
	default:
		var ae *telegram.APIError
		if errors.As(err, &ae) {
			writeError(w, http.StatusBadGateway, "telegram_error", telegram.DescribeError(err))
			return
		}
		writeError(w, http.StatusBadGateway, "store_failed", telegram.DescribeError(err))
	}
}

func (s *Server) telegramDeleteToken(w http.ResponseWriter, r *http.Request) {
	if !s.telegramReady(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.opts.Telegram.DeleteToken(ctx); err != nil {
		writeDescErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func parseChatID(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, "bad_chat_id", "chat ID must be a non-zero integer, for example -1001234567890")
		return 0, false
	}
	return id, true
}

func (s *Server) telegramAddChat(w http.ResponseWriter, r *http.Request) {
	if !s.telegramReady(w) {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req struct {
		ChatID json.Number `json:"chatID"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "expected JSON {\"chatID\": -100...}")
		return
	}
	id, ok := parseChatID(w, req.ChatID.String())
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	chat, err := s.opts.Telegram.AddChat(ctx, id)
	if err != nil {
		writeTelegramErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": chat})
}

func (s *Server) telegramDeleteChat(w http.ResponseWriter, r *http.Request) {
	if !s.telegramReady(w) {
		return
	}
	id, ok := parseChatID(w, r.PathValue("id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.opts.Telegram.DeleteChat(ctx, id); err != nil {
		writeDescErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) telegramTest(w http.ResponseWriter, r *http.Request) {
	if !s.telegramReady(w) {
		return
	}
	id, ok := parseChatID(w, r.PathValue("id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.opts.Telegram.SendTest(ctx, id)
	if err != nil {
		writeTelegramErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) telegramRouting(w http.ResponseWriter, r *http.Request) {
	s.telegramRoutingFor(w, r, r.URL.Query().Get("system"))
}

func (s *Server) telegramRoutingFor(w http.ResponseWriter, r *http.Request, system string) {
	if !s.telegramReady(w) {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req struct {
		Chats []telegram.RoutingInput `json:"chats"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	snap, err := s.opts.Telegram.SaveRoutingFor(ctx, system, req.Chats)
	if err != nil {
		writeDescErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "source": snap.Source})
}

func writeTelegramErr(w http.ResponseWriter, err error) {
	var ae *telegram.APIError
	switch {
	case errors.Is(err, telegram.ErrNoToken):
		writeError(w, http.StatusConflict, "no_token", "save the bot token first")
	case errors.As(err, &ae) && ae.Code >= 400 && ae.Code < 500:
		writeError(w, http.StatusUnprocessableEntity, "telegram_error", telegram.DescribeError(err))
	default:
		writeError(w, http.StatusBadGateway, "telegram_error", telegram.DescribeError(err))
	}
}

func writeDescErr(w http.ResponseWriter, err error) {
	var inv *descstore.InvalidError
	switch {
	case errors.Is(err, telegram.ErrNoToken):
		writeError(w, http.StatusConflict, "no_token", "save the bot token first")
	case errors.Is(err, telegram.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, telegram.ErrNamespace):
		writeError(w, http.StatusConflict, "namespace_mismatch", err.Error())
	case errors.Is(err, descstore.ErrNoDescription):
		writeError(w, http.StatusConflict, "no_description", "the system description is not saved yet: save it in the description editor")
	case errors.Is(err, descstore.ErrNotWritable):
		writeError(w, http.StatusConflict, "not_writable", err.Error())
	case errors.As(err, &inv):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": inv.Error(), "code": "invalid", "errors": inv.Result.Errors})
	default:
		writeError(w, http.StatusBadGateway, "save_failed", telegram.DescribeError(err))
	}
}
