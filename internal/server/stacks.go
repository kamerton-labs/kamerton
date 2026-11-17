// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

// Every read of Prometheus or Alertmanager for a system goes through its set's clients (clientsFor).

type setClients struct {
	Set stack.Resolved
	// Unknown is the set ID asked for when no such set exists; the built-in set is used instead.
	Unknown string
	// Prom and AM are nil when the set has no such program.
	Prom prometheus.Querier
	AM   alertmanager.Reader
}

// clientsFor treats "" as the built-in set; an unknown ID falls back to it.
func (s *Server) clientsFor(ctx context.Context, stackID string) setClients {
	set, unknown, err := s.resolveSet(ctx, stackID)
	if err != nil {
		s.log.Warn("sets of programs unavailable; using the install-time addresses", "error", err)
		set = s.sets.Builtin()
		unknown = stackID != "" && stackID != stack.BuiltinID
	}
	c := setClients{Set: set, Prom: s.promFor(set.URL(stack.KindPrometheus)), AM: s.amFor(set.URL(stack.KindAlertmanager))}
	if unknown {
		c.Unknown = stackID
	}
	return c
}

// setList is remembered for two seconds: one page load resolves every system's set several
// times, and in a cluster each read hits the API server. API changes forget it.
func (s *Server) setList(ctx context.Context) ([]stack.Resolved, error) {
	s.setsMu.Lock()
	if s.setsCache != nil && time.Since(s.setsAt) < 2*time.Second {
		l := s.setsCache
		s.setsMu.Unlock()
		return l, nil
	}
	s.setsMu.Unlock()
	l, err := s.sets.List(ctx)
	if err != nil {
		return nil, err
	}
	s.setsMu.Lock()
	s.setsCache, s.setsAt = l, time.Now()
	s.setsMu.Unlock()
	return l, nil
}

func (s *Server) forgetSets() {
	s.setsMu.Lock()
	s.setsCache = nil
	s.setsMu.Unlock()
}

func (s *Server) resolveSet(ctx context.Context, id string) (stack.Resolved, bool, error) {
	list, err := s.setList(ctx)
	if err != nil {
		return stack.Resolved{}, false, err
	}
	if id == "" {
		id = stack.BuiltinID
	}
	var builtin stack.Resolved
	for _, set := range list {
		if set.ID == id {
			return set, false, nil
		}
		if set.Builtin {
			builtin = set
		}
	}
	return builtin, true, nil
}

// promFor uses the client from Options for the install-time address.
func (s *Server) promFor(u string) prometheus.Querier {
	if u == s.sets.FlagURL(stack.KindPrometheus) && s.opts.Prometheus != nil {
		return s.opts.Prometheus
	}
	if u == "" {
		return nil
	}
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	if c, ok := s.proms[u]; ok {
		return c
	}
	newProm := s.opts.NewPrometheus
	if newProm == nil {
		newProm = func(u string) (prometheus.Querier, error) { return prometheus.New(u) }
	}
	c, err := newProm(u)
	if err != nil {
		s.log.Warn("prometheus client", "url", u, "error", err)
		return nil
	}
	s.proms[u] = c
	return c
}

func (s *Server) amFor(u string) alertmanager.Reader {
	if u == s.sets.FlagURL(stack.KindAlertmanager) && s.opts.Alertmanager != nil {
		return s.opts.Alertmanager
	}
	if u == "" {
		return nil
	}
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	if c, ok := s.ams[u]; ok {
		return c
	}
	newAM := s.opts.NewAlertmanager
	if newAM == nil {
		newAM = func(u string) alertmanager.Reader { return alertmanager.New(u) }
	}
	c := newAM(u)
	s.ams[u] = c
	return c
}

// stackOf also works for a document that does not pass validation.
func stackOf(snap *descstore.Snapshot) string {
	if snap == nil {
		return ""
	}
	if snap.SD != nil {
		return snap.SD.Spec.Stack
	}
	var doc struct {
		Spec struct {
			Stack string `json:"stack"`
		} `json:"spec"`
	}
	if yaml.Unmarshal(snap.Raw, &doc) == nil {
		return doc.Spec.Stack
	}
	return ""
}

func (s *Server) stackWarnings(ctx context.Context, id string) []description.Issue {
	if id == "" || id == stack.BuiltinID {
		return nil
	}
	if _, unknown, err := s.resolveSet(ctx, id); err == nil && unknown {
		return []description.Issue{{Path: "/spec/stack", Severity: description.SeverityWarning,
			Message: "there is no set of programs \"" + id + "\": the system uses the " + stack.BuiltinName + " set"}}
	}
	return nil
}

// usage maps a set ID to the systems that ask for it (asked) and that really use it
// (resolved, where unknown sets resolve to the built-in one).
func (s *Server) usage(ctx context.Context, sets []stack.Resolved) (asked, resolved map[string][]string) {
	asked, resolved = map[string][]string{}, map[string][]string{}
	list, err := s.opts.Descriptions.List(ctx)
	if err != nil {
		return asked, resolved
	}
	known := map[string]bool{}
	for _, set := range sets {
		known[set.ID] = true
	}
	for _, snap := range list {
		id := stackOf(snap)
		if id == "" {
			id = stack.BuiltinID
		}
		asked[id] = append(asked[id], snap.ID)
		if !known[id] {
			id = stack.BuiltinID
		}
		resolved[id] = append(resolved[id], snap.ID)
	}
	return asked, resolved
}

type stackView struct {
	stack.Resolved
	Health   []stack.Status `json:"health"`
	UsedBy   []string       `json:"usedBy"`
	Warnings []string       `json:"warnings"`
}

// operator is remembered for a minute: every status page refresh reads it for warnings.
func (s *Server) operator(ctx context.Context) *stack.Operator {
	if s.opts.Discoverer == nil {
		return nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.op != nil && time.Since(s.opAt) < time.Minute {
		return s.op
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	op := s.opts.Discoverer.Operator(ctx)
	s.op, s.opAt = &op, time.Now()
	return s.op
}

func (s *Server) discoveryState() map[string]any {
	if s.opts.Discoverer == nil {
		reason := s.opts.DiscoveryUnavailable
		if reason == "" {
			reason = "Discovery of installed programs is not available."
		}
		return map[string]any{"available": false, "reason": reason}
	}
	return map[string]any{"available": true}
}

func (s *Server) listStacks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	sets, err := s.sets.List(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "stacks_unavailable", err.Error())
		return
	}
	_, resolved := s.usage(ctx, sets)
	op := s.operator(ctx)
	out := make([]stackView, len(sets))
	var wg sync.WaitGroup
	for i := range sets {
		out[i] = stackView{Resolved: sets[i], UsedBy: append([]string{}, resolved[sets[i].ID]...), Warnings: []string{}}
		if len(sets) > 1 || len(out[i].UsedBy) > 0 {
			out[i].Warnings = append(out[i].Warnings, stack.SetWarnings(&sets[i], op)...)
		}
		wg.Add(1)
		go func(v *stackView) {
			defer wg.Done()
			v.Health = s.checker.Health(ctx, &v.Resolved)
		}(&out[i])
	}
	wg.Wait()
	warnings := []string{}
	if op != nil && len(sets) > 1 {
		warnings = append(warnings, stack.OperatorWarnings(op)...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sets": out, "discovery": s.discoveryState(), "warnings": warnings})
}

func (s *Server) stackStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id := r.URL.Query().Get("set")
	if id == "" {
		id = stack.BuiltinID
	}
	set, err := s.sets.Get(ctx, id)
	if err != nil {
		writeStackErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"set": set.ID, "components": s.checker.Health(ctx, set)})
}

func decodeStackInput(w http.ResponseWriter, r *http.Request) (stack.Input, bool) {
	var in stack.Input
	body, ok := readBody(w, r)
	if !ok {
		return in, false
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", `expected JSON {"name", "urls": {"prometheus": "http://…"}, "ruleLabels", "alertmanagerConfigLabels"}`)
		return in, false
	}
	return in, true
}

func (s *Server) createStack(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeStackInput(w, r)
	if !ok {
		return
	}
	set, err := s.sets.Create(r.Context(), in)
	s.forgetSets()
	if err != nil {
		writeStackErr(w, err)
		return
	}
	s.log.Info("set of programs created", "set", set.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"set": set})
}

func (s *Server) updateStack(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeStackInput(w, r)
	if !ok {
		return
	}
	set, err := s.sets.Update(r.Context(), r.PathValue("id"), in)
	s.forgetSets()
	if err != nil {
		writeStackErr(w, err)
		return
	}
	s.log.Info("set of programs changed", "set", set.ID)
	writeJSON(w, http.StatusOK, map[string]any{"set": set})
}

func (s *Server) deleteStack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sets, err := s.sets.List(r.Context())
	if err != nil {
		writeStackErr(w, err)
		return
	}
	asked, _ := s.usage(r.Context(), sets)
	err = s.sets.Delete(r.Context(), id, asked[id])
	s.forgetSets()
	if err != nil {
		writeStackErr(w, err)
		return
	}
	s.log.Info("set of programs deleted", "set", id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) setStackProgram(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", `expected JSON {"url": "http://…"}`)
		return
	}
	kind := r.PathValue("kind")
	if !stack.ValidKind(kind) {
		writeError(w, http.StatusNotFound, "unknown_program", "no such program: "+kind)
		return
	}
	set, err := s.sets.Update(r.Context(), r.PathValue("id"), stack.Input{URLs: map[string]string{kind: req.URL}})
	s.forgetSets()
	if err != nil {
		writeStackErr(w, err)
		return
	}
	s.log.Info("program address changed", "set", set.ID, "program", kind)
	writeJSON(w, http.StatusOK, map[string]any{"set": set})
}

func (s *Server) resetStackProgram(w http.ResponseWriter, r *http.Request) {
	set, err := s.sets.ResetAddress(r.Context(), r.PathValue("id"), r.PathValue("kind"))
	s.forgetSets()
	if err != nil {
		writeStackErr(w, err)
		return
	}
	s.log.Info("program address reset", "set", set.ID, "program", r.PathValue("kind"))
	writeJSON(w, http.StatusOK, map[string]any{"set": set})
}

// checkStackAddress only sends GET requests to the given address.
func (s *Server) checkStackAddress(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req struct {
		Kind string `json:"kind"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", `expected JSON {"kind": "prometheus", "url": "http://…"}`)
		return
	}
	if !stack.ValidKind(req.Kind) {
		writeError(w, http.StatusBadRequest, "unknown_program", "no such program: "+req.Kind)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	httpc := s.opts.CheckHTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 5 * time.Second}
	}
	writeJSON(w, http.StatusOK, stack.Check(ctx, httpc, req.Kind, req.URL))
}

func (s *Server) discoverStacks(w http.ResponseWriter, r *http.Request) {
	if s.opts.Discoverer == nil {
		writeJSON(w, http.StatusOK, s.discoveryState())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.opts.Discoverer.Discover(ctx)
	if err != nil {
		var de *stack.DiscoveryError
		msg := err.Error()
		if errors.As(err, &de) {
			msg = de.Message
		}
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": msg})
		return
	}
	s.opMu.Lock()
	op := res.Operator
	s.op, s.opAt = &op, time.Now()
	s.opMu.Unlock()
	if sets, err := s.sets.List(ctx); err == nil {
		for i := range res.Found {
			f := &res.Found[i]
			for _, set := range sets {
				if f.URL != "" && sameAddress(set.URL(f.Kind), f.URL, s.opts.Namespace) {
					f.UsedBy = append(f.UsedBy, set.ID)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "found": res.Found, "operator": res.Operator, "warnings": res.Warnings})
}

func sameAddress(a, b, ns string) bool { return stack.SameAddress(a, b, ns) }

func writeStackErr(w http.ResponseWriter, err error) {
	var in *stack.InputError
	var use *stack.InUseError
	switch {
	case errors.As(err, &in):
		writeError(w, http.StatusBadRequest, "invalid", in.Message)
	case errors.As(err, &use):
		writeJSON(w, http.StatusConflict, map[string]any{"code": "in_use", "systems": use.Systems,
			"error": "the set is used by systems: " + strings.Join(use.Systems, ", ") + ". Choose another set for them first."})
	case errors.Is(err, stack.ErrUnknownSet):
		writeError(w, http.StatusNotFound, "unknown_set", "no such set of programs")
	case errors.Is(err, stack.ErrBuiltin):
		writeError(w, http.StatusConflict, "builtin", "the main set cannot be deleted")
	default:
		writeError(w, http.StatusBadGateway, "stacks_unavailable", err.Error())
	}
}

type amSource struct {
	url string
	am  alertmanager.Reader
}

// alertmanagersInUse falls back to the built-in set when there are no systems.
func (s *Server) alertmanagersInUse(ctx context.Context, list []*descstore.Snapshot) []amSource {
	seen := map[string]bool{}
	var out []amSource
	add := func(c setClients) {
		u := c.Set.URL(stack.KindAlertmanager)
		if c.AM == nil || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, amSource{url: u, am: c.AM})
	}
	for _, snap := range list {
		add(s.clientsFor(ctx, stackOf(snap)))
	}
	if len(list) == 0 {
		add(s.clientsFor(ctx, ""))
	}
	return out
}

// fetchAlerts includes alerts muted by inhibit rules when the reader can list them.
func fetchAlerts(ctx context.Context, sources []amSource, filter map[string]string) (lists [][]alertmanager.Alert, errs []error) {
	lists = make([][]alertmanager.Alert, len(sources))
	errs = make([]error, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src amSource) {
			defer wg.Done()
			if m, ok := src.am.(alertmanager.MutedReader); ok {
				lists[i], errs[i] = m.AlertsWithInhibited(ctx, filter)
				return
			}
			lists[i], errs[i] = src.am.ActiveAlerts(ctx, filter)
		}(i, src)
	}
	wg.Wait()
	return lists, errs
}

// mergeAlerts keeps an alert reported by several Alertmanagers once (same fingerprint, else
// same labels), with the earliest start.
func mergeAlerts(lists ...[]alertmanager.Alert) []alertmanager.Alert {
	idx := map[string]int{}
	var out []alertmanager.Alert
	for _, list := range lists {
		for _, a := range list {
			key := a.Fingerprint
			if key == "" {
				key = labelKey(a.Labels)
			}
			if i, ok := idx[key]; ok {
				if a.StartsAt.Before(out[i].StartsAt) {
					out[i].StartsAt = a.StartsAt
				}
				continue
			}
			idx[key] = len(out)
			out = append(out, a)
		}
	}
	return out
}

func labelKey(l map[string]string) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(l[k])
		b.WriteByte(0)
	}
	return b.String()
}
