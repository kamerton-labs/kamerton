// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package history remembers incidents and what people wrote about them, and
// brings the memory back when the same thing happens again.
package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

const (
	// Note caps, in characters.
	MaxCause = 200
	MaxFix   = 500
	// StaleAfter closes an open record nobody could confirm for this long
	// (Kamerton missed the resolve and Alertmanager was not answering); the
	// end is then the last time it was seen and marked as estimated.
	StaleAfter = 24 * time.Hour
	// lateEnd: a record closed without a resolve notification ends now when
	// it was seen this recently, else when it was last seen.
	lateEnd = 10 * time.Minute
	// AskAfter is the shortest incident that gets the "incident is over" message.
	AskAfter = 2 * time.Minute
	// RecallWindow is the period of Previous.Count30d.
	RecallWindow  = 30 * 24 * time.Hour
	maxExperience = 5
	maxRootAlerts = 5
	maxAffected   = 20
	maxAsked      = 10
	maxTitle      = 120
)

const (
	SourceUI       = "ui"
	SourceTelegram = "telegram"
)

type AlertRef struct {
	Name     string `json:"name"`
	Service  string `json:"service"`
	Title    string `json:"title,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type Note struct {
	Cause     string    `json:"cause,omitempty"`
	Fix       string    `json:"fix,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Source    string    `json:"source"`
}

// Asked is a sent "incident is over" message; a reply to it is the note.
type Asked struct {
	Chat    int64 `json:"chat"`
	Message int64 `json:"message"`
}

// Record is stored as is; View is what the API returns.
type Record struct {
	ID string `json:"id"`
	// Incident is the triage incident ID (system:roots) of the last view;
	// it stays after the record closes.
	Incident     string                     `json:"incident"`
	System       string                     `json:"system"`
	Roots        []string                   `json:"roots"`
	RootAlerts   []AlertRef                 `json:"rootAlerts"`
	Affected     []string                   `json:"affected"`
	StartedAt    time.Time                  `json:"startedAt"`
	EndedAt      time.Time                  `json:"endedAt,omitzero"`
	EndEstimated bool                       `json:"endEstimated,omitempty"`
	Open         bool                       `json:"open"`
	LastSeen     time.Time                  `json:"lastSeen"`
	Evidence     *generator.EvidenceSummary `json:"evidence,omitempty"`
	Note         *Note                      `json:"note,omitempty"`
	// FollowUpTo are the Telegram receivers (names) that got the follow-up.
	FollowUpTo []string `json:"followUpTo,omitempty"`
	AskSent    bool     `json:"askSent,omitempty"`
	Asked      []Asked  `json:"asked,omitempty"`
	// Firing are the root alerts ("alert@service") of the last view; a
	// resolved notification ends the record when it covers all of them.
	Firing    []string    `json:"firing,omitempty"`
	Aftermath []Aftermath `json:"aftermath,omitempty"`
	// Merged are the IDs of records opened for the same incident before
	// triage knew its root.
	Merged []string `json:"merged,omitempty"`
	// Promoted: aftermath that outlived AftermathGrace and became an
	// incident of its own; its follow-up is started by the sweep.
	Promoted bool `json:"promoted,omitempty"`
}

// Aftermath are alerts of a service the incident affected that fire on
// their own while it is open or within AftermathGrace after it ended.
type Aftermath struct {
	Service  string    `json:"service"`
	Alerts   []string  `json:"alerts"`
	Since    time.Time `json:"since"`
	LastSeen time.Time `json:"lastSeen"`
}

func (r Record) end() time.Time {
	if r.EndedAt.IsZero() {
		return r.LastSeen
	}
	return r.EndedAt
}

func (r Record) Duration() time.Duration {
	if r.Open || r.EndedAt.IsZero() {
		return 0
	}
	return r.EndedAt.Sub(r.StartedAt)
}

type View struct {
	ID              string                     `json:"id"`
	System          string                     `json:"system"`
	Roots           []string                   `json:"roots"`
	RootAlerts      []AlertRef                 `json:"rootAlerts"`
	Affected        []string                   `json:"affected"`
	StartedAt       time.Time                  `json:"startedAt"`
	EndedAt         *time.Time                 `json:"endedAt,omitempty"`
	DurationSeconds *int64                     `json:"durationSeconds,omitempty"`
	EndEstimated    bool                       `json:"endEstimated,omitempty"`
	Open            bool                       `json:"open"`
	Evidence        *generator.EvidenceSummary `json:"evidence,omitempty"`
	Note            *Note                      `json:"note,omitempty"`
	Aftermath       []Aftermath                `json:"aftermath,omitempty"`
}

func (r Record) View() View {
	v := View{ID: r.ID, System: r.System, Roots: r.Roots, RootAlerts: r.RootAlerts, Affected: r.Affected,
		StartedAt: r.StartedAt, Open: r.Open, EndEstimated: r.EndEstimated, Evidence: r.Evidence, Note: r.Note, Aftermath: r.Aftermath}
	if v.Roots == nil {
		v.Roots = []string{}
	}
	if v.RootAlerts == nil {
		v.RootAlerts = []AlertRef{}
	}
	if v.Affected == nil {
		v.Affected = []string{}
	}
	if !r.Open && !r.EndedAt.IsZero() {
		end := r.EndedAt
		d := int64(r.Duration() / time.Second)
		v.EndedAt, v.DurationSeconds = &end, &d
	}
	return v
}

// Previous is the earlier incident shown with a live one.
type Previous struct {
	ID              string    `json:"id"`
	StartedAt       time.Time `json:"startedAt"`
	DurationSeconds int64     `json:"durationSeconds"`
	Cause           string    `json:"cause,omitempty"`
	Fix             string    `json:"fix,omitempty"`
	Count30d        int       `json:"count30d"`
	// Noted: cause or fix was written by a person.
	Noted bool `json:"noted"`
}

func (p *Previous) Recall() *generator.Recall {
	if p == nil {
		return nil
	}
	return &generator.Recall{StartedAt: p.StartedAt, Duration: time.Duration(p.DurationSeconds) * time.Second,
		Cause: p.Cause, Fix: p.Fix, Count30d: p.Count30d, Noted: p.Noted}
}

type Experience struct {
	ID              string    `json:"id"`
	StartedAt       time.Time `json:"startedAt"`
	DurationSeconds int64     `json:"durationSeconds"`
	Cause           string    `json:"cause,omitempty"`
	Fix             string    `json:"fix,omitempty"`
}

var (
	ErrNotFound = errors.New("no such incident in the history")
	ErrTooLong  = errors.New("note is too long")
	idPattern   = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func ValidID(id string) bool { return idPattern.MatchString(id) }

func recordID(system, incident string, start time.Time) string {
	h := sha256.Sum256([]byte(system + "\x00" + incident + "@" + strconv.FormatInt(start.Unix(), 10)))
	return hex.EncodeToString(h[:8])
}

type Service struct {
	// Store nil: memory, for file and local mode.
	Store        Store
	Descriptions descstore.Store
	// Receiver reports whether a chat gets the alerts of sd through
	// Kamerton's bot; nil accepts no Telegram reply.
	Receiver func(sd *v1alpha1.SystemDescription, chatID int64) bool
	// OnClose gets the records an Observe closed, once across replicas.
	OnClose func(ctx context.Context, closed []Record)
	Now     func() time.Time
	Logger  *slog.Logger

	memory MemoryStore
}

func (s *Service) store() Store {
	if s.Store != nil {
		return s.Store
	}
	return &s.memory
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Snapshot reads the state without writing.
func (s *Service) Snapshot(ctx context.Context) (*State, error) {
	return s.store().Update(ctx, func(*State) error { return ErrNoChange })
}

// Observation is what is known about a system's incidents at one moment.
type Observation struct {
	System    string
	Incidents []triage.Incident
	// Complete: Alertmanager answered, so open records of the system without
	// a live incident have ended.
	Complete bool
	// Ended maps "alertname\x00service" to the end of an alert from a
	// resolved notification.
	Ended map[string]time.Time
}

func EndKey(alert, service string) string { return alert + "\x00" + service }

// Observe opens, updates and closes the records of the observed systems in
// one write and returns the state after it.
func (s *Service) Observe(ctx context.Context, obs ...Observation) (*State, error) {
	now := s.now().UTC()
	var closed []Record
	st, err := s.store().Update(ctx, func(st *State) error {
		closed = nil
		changed := false
		for _, o := range obs {
			c, ch := st.observe(o, now)
			closed, changed = append(closed, c...), changed || ch
		}
		if !changed {
			return ErrNoChange
		}
		st.prune(now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(closed) > 0 && s.OnClose != nil {
		s.OnClose(ctx, closed)
	}
	return st, nil
}

func (st *State) find(id string) int {
	for i, r := range st.Records {
		if r.ID == id {
			return i
		}
	}
	return -1
}

// OpenRecord returns the open record of a live incident.
func (st *State) OpenRecord(system, incident string) *Record {
	if st == nil {
		return nil
	}
	if i := st.openFor(system, incident); i >= 0 {
		r := st.Records[i]
		return &r
	}
	return nil
}

// OpenSystems returns the systems with open records.
func (st *State) OpenSystems() []string {
	var out []string
	for _, r := range st.Records {
		if r.Open && !contains(out, r.System) {
			out = append(out, r.System)
		}
	}
	sort.Strings(out)
	return out
}

// update changes the record with the given ID; ErrNotFound when there is none.
func (s *Service) update(ctx context.Context, id string, mutate func(*Record) error) (Record, error) {
	var out Record
	_, err := s.store().Update(ctx, func(st *State) error {
		i := st.find(id)
		if i < 0 {
			return ErrNotFound
		}
		if err := mutate(&st.Records[i]); err != nil {
			return err
		}
		out = st.Records[i]
		return nil
	})
	return out, err
}

// updateOpen changes the open record of a live incident; no record: nothing.
func (s *Service) updateOpen(ctx context.Context, system, incident string, mutate func(*Record)) error {
	_, err := s.store().Update(ctx, func(st *State) error {
		i := st.openFor(system, incident)
		if i < 0 {
			return ErrNoChange
		}
		mutate(&st.Records[i])
		return nil
	})
	return err
}

// AttachEvidence keeps the evidence of a live incident with its record.
func (s *Service) AttachEvidence(ctx context.Context, system, incident string, sum generator.EvidenceSummary) error {
	ev := trimEvidence(sum)
	return s.updateOpen(ctx, system, incident, func(r *Record) { r.Evidence = &ev })
}

// FollowUpSent remembers the Telegram receivers that got the follow-up of a
// live incident: the "incident is over" message goes to them.
func (s *Service) FollowUpSent(ctx context.Context, system, incident string, receivers []string) error {
	if len(receivers) == 0 {
		return nil
	}
	return s.updateOpen(ctx, system, incident, func(r *Record) {
		for _, n := range receivers {
			if !contains(r.FollowUpTo, n) {
				r.FollowUpTo = append(r.FollowUpTo, n)
			}
		}
	})
}

// RecordAsked keeps the "incident is over" messages; replies to them are notes.
func (s *Service) RecordAsked(ctx context.Context, id string, asked []Asked) error {
	_, err := s.update(ctx, id, func(r *Record) error {
		r.AskSent, r.FollowUpTo = true, nil
		r.Asked = append(r.Asked, asked...)
		if len(r.Asked) > maxAsked {
			r.Asked = r.Asked[len(r.Asked)-maxAsked:]
		}
		return nil
	})
	return err
}

// ClaimAsk marks a closed record as asked before the message is sent, so
// only one replica sends it; false when it was claimed already.
func (s *Service) ClaimAsk(ctx context.Context, id string) bool {
	claimed := false
	_, err := s.store().Update(ctx, func(st *State) error {
		claimed = false
		i := st.find(id)
		if i < 0 || st.Records[i].AskSent {
			return ErrNoChange
		}
		st.Records[i].AskSent, claimed = true, true
		return nil
	})
	return err == nil && claimed
}

// Filter of List; empty fields match everything. Service matches roots and
// affected services.
type Filter struct {
	System, Service string
	Limit           int
}

// List returns records, most recent first.
func (s *Service) List(ctx context.Context, f Filter) ([]Record, error) {
	st, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var out []Record
	for i := len(st.Records) - 1; i >= 0; i-- {
		r := st.Records[i]
		if f.System != "" && r.System != f.System {
			continue
		}
		if f.Service != "" && !contains(r.Roots, f.Service) && !contains(r.Affected, f.Service) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// NoteInput: nil keeps a field, "" clears it.
type NoteInput struct {
	Cause *string `json:"cause"`
	Fix   *string `json:"fix"`
}

// CheckNote trims the fields and checks their lengths.
func CheckNote(in *NoteInput) error {
	for _, f := range []struct {
		v   *string
		max int
	}{{in.Cause, MaxCause}, {in.Fix, MaxFix}} {
		if f.v == nil {
			continue
		}
		*f.v = strings.TrimSpace(*f.v)
		if utf8.RuneCountInString(*f.v) > f.max {
			return ErrTooLong
		}
	}
	return nil
}

// SetNote stores what people wrote; a note with both fields empty is removed.
func (s *Service) SetNote(ctx context.Context, id string, in NoteInput, source string) (Record, error) {
	if err := CheckNote(&in); err != nil {
		return Record{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	return s.update(ctx, id, func(r *Record) error {
		n := Note{}
		if r.Note != nil {
			n = *r.Note
		}
		if in.Cause != nil {
			n.Cause = *in.Cause
		}
		if in.Fix != nil {
			n.Fix = *in.Fix
		}
		n.UpdatedAt, n.Source = now, source
		r.Note = &n
		if n.Cause == "" && n.Fix == "" {
			r.Note = nil
		}
		return nil
	})
}

// Recall finds the earlier incident most like a live one: closed records of
// the same system with its first root, preferring one with the same root
// alert; nil when there is none.
func (st *State) Recall(system string, inc triage.Incident, now time.Time) *Previous {
	if st == nil || len(inc.Roots) == 0 {
		return nil
	}
	root := inc.Roots[0]
	kind := primaryAlert(inc.RootAlerts, root)
	sameKind := func(r Record) bool {
		for _, a := range r.RootAlerts {
			if a.Service == root && a.Name == kind {
				return true
			}
		}
		return false
	}
	var service, kinds []Record
	for _, r := range st.Records {
		if r.Open || r.System != system || !contains(r.Roots, root) {
			continue
		}
		service = append(service, r)
		if kind != "" && sameKind(r) {
			kinds = append(kinds, r)
		}
	}
	pick := service
	if len(kinds) > 0 {
		pick = kinds
	}
	if len(pick) == 0 {
		return nil
	}
	sort.SliceStable(pick, func(i, j int) bool { return pick[i].StartedAt.After(pick[j].StartedAt) })
	last := pick[0]
	p := &Previous{ID: last.ID, StartedAt: last.StartedAt, DurationSeconds: int64(last.Duration() / time.Second)}
	for _, r := range pick {
		if now.Sub(r.StartedAt) <= RecallWindow {
			p.Count30d++
		}
	}
	if last.Evidence != nil {
		p.Cause = last.Evidence.Cause
	}
	if n := last.Note; n != nil {
		p.Noted = true
		if n.Cause != "" {
			p.Cause = n.Cause
		}
		p.Fix = n.Fix
	}
	return p
}

// AftermathOf tells whether a live incident is the tail of an earlier one in
// a state already read; nil-safe for a Service and a State.
func (s *Service) AftermathOf(st *State, system string, inc triage.Incident) *Record {
	if s == nil || st == nil {
		return nil
	}
	return st.AftermathOf(system, inc, s.now())
}

// RecallFrom finds the earlier incident most like inc in a state already read.
func (s *Service) RecallFrom(st *State, system string, inc triage.Incident) *Previous {
	return st.Recall(system, inc, s.now())
}

// Recall reads the state and finds the earlier incident most like inc.
func (s *Service) Recall(ctx context.Context, system string, inc triage.Incident) *Previous {
	st, err := s.Snapshot(ctx)
	if err != nil {
		s.log().Warn("incident history unavailable", "error", err)
		return nil
	}
	return st.Recall(system, inc, s.now())
}

// Experience returns the notes about incidents with the service as a root,
// most recent first.
func (s *Service) Experience(ctx context.Context, system, service string) ([]Experience, error) {
	list, err := s.List(ctx, Filter{System: system})
	if err != nil {
		return nil, err
	}
	out := []Experience{}
	for _, r := range list {
		if r.Note == nil || !contains(r.Roots, service) {
			continue
		}
		e := Experience{ID: r.ID, StartedAt: r.StartedAt, DurationSeconds: int64(r.Duration() / time.Second), Cause: r.Note.Cause, Fix: r.Note.Fix}
		if e.Cause == "" && r.Evidence != nil {
			e.Cause = r.Evidence.Cause
		}
		out = append(out, e)
		if len(out) == maxExperience {
			break
		}
	}
	return out, nil
}

// primaryAlert picks the root's alert that explains others most (a symptom
// source, then a target), the earliest among equals.
func primaryAlert(alerts []triage.Alert, svc string) string {
	rank := func(name string) int {
		switch {
		case rules.IsSymptomSource(name):
			return 2
		case rules.IsSymptomTarget(name):
			return 1
		}
		return 0
	}
	var best *triage.Alert
	for i := range alerts {
		a := &alerts[i]
		if a.Service != svc {
			continue
		}
		if best == nil || rank(a.Name) > rank(best.Name) || (rank(a.Name) == rank(best.Name) && a.StartsAt.Before(best.StartsAt)) {
			best = a
		}
	}
	if best == nil {
		return ""
	}
	return best.Name
}

// Evidence caps keep a record small; a trace link longer than maxURL is
// dropped rather than cut (a cut URL is useless).
const (
	maxCause   = 200
	maxChange  = 160
	maxChanges = 3
	maxLine    = 200
	maxURL     = 1000
	maxNames   = 10
)

func trimEvidence(e generator.EvidenceSummary) generator.EvidenceSummary {
	e.Cause = trim(e.Cause, maxCause)
	if len(e.Changes) > maxChanges {
		e.Changes = e.Changes[:maxChanges]
	}
	changes := make([]string, 0, len(e.Changes))
	for _, c := range e.Changes {
		changes = append(changes, trim(c, maxChange))
	}
	if len(changes) == 0 {
		changes = nil
	}
	e.Changes = changes
	e.Operation = trim(e.Operation, maxChange)
	e.LogLine = trim(e.LogLine, maxLine)
	if len(e.TracesURL) > maxURL {
		e.TracesURL = ""
	}
	if len(e.Explains) > maxNames {
		e.Explains = e.Explains[:maxNames]
	}
	return e
}

func trim(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
