// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package server is the HTTP API of the core and serves the embedded UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/backtest"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/discovery"
	"github.com/kamerton-labs/kamerton/internal/email"
	"github.com/kamerton-labs/kamerton/internal/followup"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/history"
	"github.com/kamerton-labs/kamerton/internal/noise"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/schema"
	"github.com/kamerton-labs/kamerton/internal/stack"
	"github.com/kamerton-labs/kamerton/internal/telegram"
	"github.com/kamerton-labs/kamerton/internal/triage"
	"github.com/kamerton-labs/kamerton/internal/version"
)

const maxBody = 1 << 20

type Options struct {
	// UIBuilt is false when only the placeholder UI exists.
	UI      fs.FS
	UIBuilt bool
	// Prometheus and Alertmanager use the install-time addresses; nil when not configured.
	Prometheus   prometheus.Querier
	Alertmanager alertmanager.Reader
	// Sets nil means only the built-in set, kept in memory.
	Sets *stack.Sets
	// Discoverer is nil when unavailable; DiscoveryUnavailable says why.
	Discoverer           *stack.Discoverer
	DiscoveryUnavailable string
	CheckHTTP            *http.Client
	NewPrometheus        func(url string) (prometheus.Querier, error)
	NewAlertmanager      func(url string) alertmanager.Reader
	Generator            generator.Options
	Logger               *slog.Logger
	// Descriptions defaults to an in-memory store that cannot be saved.
	Descriptions descstore.Store
	// Telegram is nil when Telegram setup is unavailable (no cluster).
	Telegram            *telegram.Service
	TelegramUnavailable string
	// Email is nil when email setup is unavailable (no cluster).
	Email            *email.Service
	EmailUnavailable string
	Namespace        string
	// Auth enables login; nil leaves the UI and API open (see NewAuth).
	Auth *Auth
	// FollowUp is nil when unavailable. WebhookToken is the bearer token Alertmanager must send.
	FollowUp            *followup.Service
	WebhookToken        TokenSource
	FollowUpUnavailable string
	// Feedback keeps "useful / noise" verdicts; nil: in memory.
	Feedback *noise.Feedback
	// History keeps incident records and notes; nil: in memory.
	History *history.Service
}

type Server struct {
	opts Options
	log  *slog.Logger

	memory *descstore.Memory

	sets    *stack.Sets
	checker *stack.Checker

	clientsMu sync.Mutex
	proms     map[string]prometheus.Querier
	ams       map[string]alertmanager.Reader

	opMu sync.Mutex
	op   *stack.Operator
	opAt time.Time

	setsMu    sync.Mutex
	setsCache []stack.Resolved
	setsAt    time.Time

	hookMu  sync.Mutex
	hookTok string
	hookAt  time.Time

	feedback *noise.Feedback
	history  *history.Service
}

func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{opts: opts, log: opts.Logger, proms: map[string]prometheus.Querier{}, ams: map[string]alertmanager.Reader{}}
	if opts.Descriptions == nil {
		s.memory = &descstore.Memory{}
		s.opts.Descriptions = s.memory
	}
	s.sets = opts.Sets
	if s.sets == nil {
		s.sets = &stack.Sets{Store: &stack.MemoryStateStore{}}
	}
	if s.sets.Store == nil {
		s.sets.Store = &stack.MemoryStateStore{}
	}
	s.checker = &stack.Checker{HTTP: opts.CheckHTTP}
	s.feedback = opts.Feedback
	if s.feedback == nil {
		s.feedback = &noise.Feedback{Logger: s.log}
	}
	if s.feedback.Descriptions == nil {
		s.feedback.Descriptions = s.opts.Descriptions
	}
	s.history = opts.History
	if s.history == nil {
		s.history = &history.Service{Logger: s.log}
	}
	if s.history.Descriptions == nil {
		s.history.Descriptions = s.opts.Descriptions
	}
	if f := s.opts.FollowUp; f != nil {
		if f.History == nil {
			f.History = s.history
		}
		if s.history.OnClose == nil {
			s.history.OnClose = f.RecordsClosed
		}
		if f.Descriptions == nil {
			f.Descriptions = s.opts.Descriptions
		}
		if f.Alerts == nil {
			f.Alerts = s.systemAlertsWithMuted
		}
		if f.Sources == nil {
			f.Sources = s.evidenceSources
		}
	}
	return s
}

// SetDescription has no effect when a file or cluster store is configured.
func (s *Server) SetDescription(sd *v1alpha1.SystemDescription, raw []byte, source string) {
	if s.memory != nil {
		s.memory.Set(sd, raw, source)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/version", s.version)
	mux.HandleFunc("GET /api/v1/schema", s.schema)
	mux.HandleFunc("GET /api/v1/stack", s.stackStatus)

	mux.HandleFunc("GET /api/v1/stacks", s.listStacks)
	mux.HandleFunc("POST /api/v1/stacks", s.createStack)
	mux.HandleFunc("POST /api/v1/stacks/check", s.checkStackAddress)
	mux.HandleFunc("GET /api/v1/stacks/discover", s.discoverStacks)
	mux.HandleFunc("PUT /api/v1/stacks/{id}", s.updateStack)
	mux.HandleFunc("DELETE /api/v1/stacks/{id}", s.deleteStack)
	mux.HandleFunc("PUT /api/v1/stacks/{id}/programs/{kind}", s.setStackProgram)
	mux.HandleFunc("DELETE /api/v1/stacks/{id}/programs/{kind}", s.resetStackProgram)
	mux.HandleFunc("GET /api/v1/settings", s.settings)

	mux.HandleFunc("GET /api/auth/session", s.authSession)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/logout", s.authLogout)

	mux.HandleFunc("GET /api/v1/systems", s.listSystems)
	mux.HandleFunc("POST /api/v1/systems", s.createSystem)
	mux.HandleFunc("GET /api/v1/systems/{id}", s.getSystem)
	mux.HandleFunc("PUT /api/v1/systems/{id}", s.putSystem)
	mux.HandleFunc("DELETE /api/v1/systems/{id}", s.deleteSystem)
	mux.HandleFunc("POST /api/v1/systems/{id}/validate", s.validateSystem)
	mux.HandleFunc("POST /api/v1/systems/{id}/rules/preview", s.previewSystemRules)
	mux.HandleFunc("GET /api/v1/systems/{id}/rules/state", s.ruleStates)
	mux.HandleFunc("GET /api/v1/systems/{id}/discovery", s.discoverSystem)
	mux.HandleFunc("POST /api/v1/systems/{id}/backtest", s.backtestSystem)
	mux.HandleFunc("GET /api/v1/systems/{id}/runbooks/{service}", s.runbookSystem)
	mux.HandleFunc("GET /api/v1/systems/{id}/alerts", s.alertsSystem)
	mux.HandleFunc("GET /api/v1/systems/{id}/noise", s.noiseSystem)
	mux.HandleFunc("POST /api/v1/systems/{id}/noise/feedback", s.noiseFeedback)
	mux.HandleFunc("GET /api/v1/systems/{id}/telegram", s.telegramStatusSystem)
	mux.HandleFunc("PUT /api/v1/systems/{id}/telegram/routing", s.telegramRoutingSystem)

	// Single-description endpoints operate on the default system. Links in
	// notifications already sent rely on the runbook and alert endpoints.
	mux.HandleFunc("GET /api/v1/description", s.getDescription)
	mux.HandleFunc("PUT /api/v1/description", s.putDescription)
	mux.HandleFunc("POST /api/v1/description/validate", s.validate)
	mux.HandleFunc("POST /api/v1/rules/preview", s.previewRules)
	mux.HandleFunc("GET /api/v1/discovery/services", s.discover)
	mux.HandleFunc("POST /api/v1/backtest", s.backtest)
	mux.HandleFunc("GET /api/v1/alerts", s.alerts)
	mux.HandleFunc("GET /api/v1/runbooks/{service}", s.runbook)
	mux.HandleFunc("GET /api/v1/incidents/history", s.incidentHistory)
	mux.HandleFunc("PUT /api/v1/incidents/history/{id}/note", s.incidentNote)

	// Alertmanager webhook: bearer token instead of a session (hooks.go).
	mux.HandleFunc("POST "+AlertmanagerHookPath, s.alertmanagerHook)

	mux.HandleFunc("GET /api/v1/telegram", s.telegramStatus)
	mux.HandleFunc("PUT /api/v1/telegram/token", s.telegramSetToken)
	mux.HandleFunc("DELETE /api/v1/telegram/token", s.telegramDeleteToken)
	mux.HandleFunc("POST /api/v1/telegram/chats", s.telegramAddChat)
	mux.HandleFunc("DELETE /api/v1/telegram/chats/{id}", s.telegramDeleteChat)
	mux.HandleFunc("POST /api/v1/telegram/chats/{id}/test", s.telegramTest)
	mux.HandleFunc("PUT /api/v1/telegram/routing", s.telegramRouting)

	mux.HandleFunc("GET /api/v1/email", s.emailStatus)
	mux.HandleFunc("PUT /api/v1/email/server", s.emailSetServer)
	mux.HandleFunc("DELETE /api/v1/email/server", s.emailDeleteServer)
	mux.HandleFunc("POST /api/v1/email/recipients", s.emailAddRecipient)
	mux.HandleFunc("PUT /api/v1/email/recipients/{id}", s.emailUpdateRecipient)
	mux.HandleFunc("DELETE /api/v1/email/recipients/{id}", s.emailDeleteRecipient)
	mux.HandleFunc("POST /api/v1/email/recipients/{id}/test", s.emailTest)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint")
	})
	mux.Handle("/", s.ui())
	return logRequests(s.log, securityHeaders(s.protect(mux)))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": version.Version, "commit": version.Commit, "apiVersion": v1alpha1.APIVersion()})
}

func (s *Server) schema(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/schema+json")
	_, _ = w.Write(schema.JSON())
}

const starterDescription = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: my-system
spec:
  defaults:
    rateWindow: 5m
  services:
    - name: api
      type: http
      critical: true
`

func (s *Server) getDescription(w http.ResponseWriter, r *http.Request) {
	mode := s.opts.Descriptions.Mode()
	writable := mode != descstore.ModeNone
	snap, err := descstore.Default(r.Context(), s.opts.Descriptions)
	switch {
	case errors.Is(err, descstore.ErrNoDescription):
		writeJSON(w, http.StatusOK, map[string]any{"yaml": starterDescription, "source": "template", "isTemplate": true, "mode": mode, "writable": writable})
	case err != nil:
		writeError(w, http.StatusBadGateway, "description_unavailable", err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"yaml": string(snap.Raw), "source": snap.Source, "isTemplate": false, "mode": mode, "writable": writable, "note": snap.Note, "id": snap.ID})
	}
}

// putDescription replaces the system with the document's name, or creates a new one.
func (s *Server) putDescription(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := description.Validate(body)
	if !res.Valid {
		writeInvalid(w, res)
		return
	}
	id := res.Document.Name
	var snap *descstore.Snapshot
	var err error
	stored, gerr := s.opts.Descriptions.Get(ctx, id)
	if gerr != nil {
		stored = nil
	}
	if !s.allowSave(ctx, w, body, stored) {
		return
	}
	if stored != nil {
		snap, res, err = s.opts.Descriptions.Save(ctx, id, body)
	} else {
		snap, res, err = s.opts.Descriptions.Create(ctx, body)
	}
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.log.Info("description saved", "source", snap.Source, "by", "api")
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "source": snap.Source, "id": snap.ID, "mode": s.opts.Descriptions.Mode(), "warnings": res.Warnings})
}

func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	res := description.Validate(body)
	s.checkDraftSecrets(r.Context(), &res, s.storedByName(r.Context(), res.Document))
	writeJSON(w, http.StatusOK, res)
}

type previewResponse struct {
	description.Result
	RuleCount int    `json:"ruleCount"`
	Rules     string `json:"rules"`
}

// preview generates the rules of a draft that would replace stored (nil:
// the system with the draft's name, if any).
func (s *Server) preview(w http.ResponseWriter, r *http.Request, body []byte, stored *descstore.Snapshot) {
	res := description.Validate(body)
	if stored == nil {
		stored = s.storedByName(r.Context(), res.Document)
	}
	s.checkDraftSecrets(r.Context(), &res, stored)
	out := previewResponse{Result: res}
	if res.Valid {
		b, err := generator.Generate(res.Document, s.opts.Generator)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "generation_failed", err.Error())
			return
		}
		out.RuleCount, out.Rules = b.RuleCount, string(b.RulesYAML)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) previewRules(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.preview(w, r, body, nil)
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	snap, _ := descstore.Default(r.Context(), s.opts.Descriptions)
	s.discoverFor(w, r, snap)
}

type discoveredView struct {
	discovery.Service
	InSystems []string `json:"inSystems"`
}

func (s *Server) discoverFor(w http.ResponseWriter, r *http.Request, snap *descstore.Snapshot) {
	var tel description.ResolvedTelemetry
	described := map[string]bool{}
	if snap != nil && snap.SD != nil {
		tel = description.ResolveTelemetry(snap.SD.Spec.Telemetry)
		for i := range snap.SD.Spec.Services {
			described[description.TelemetryName(&snap.SD.Spec.Services[i])] = true
		}
	} else {
		tel = description.ResolveTelemetry(nil)
	}
	others := map[string][]string{}
	if list, err := s.opts.Descriptions.List(r.Context()); err == nil {
		for _, o := range list {
			if o.SD == nil || (snap != nil && o.ID == snap.ID) {
				continue
			}
			for i := range o.SD.Spec.Services {
				n := description.TelemetryName(&o.SD.Spec.Services[i])
				others[n] = append(others[n], o.ID)
			}
		}
	}
	d := &discovery.Discoverer{Prom: s.clientsFor(r.Context(), stackOf(snap)).Prom, Telemetry: tel}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	services, err := d.Discover(ctx, described)
	switch {
	case errors.Is(err, discovery.ErrNotConfigured):
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "services": []any{}, "message": "Prometheus URL is not configured; nothing can be discovered"})
	case err != nil:
		writeError(w, http.StatusBadGateway, "prometheus_error", err.Error())
	default:
		out := make([]discoveredView, 0, len(services))
		for _, sv := range services {
			out = append(out, discoveredView{Service: sv, InSystems: append([]string{}, others[sv.Name]...)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "services": out})
	}
}

type backtestRequest struct {
	Expr  string `json:"expr"`
	For   string `json:"for"`
	Range string `json:"range"`
	Step  string `json:"step"`
}

func (s *Server) backtest(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req backtestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.runBacktest(w, r, s.clientsFor(r.Context(), r.URL.Query().Get("set")).Prom, req, nil)
}

func (s *Server) runBacktest(w http.ResponseWriter, r *http.Request, prom prometheus.Querier, req backtestRequest, extra map[string]any) {
	forD, err1 := rules.ParseDuration(req.For, 0)
	rangeD, err2 := rules.ParseDuration(req.Range, 7*24*time.Hour)
	step, err3 := rules.ParseDuration(req.Step, time.Minute)
	if err := errors.Join(err1, err2, err3); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	end := time.Now().Truncate(step)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := backtest.Run(ctx, prom, backtest.Request{Expr: req.Expr, For: forD, Start: end.Add(-rangeD), End: end, Step: step})
	switch {
	case errors.Is(err, backtest.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "not_configured", "Prometheus URL is not configured; backtest is unavailable")
	case err != nil:
		writeError(w, http.StatusBadGateway, "backtest_failed", err.Error())
	case extra != nil:
		writeJSON(w, http.StatusOK, map[string]any{"firings": res.Firings, "series": res.Series, "episodes": res.Episodes, "start": res.Start, "end": res.End, "step": res.Step, "rule": extra})
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

type alertView struct {
	Name     string    `json:"name"`
	System   string    `json:"system"`
	Service  string    `json:"service"`
	Severity string    `json:"severity"`
	StartsAt time.Time `json:"startsAt"`
	Summary  string    `json:"summary"`
	// Title, Value and Threshold are the plain-words annotations of the rule library.
	Title       string            `json:"title,omitempty"`
	Value       string            `json:"value,omitempty"`
	Threshold   string            `json:"threshold,omitempty"`
	Text        string            `json:"text,omitempty"`
	Action      string            `json:"action,omitempty"`
	Labels      map[string]string `json:"labels"`
	Triage      *triage.Finding   `json:"triage,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	// Muted is true for alerts an inhibit rule mutes (listed under "muted").
	Muted    bool   `json:"muted,omitempty"`
	Incident string `json:"incident,omitempty"`
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) { s.alertsFor(w, r, nil) }

// activeAlerts returns active alerts, inhibited ones included, of one system or,
// with snap nil, of all systems without duplicates. partial lists the
// Alertmanagers that failed while others answered.
func (s *Server) activeAlerts(ctx context.Context, snap *descstore.Snapshot, list []*descstore.Snapshot) (raw []alertmanager.Alert, configured bool, partial []string, err error) {
	filter := map[string]string{rules.LabelManaged: "true"}
	var sources []amSource
	if snap != nil {
		filter[generator.LabelSystem] = snap.Name()
		c := s.clientsFor(ctx, stackOf(snap))
		if c.AM != nil {
			sources = []amSource{{url: c.Set.URL(stack.KindAlertmanager), am: c.AM}}
		}
	} else {
		sources = s.alertmanagersInUse(ctx, list)
	}
	if len(sources) == 0 {
		return nil, false, nil, nil
	}
	lists, errs := fetchAlerts(ctx, sources, filter)
	var ok [][]alertmanager.Alert
	var failed []error
	for i := range sources {
		if errs[i] != nil {
			failed = append(failed, errs[i])
			partial = append(partial, sources[i].url)
			continue
		}
		ok = append(ok, lists[i])
	}
	if len(ok) == 0 {
		return nil, true, nil, errors.Join(failed...)
	}
	return mergeAlerts(ok...), true, partial, nil
}

// alertsFor lists active alerts: "alerts" are delivered ones (a shape clients
// rely on), "muted" are inhibited ones and "incidents" groups them by cause.
func (s *Server) alertsFor(w http.ResponseWriter, r *http.Request, snap *descstore.Snapshot) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	list, _ := s.opts.Descriptions.List(ctx)
	raw, configured, partial, err := s.activeAlerts(ctx, snap, list)
	if !configured {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "alerts": []any{}, "muted": []any{}, "incidents": []any{}, "message": "Alertmanager URL is not configured"})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "alertmanager_error", err.Error())
		return
	}
	graphs := map[string]*triage.Graph{}
	for _, snap := range list {
		if snap.SD != nil {
			graphs[snap.SD.Name] = triage.NewGraph(&snap.SD.Spec)
		}
	}
	unhealthy := map[string]map[string]bool{}
	bySystem := map[string][]triage.Alert{}
	var systems []string
	for _, a := range raw {
		sys := a.Labels[generator.LabelSystem]
		if unhealthy[sys] == nil {
			unhealthy[sys] = map[string]bool{}
			systems = append(systems, sys)
		}
		unhealthy[sys][a.Labels[rules.LabelService]] = true
		bySystem[sys] = append(bySystem[sys], triageAlert(a))
	}
	sort.Strings(systems)
	incidents := []incidentView{}
	incidentOf := map[string]string{} // system + service -> incident ID; service "" is the system itself
	for _, sys := range systems {
		g := graphs[sys]
		if g == nil {
			g = triage.NewGraph(&v1alpha1.SystemDescriptionSpec{})
		}
		for _, inc := range g.Incidents(sys, bySystem[sys]) {
			if len(inc.Roots) == 0 {
				incidentOf[sys+"\x00"] = inc.ID // its affected services keep their other alerts elsewhere
			} else {
				for _, svc := range append(append([]string{}, inc.Roots...), inc.Affected...) {
					incidentOf[sys+"\x00"+svc] = inc.ID
				}
			}
			v := incidentView{Incident: inc}
			if s.opts.FollowUp != nil {
				v.Evidence = s.opts.FollowUp.Evidence(inc.ID)
			}
			incidents = append(incidents, v)
		}
	}
	s.attachHistory(ctx, list, snap, partial, bySystem, incidents)
	out := make([]alertView, 0, len(raw))
	muted := []alertView{}
	for _, a := range raw {
		v := alertView{
			Name: a.Labels["alertname"], System: a.Labels[generator.LabelSystem], Service: a.Labels[rules.LabelService], Severity: a.Labels[rules.LabelSeverity],
			StartsAt: a.StartsAt, Summary: a.Annotations["summary"], Labels: a.Labels,
			Title: a.Annotations[rules.AnnotationTitle], Value: a.Annotations[rules.AnnotationValue], Threshold: a.Annotations[rules.AnnotationThreshold],
			Fingerprint: a.Fingerprint, Muted: a.Inhibited(),
		}
		since := a.StartsAt.UTC().Format("15:04 02.01 UTC")
		fill := func(s string) string { return strings.ReplaceAll(s, "{since}", since) }
		if t := a.Annotations[generator.AnnotationMessageTitle]; t != "" {
			v.Title = fill(t)
		}
		v.Text, v.Action = fill(a.Annotations[generator.AnnotationMessageText]), fill(a.Annotations[generator.AnnotationMessageAction])
		if g := graphs[v.System]; g != nil && v.Service != "" {
			f := g.ProbableSource(v.Service, unhealthy[v.System])
			v.Triage = &f
		}
		v.Incident = incidentOf[v.System+"\x00"+v.Service]
		if id, ok := incidentOf[v.System+"\x00"]; ok && v.Name == rules.AlertServiceTelemetryMissing {
			v.Incident = id
		}
		if v.Muted {
			muted = append(muted, v)
			continue
		}
		out = append(out, v)
	}
	resp := map[string]any{"configured": true, "alerts": out, "muted": muted, "incidents": incidents}
	if len(partial) > 0 {
		resp["unreachable"] = partial
	}
	writeJSON(w, http.StatusOK, resp)
}

// incidentView adds the evidence the follow-up collected for the incident (in
// this replica's memory) to the fields of triage.Incident.
type incidentView struct {
	triage.Incident
	Evidence *generator.EvidenceSummary `json:"evidence,omitempty"`
	// HistoryID is the history record of this incident; Previous the earlier
	// incident most like it.
	HistoryID string            `json:"historyId,omitempty"`
	Previous  *history.Previous `json:"previous,omitempty"`
}

func triageAlert(a alertmanager.Alert) triage.Alert {
	title := a.Annotations[rules.AnnotationTitle]
	if t := a.Annotations[generator.AnnotationMessageTitle]; t != "" {
		title = strings.ReplaceAll(t, "{since}", a.StartsAt.UTC().Format("15:04 02.01 UTC"))
	}
	if title == "" {
		title = a.Annotations["summary"]
	}
	return triage.Alert{
		Fingerprint: a.Fingerprint, Name: a.Labels["alertname"], Service: a.Labels[rules.LabelService],
		Severity: a.Labels[rules.LabelSeverity], StartsAt: a.StartsAt,
		Title: title, Value: a.Annotations[rules.AnnotationValue], Muted: a.Inhibited(),
	}
}

const uiNotBuilt = `<!doctype html><html lang="en"><meta charset="utf-8"><title>Kamerton</title>
<body style="font-family:system-ui;margin:3rem;max-width:40rem">
<h1>Kamerton</h1>
<p>The UI is not built. Run <code>make ui</code> and rebuild the binary. The API is available under <code>/api/v1/</code>.</p></body></html>`

func (s *Server) ui() http.Handler {
	if !s.opts.UIBuilt || s.opts.UI == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, uiNotBuilt)
		})
	}
	files := http.FileServerFS(s.opts.UI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(s.opts.UI, p); err != nil {
			// SPA fallback: client-side routes are served index.html.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		} else if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is larger than 1 MiB")
		return nil, false
	case err != nil:
		writeError(w, http.StatusBadRequest, "bad_request", "cannot read the request body")
		return nil, false
	}
	return body, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": msg, "code": code})
}

func writeInvalid(w http.ResponseWriter, res description.Result) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "description is invalid", "code": "invalid", "valid": false, "errors": res.Errors, "warnings": res.Warnings})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	var inv *descstore.InvalidError
	switch {
	case errors.As(err, &inv):
		writeInvalid(w, inv.Result)
	case errors.Is(err, descstore.ErrNoDescription):
		writeError(w, http.StatusNotFound, "not_found", "no such system")
	case errors.Is(err, descstore.ErrNotWritable):
		writeError(w, http.StatusConflict, "not_writable", err.Error())
	case errors.Is(err, descstore.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, descstore.ErrExists):
		writeError(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, descstore.ErrRename):
		writeError(w, http.StatusConflict, "rename", err.Error())
	default:
		writeError(w, http.StatusBadGateway, "save_failed", err.Error())
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// securityHeaders stops content sniffing and framing by other sites (clickjacking).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start))
		}
	})
}
