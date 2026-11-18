// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

type systemSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	Source      string `json:"source"`
	Environment string `json:"environment,omitempty"`
	Description string `json:"description,omitempty"`
	Services    int    `json:"services"`
	Rules       int    `json:"rules"`
	Receivers   int    `json:"receivers"`
	// ChannelsOff lists receivers whose Secret is missing: their channel gets nothing yet.
	ChannelsOff []channelOff `json:"channelsOff,omitempty"`
	Valid       bool         `json:"valid"`
	Problem     string       `json:"problem,omitempty"`
	// Alerts is nil when Alertmanager is not configured or unreachable.
	Alerts *alertCounts `json:"alerts"`
	// State: ok, warning, firing, invalid or unknown (no alert data).
	State string `json:"state"`
	// Stack is the set of programs the system uses (unknown sets resolve to
	// the built-in one; StackUnknown then names what was asked for).
	Stack        string `json:"stack"`
	StackName    string `json:"stackName"`
	StackUnknown string `json:"stackUnknown,omitempty"`
}

type channelOff struct {
	Receiver string `json:"receiver"`
	// Channel: telegram or email.
	Channel string `json:"channel"`
}

type alertCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
}

const AnnotationDescription = descstore.AnnotationPrefix + "description"

func (s *Server) listSystems(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := s.opts.Descriptions.List(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "description_unavailable", err.Error())
		return
	}
	counts, configured, aerr := s.alertCounts(ctx, list)
	out := make([]systemSummary, 0, len(list))
	for _, snap := range list {
		sum := systemSummary{ID: snap.ID, Name: snap.Name(), Source: snap.Source, Valid: snap.SD != nil}
		c := s.clientsFor(ctx, stackOf(snap))
		sum.Stack, sum.StackName, sum.StackUnknown = c.Set.ID, c.Set.Name, c.Unknown
		if snap.SD == nil {
			sum.State = "invalid"
			if snap.Invalid != nil && len(snap.Invalid.Errors) > 0 {
				sum.Problem = snap.Invalid.Errors[0].Path + ": " + snap.Invalid.Errors[0].Message
			}
			out = append(out, sum)
			continue
		}
		sd := snap.SD
		sum.Namespace, sum.Environment, sum.Services = sd.Namespace, sd.Spec.Environment, len(sd.Spec.Services)
		sum.Description = sd.Annotations[AnnotationDescription]
		if n := sd.Spec.Notifications; n != nil {
			sum.Receivers = len(n.Receivers)
		}
		gen := s.opts.Generator
		if check := s.opts.SecretMissing; check != nil {
			gen.SecretMissing = func(ref v1alpha1.SecretKeyRef) bool { return check(ctx, sd.Namespace, ref) }
		}
		if b, err := generator.Generate(sd, gen); err == nil {
			sum.Rules = b.RuleCount
			for _, m := range b.MissingSecrets {
				sum.ChannelsOff = append(sum.ChannelsOff, channelOff{Receiver: m.Receiver, Channel: m.Channel})
			}
		} else {
			sum.Problem = err.Error()
		}
		cnt, known := counts[snap.ID]
		switch {
		case !known:
			sum.State = "unknown"
		default:
			c := cnt
			sum.Alerts = c
			switch {
			case c.Critical > 0:
				sum.State = "firing"
			case c.Warning > 0:
				sum.State = "warning"
			default:
				sum.State = "ok"
			}
		}
		out = append(out, sum)
	}
	nsets := 1
	if sets, err := s.setList(ctx); err == nil {
		nsets = len(sets)
	}
	resp := map[string]any{
		"mode": s.opts.Descriptions.Mode(), "writable": s.opts.Descriptions.Mode() != descstore.ModeNone,
		"systems": out, "alertsConfigured": configured, "starter": starterDescription, "stacks": nsets,
	}
	if aerr != nil {
		resp["alertsError"] = aerr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// alertCounts counts active alerts per system ID from the Alertmanager of its set,
// or without one from Prometheus' ALERTS, asking each source once. A system
// missing from counts has no alert data; err is the first failure.
func (s *Server) alertCounts(ctx context.Context, list []*descstore.Snapshot) (counts map[string]*alertCounts, configured bool, err error) {
	type source struct {
		am     alertmanager.Reader
		prom   prometheus.Querier
		labels []map[string]string
		err    error
	}
	sources := map[string]*source{}
	bySystem := map[string]string{}
	for _, snap := range list {
		if snap.SD == nil {
			continue
		}
		c := s.clientsFor(ctx, stackOf(snap))
		var key string
		switch {
		case c.AM != nil:
			key = "am " + c.Set.URL(stack.KindAlertmanager)
			if sources[key] == nil {
				sources[key] = &source{am: c.AM}
			}
		case c.Prom != nil:
			key = "prom " + c.Set.URL(stack.KindPrometheus)
			if sources[key] == nil {
				sources[key] = &source{prom: c.Prom}
			}
		default:
			continue
		}
		bySystem[snap.ID] = key
	}
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(src *source) {
			defer wg.Done()
			if src.am != nil {
				raw, err := src.am.ActiveAlerts(ctx, map[string]string{rules.LabelManaged: "true"})
				for _, a := range raw {
					src.labels = append(src.labels, a.Labels)
				}
				src.err = err
				return
			}
			m := rules.Matchers{}.Eq(rules.LabelManaged, "true").Eq("alertstate", "firing")
			v, err := src.prom.Query(ctx, m.Selector("ALERTS"), time.Now())
			src.err = err
			if vec, ok := v.(model.Vector); ok {
				for _, smp := range vec {
					lbls := map[string]string{}
					for k, val := range smp.Metric {
						lbls[string(k)] = string(val)
					}
					src.labels = append(src.labels, lbls)
				}
			}
		}(src)
	}
	wg.Wait()
	counts = map[string]*alertCounts{}
	for _, snap := range list {
		key, ok := bySystem[snap.ID]
		if !ok {
			continue
		}
		src := sources[key]
		if src.am != nil {
			configured = true
		}
		if src.err != nil {
			if err == nil && src.am != nil {
				err = src.err
			}
			continue
		}
		configured = true
		c := &alertCounts{}
		for _, l := range src.labels {
			if l[generator.LabelSystem] != snap.SD.Name {
				continue
			}
			if l[rules.LabelSeverity] == "critical" {
				c.Critical++
			} else {
				c.Warning++
			}
		}
		counts[snap.ID] = c
	}
	return counts, configured, err
}

// system writes the error response and returns nil when it cannot load the system.
// requireValid rejects stored documents that do not validate.
func (s *Server) system(w http.ResponseWriter, r *http.Request, requireValid bool) *descstore.Snapshot {
	snap, err := s.opts.Descriptions.Get(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, descstore.ErrNoDescription):
		writeError(w, http.StatusNotFound, "not_found", "no such system: "+r.PathValue("id"))
		return nil
	case err != nil:
		writeError(w, http.StatusBadGateway, "description_unavailable", err.Error())
		return nil
	case requireValid && snap.SD == nil:
		writeError(w, http.StatusConflict, "invalid", "the stored description of this system has errors; fix it in YAML mode")
		return nil
	}
	return snap
}

func (s *Server) getSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	mode := s.opts.Descriptions.Mode()
	c := s.clientsFor(r.Context(), stackOf(snap))
	resp := map[string]any{
		"id": snap.ID, "name": snap.Name(), "yaml": string(snap.Raw), "source": snap.Source,
		"mode": mode, "writable": mode != descstore.ModeNone, "valid": snap.SD != nil,
		"errors": []description.Issue{}, "warnings": []description.Issue{},
		"stack": map[string]any{"id": c.Set.ID, "name": c.Set.Name, "unknown": c.Unknown, "grafanaURL": c.Set.URL(stack.KindGrafana)},
	}
	if snap.Invalid != nil {
		resp["errors"], resp["warnings"] = snap.Invalid.Errors, snap.Invalid.Warnings
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) createSystem(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !s.allowSave(ctx, w, body, nil) {
		return
	}
	snap, res, err := s.opts.Descriptions.Create(ctx, body)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.log.Info("system created", "system", snap.ID, "source", snap.Source)
	writeJSON(w, http.StatusCreated, map[string]any{"saved": true, "id": snap.ID, "source": snap.Source, "warnings": append(res.Warnings, s.stackWarnings(ctx, stackOf(snap))...)})
}

func (s *Server) putSystem(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// The stored version decides which Secret references may stay; Save reports a missing system.
	stored, _ := s.opts.Descriptions.Get(ctx, r.PathValue("id"))
	if !s.allowSave(ctx, w, body, stored) {
		return
	}
	snap, res, err := s.opts.Descriptions.Save(ctx, r.PathValue("id"), body)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.log.Info("system saved", "system", snap.ID, "source", snap.Source)
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "id": snap.ID, "source": snap.Source, "mode": s.opts.Descriptions.Mode(), "warnings": append(res.Warnings, s.stackWarnings(ctx, stackOf(snap))...)})
}

func (s *Server) deleteSystem(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.opts.Descriptions.Delete(ctx, r.PathValue("id")); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.log.Info("system deleted", "system", r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) validateSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	res := description.Validate(body)
	if res.Document != nil && res.Document.Name != snap.Name() {
		res.Errors = append(res.Errors, description.Issue{Path: "/metadata/name", Message: "the name of a system cannot be changed (was " + snap.Name() + ")", Severity: description.SeverityError})
		res.Valid = false
	}
	if res.Document != nil {
		s.checkDraftSecrets(r.Context(), &res, snap)
		res.Warnings = append(res.Warnings, s.stackWarnings(r.Context(), res.Document.Spec.Stack)...)
	}
	writeJSON(w, http.StatusOK, res)
}

// draft returns the request body, or the stored description when it is empty.
func draft(body []byte, snap *descstore.Snapshot) []byte {
	if len(body) == 0 {
		return snap.Raw
	}
	return body
}

func (s *Server) previewSystemRules(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.preview(w, r, draft(body, snap), snap)
}

func (s *Server) discoverSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	s.discoverFor(w, r, snap)
}

func (s *Server) alertsSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	s.alertsFor(w, r, snap)
}

type systemBacktestRequest struct {
	backtestRequest
	YAML      string `json:"yaml"`
	Service   string `json:"service"`
	Alert     string `json:"alert"`
	Operation string `json:"operation"`
}

func (s *Server) backtestSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req systemBacktestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	prom := s.clientsFor(r.Context(), stackOf(snap)).Prom
	if req.Alert == "" {
		s.runBacktest(w, r, prom, req.backtestRequest, nil)
		return
	}
	res := description.Validate(draft([]byte(req.YAML), snap))
	if !res.Valid {
		writeInvalid(w, res)
		return
	}
	b, err := generator.Generate(res.Document, s.opts.Generator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generation_failed", err.Error())
		return
	}
	for _, g := range b.RuleGroups.Groups {
		for _, rl := range g.Rules {
			if rl.Alert != req.Alert || rl.Labels[rules.LabelService] != req.Service || rl.Labels[rules.LabelOperation] != req.Operation {
				continue
			}
			bt := req.backtestRequest
			bt.Expr, bt.For = rl.Expr, model.Duration(rl.For).String()
			s.runBacktest(w, r, prom, bt, map[string]any{
				"expr": rl.Expr, "for": bt.For, "title": rl.Annotations[rules.AnnotationTitle], "threshold": rl.Annotations[rules.AnnotationThreshold],
			})
			return
		}
	}
	writeError(w, http.StatusNotFound, "no_rule", "the description does not produce this rule (is it turned off?)")
}

type ruleState struct {
	Alert     string `json:"alert"`
	Service   string `json:"service"`
	Operation string `json:"operation,omitempty"`
	Rule      string `json:"rule,omitempty"`
	// State: pending (the condition holds, waiting for "for") or firing.
	State    string            `json:"state"`
	Severity string            `json:"severity"`
	Labels   map[string]string `json:"labels"`
}

func (s *Server) ruleStates(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	prom := s.clientsFor(r.Context(), stackOf(snap)).Prom
	if prom == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "reachable": false, "states": []any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	m := rules.Matchers{}.Eq(generator.LabelSystem, snap.Name()).Eq(rules.LabelManaged, "true")
	v, err := prom.Query(ctx, m.Selector("ALERTS"), time.Now())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "reachable": false, "message": err.Error(), "states": []any{}})
		return
	}
	out := []ruleState{}
	if vec, ok := v.(model.Vector); ok {
		for _, smp := range vec {
			lbls := map[string]string{}
			for k, val := range smp.Metric {
				lbls[string(k)] = string(val)
			}
			out = append(out, ruleState{
				Alert: lbls["alertname"], Service: lbls[rules.LabelService], Operation: lbls[rules.LabelOperation], Rule: lbls[rules.LabelRule],
				State: lbls["alertstate"], Severity: lbls[rules.LabelSeverity], Labels: lbls,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "reachable": true, "states": out})
}

func (s *Server) runbookSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, true)
	if snap == nil {
		return
	}
	s.runbookFor(r.Context(), w, snap, r.PathValue("service"))
}

func (s *Server) telegramStatusSystem(w http.ResponseWriter, r *http.Request) {
	if snap := s.system(w, r, false); snap == nil {
		return
	}
	s.telegramStatusFor(w, r, r.PathValue("id"))
}

func (s *Server) telegramRoutingSystem(w http.ResponseWriter, r *http.Request) {
	if snap := s.system(w, r, false); snap == nil {
		return
	}
	s.telegramRoutingFor(w, r, r.PathValue("id"))
}
