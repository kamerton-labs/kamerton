// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/live"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

// The system as it is now, for the map: where the failure is, what it drags along, the numbers.

// Service states on the map.
const (
	stateSource   = "source"   // the cause of an incident, with a critical alert
	stateWarning  = "warning"  // the cause of an incident with warnings only
	stateAffected = "affected" // suffers from another service's failure
	stateOK       = "ok"
)

type nowAlert struct {
	Name     string    `json:"name"`
	Title    string    `json:"title"`
	Severity string    `json:"severity"`
	Value    string    `json:"value,omitempty"`
	StartsAt time.Time `json:"startsAt"`
}

type nowService struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Critical  bool     `json:"critical,omitempty"`
	DependsOn []string `json:"dependsOn"`
	State     string   `json:"state"`
	// Because are the services whose failure this one suffers from (affected only).
	Because []string   `json:"because,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
	Alerts  []nowAlert `json:"alerts"`
	live.Numbers
}

type nowIncident struct {
	ID       string    `json:"id"`
	Roots    []string  `json:"roots"`
	Affected []string  `json:"affected"`
	StartsAt time.Time `json:"startsAt"`
}

// nowEdge is the calls between two services of the system now (names of the system).
type nowEdge struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Requests   *float64 `json:"requests"`
	ErrorRatio *float64 `json:"errorRatio"`
}

type nowResponse struct {
	// Alerting: Alertmanager is set and answered; Metrics: Prometheus is set.
	Alerting  bool          `json:"alerting"`
	Metrics   bool          `json:"metrics"`
	Traces    bool          `json:"traces"`
	Logs      bool          `json:"logs"`
	Message   string        `json:"message,omitempty"`
	Services  []nowService  `json:"services"`
	Incidents []nowIncident `json:"incidents"`
	// Edges are the calls Tempo saw between the services; empty without its metrics generator.
	Edges []nowEdge  `json:"edges"`
	SLO   []live.SLO `json:"slo"`
	At    time.Time  `json:"at"`
}

func (s *Server) systemNow(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	if snap.SD == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_description", "the description has errors; fix it to see the system")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	c := s.clientsFor(ctx, stackOf(snap))
	now := time.Now()
	resp := nowResponse{Services: []nowService{}, Incidents: []nowIncident{}, Edges: []nowEdge{}, SLO: []live.SLO{}, At: now.UTC(),
		Metrics: c.Prom != nil, Traces: c.Set.URL(stack.KindTempo) != "", Logs: c.Set.URL(stack.KindLoki) != ""}

	numbers := map[string]live.Numbers{}
	var edges []live.Edge
	done := make(chan struct{})
	go func() {
		defer close(done)
		if c.Prom != nil {
			numbers = live.NumbersNow(ctx, c.Prom, snap.SD, now)
			resp.SLO = live.SLOs(ctx, c.Prom, snap.SD, now)
			edges = live.EdgesNow(ctx, c.Prom, now)
		}
	}()
	st, configured, err := s.alertState(ctx, snap)
	<-done
	resp.Alerting = configured && err == nil
	if err != nil {
		resp.Message = err.Error()
	}

	sys := snap.Name()
	byService := map[string][]nowAlert{}
	for _, list := range [][]alertView{st.alerts, st.muted} {
		for _, a := range list {
			if a.System != sys || a.Service == "" {
				continue
			}
			byService[a.Service] = append(byService[a.Service], nowAlert{Name: a.Name, Title: a.Title, Severity: a.Severity, Value: a.Value, StartsAt: a.StartsAt})
		}
	}
	state, because, since := map[string]string{}, map[string][]string{}, map[string]time.Time{}
	for _, inc := range st.incidents {
		if inc.System != sys || len(inc.Roots) == 0 {
			continue
		}
		resp.Incidents = append(resp.Incidents, nowIncident{ID: inc.ID, Roots: inc.Roots, Affected: inc.Affected, StartsAt: inc.StartsAt})
		for _, root := range inc.Roots {
			state[root] = stateWarning
			for _, a := range inc.RootAlerts {
				if a.Service == root && a.Severity == string(v1alpha1.SeverityCritical) {
					state[root] = stateSource
				}
			}
			since[root] = inc.StartsAt
		}
		for _, aff := range inc.Affected {
			if _, isRoot := state[aff]; !isRoot {
				state[aff] = stateAffected
				because[aff] = inc.Roots
				since[aff] = inc.StartsAt
			}
		}
	}
	byTelemetry := map[string]string{}
	for i := range snap.SD.Spec.Services {
		byTelemetry[description.TelemetryName(&snap.SD.Spec.Services[i])] = snap.SD.Spec.Services[i].Name
	}
	for _, e := range edges {
		from, to := byTelemetry[e.From], byTelemetry[e.To]
		if from != "" && to != "" && from != to {
			resp.Edges = append(resp.Edges, nowEdge{From: from, To: to, Requests: e.Requests, ErrorRatio: e.ErrorRatio})
		}
	}
	for i := range snap.SD.Spec.Services {
		svc := &snap.SD.Spec.Services[i]
		ns := nowService{Name: svc.Name, Type: string(svc.Type), Critical: svc.Critical, DependsOn: svc.DependsOn, State: stateOK,
			Alerts: byService[svc.Name], Numbers: numbers[svc.Name]}
		if ns.DependsOn == nil {
			ns.DependsOn = []string{}
		}
		if ns.Alerts == nil {
			ns.Alerts = []nowAlert{}
		}
		if v, ok := state[svc.Name]; ok {
			ns.State, ns.Because = v, because[svc.Name]
			t := since[svc.Name]
			ns.Since = &t
		}
		resp.Services = append(resp.Services, ns)
	}
	writeJSON(w, http.StatusOK, resp)
}

// service finds a service of the system by name, answering 404 itself.
func (s *Server) liveService(w http.ResponseWriter, r *http.Request) (*v1alpha1.SystemDescription, *v1alpha1.Service, setClients, bool) {
	snap := s.system(w, r, false)
	if snap == nil {
		return nil, nil, setClients{}, false
	}
	if snap.SD == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_description", "the description has errors; fix it to see the system")
		return nil, nil, setClients{}, false
	}
	name := r.PathValue("service")
	for i := range snap.SD.Spec.Services {
		if snap.SD.Spec.Services[i].Name == name {
			return snap.SD, &snap.SD.Spec.Services[i], s.clientsFor(r.Context(), stackOf(snap)), true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "no such service in the system")
	return nil, nil, setClients{}, false
}

// Ranges the service page offers.
var liveRanges = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

func (s *Server) serviceSeries(w http.ResponseWriter, r *http.Request) {
	sd, svc, c, ok := s.liveService(w, r)
	if !ok {
		return
	}
	if c.Prom == nil {
		writeError(w, http.StatusConflict, "no_prometheus", "the system has no Prometheus")
		return
	}
	rng, ok := liveRanges[r.URL.Query().Get("range")]
	if !ok {
		rng = time.Hour
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, live.ServiceSeries(ctx, c.Prom, sd, svc, time.Now(), rng))
}

// serviceRequests lists requests of the service in a time window: the ones that failed, or the
// ones slower than a duration.
func (s *Server) serviceRequests(w http.ResponseWriter, r *http.Request) {
	_, svc, c, ok := s.liveService(w, r)
	if !ok {
		return
	}
	base := c.Set.URL(stack.KindTempo)
	if base == "" {
		writeError(w, http.StatusConflict, "no_tempo", "the system has no Tempo")
		return
	}
	q := r.URL.Query()
	end := time.Now()
	start := end.Add(-time.Hour)
	if v, err := strconv.ParseInt(q.Get("start"), 10, 64); err == nil {
		start = time.Unix(v, 0)
	}
	if v, err := strconv.ParseInt(q.Get("end"), 10, 64); err == nil {
		end = time.Unix(v, 0)
	}
	if !end.After(start) || end.Sub(start) > 7*24*time.Hour {
		writeError(w, http.StatusBadRequest, "bad_range", "the window is empty or longer than 7 days")
		return
	}
	cond := "resource.service.name=" + live.TraceQLString(description.TelemetryName(svc))
	if op := q.Get("operation"); op != "" {
		cond += " && name=" + live.TraceQLString(op)
	}
	if q.Get("kind") == "slow" {
		over, err := strconv.ParseFloat(q.Get("over"), 64)
		if err != nil || over <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "slow requests need over, a duration in milliseconds")
			return
		}
		cond += " && kind=server && duration>=" + strconv.FormatFloat(over, 'f', 0, 64) + "ms"
	} else {
		cond += " && status=error"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	slow := q.Get("kind") == "slow"
	limit := 20
	if slow {
		// Tempo answers the first it finds, not the slowest: take more and keep the slowest.
		limit = 100
	}
	hits, err := live.Search(ctx, base, "{"+cond+"} | select(name)", start, end, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tempo_error", err.Error())
		return
	}
	if slow {
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].SpanMs > hits[j].SpanMs })
		if len(hits) > 20 {
			hits = hits[:20]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": hits})
}

// systemCalls are the calls from one service to another over a range, and the ones that failed.
func (s *Server) systemCalls(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	if snap.SD == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_description", "the description has errors; fix it to see the system")
		return
	}
	q := r.URL.Query()
	var from, to *v1alpha1.Service
	for i := range snap.SD.Spec.Services {
		svc := &snap.SD.Spec.Services[i]
		switch svc.Name {
		case q.Get("from"):
			from = svc
		case q.Get("to"):
			to = svc
		}
	}
	if from == nil || to == nil {
		writeError(w, http.StatusNotFound, "not_found", "no such services in the system")
		return
	}
	c := s.clientsFor(r.Context(), stackOf(snap))
	if c.Prom == nil {
		writeError(w, http.StatusConflict, "no_prometheus", "the system has no Prometheus")
		return
	}
	rng, ok := liveRanges[q.Get("range")]
	if !ok {
		rng = time.Hour
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	client, server := description.TelemetryName(from), description.TelemetryName(to)
	end := time.Now()
	resp := map[string]any{"calls": live.Calls(ctx, c.Prom, client, server, end, rng), "failed": []live.Hit{}}
	if base := c.Set.URL(stack.KindTempo); base != "" {
		hits, err := live.Search(ctx, base, live.FailedCallsQuery(client, server)+" | select(name)", end.Add(-rng), end, 20)
		if err != nil {
			resp["failedError"] = err.Error()
		} else {
			resp["failed"] = hits
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// traceView is one request: its spans, the lines written while it ran, and which service of
// the system each name of the telemetry is.
type traceView struct {
	*live.Trace
	Logs      []live.Log        `json:"logs"`
	LogsError string            `json:"logsError,omitempty"`
	Services  map[string]string `json:"services"`
}

func (s *Server) systemTrace(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	c := s.clientsFor(r.Context(), stackOf(snap))
	base := c.Set.URL(stack.KindTempo)
	if base == "" {
		writeError(w, http.StatusConflict, "no_tempo", "the system has no Tempo")
		return
	}
	id := r.PathValue("trace")
	if !live.ValidTraceID(id) {
		writeError(w, http.StatusBadRequest, "bad_request", "not a trace ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	t, err := live.GetTrace(ctx, base, id)
	if errors.Is(err, live.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Tempo does not have this request (any more)")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "tempo_error", err.Error())
		return
	}
	v := traceView{Trace: t, Logs: []live.Log{}, Services: map[string]string{}}
	label := ""
	if snap.SD != nil {
		for i := range snap.SD.Spec.Services {
			svc := &snap.SD.Spec.Services[i]
			v.Services[description.TelemetryName(svc)] = svc.Name
		}
		if tel := snap.SD.Spec.Telemetry; tel != nil && tel.Spans != nil {
			label = tel.Spans.ServiceLabel
		}
	}
	if loki := c.Set.URL(stack.KindLoki); loki != "" {
		end := t.Start.Add(time.Duration(t.DurationMs*float64(time.Millisecond)) + 2*time.Minute)
		logs, err := live.TraceLogs(ctx, loki, label, id, t.Start.Add(-time.Minute), end)
		if err != nil {
			v.LogsError = err.Error()
		} else {
			v.Logs = logs
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// Changes of a service's pods, as the page shows them.
type changeView struct {
	Kind    string    `json:"kind"` // rollout, scaling, restart, warning
	At      time.Time `json:"at,omitzero"`
	Subject string    `json:"subject"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Count   int       `json:"count,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Text    string    `json:"text,omitempty"`
}

func (s *Server) serviceChanges(w http.ResponseWriter, r *http.Request) {
	sd, svc, c, ok := s.liveService(w, r)
	if !ok {
		return
	}
	if s.opts.Workload == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "changes": []any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ch, err := s.opts.Workload.Workload(ctx, sd, svc, c.Prom, time.Now())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "error": err.Error(), "changes": []any{}})
		return
	}
	out := []changeView{}
	for _, x := range ch.Rollouts {
		out = append(out, changeView{Kind: "rollout", At: x.At, Subject: x.Workload, From: x.OldImage, To: x.NewImage})
	}
	for _, x := range ch.Scalings {
		out = append(out, changeView{Kind: "scaling", At: x.At, Subject: x.Workload, From: strconv.Itoa(x.From), To: strconv.Itoa(x.To)})
	}
	for _, x := range ch.Restarts {
		reason := x.Reason
		if x.CrashLoop {
			reason = "CrashLoopBackOff"
		}
		out = append(out, changeView{Kind: "restart", Subject: x.Pod + "/" + x.Container, Count: x.Count, Reason: reason})
	}
	for _, x := range ch.Warnings {
		out = append(out, changeView{Kind: "warning", Subject: x.Reason, Count: int(x.Count), Text: x.Message})
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "changes": out, "pods": ch.Pods, "notReady": ch.NotReady, "noPods": ch.NoPods})
}
