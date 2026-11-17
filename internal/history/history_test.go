// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package history

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

var t0 = time.Date(2026, 11, 16, 4, 18, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newService(c *clock) *Service   { return &Service{Now: c.now} }
func str(s string) *string           { return &s }
func at(minutes int) time.Time       { return t0.Add(time.Duration(minutes) * time.Minute) }
func ended(name, svc string, t time.Time) map[string]time.Time {
	return map[string]time.Time{EndKey(name, svc): t}
}

func incident(start time.Time, roots []string, affected []string, alerts ...triage.Alert) triage.Incident {
	return triage.Incident{ID: "shop:" + strings.Join(roots, "+"), System: "shop", Roots: roots, Affected: affected, RootAlerts: alerts, StartsAt: start}
}

func alert(name, svc string, start time.Time) triage.Alert {
	return triage.Alert{Name: name, Service: svc, Severity: "critical", Title: "title of " + name, StartsAt: start}
}

func observe(t *testing.T, s *Service, o Observation) *State {
	t.Helper()
	st, err := s.Observe(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestLifecycleOpenAndCloseOnResolve(t *testing.T) {
	c := &clock{t: at(1)}
	s := newService(c)
	var closed []Record
	s.OnClose = func(_ context.Context, r []Record) { closed = append(closed, r...) }
	inc := incident(at(0), []string{"payment"}, []string{"checkout"}, alert(rules.AlertNoReadyPods, "payment", at(0)))
	st := observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{inc}, Complete: true})
	if len(st.Records) != 1 {
		t.Fatalf("records %+v", st.Records)
	}
	r := st.Records[0]
	if !r.Open || !ValidID(r.ID) || r.StartedAt != at(0) || r.Affected[0] != "checkout" || r.RootAlerts[0].Title != "title of "+rules.AlertNoReadyPods {
		t.Errorf("open record %+v", r)
	}
	v := r.View()
	if v.EndedAt != nil || v.DurationSeconds != nil || !v.Open {
		t.Errorf("open view %+v", v)
	}
	if got := st.OpenRecord("shop", "shop:payment"); got == nil || got.ID != r.ID {
		t.Errorf("open record of the live incident: %+v", got)
	}

	// The same incident later, with one more affected service: same record.
	c.add(2 * time.Minute)
	inc.Affected = []string{"checkout", "frontend"}
	st = observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{inc}, Complete: true})
	if len(st.Records) != 1 || len(st.Records[0].Affected) != 2 || st.Records[0].ID != r.ID {
		t.Errorf("updated %+v", st.Records)
	}

	// Resolved: Alertmanager no longer lists it, the notification gives the end.
	c.add(10 * time.Minute)
	st = observe(t, s, Observation{System: "shop", Complete: true, Ended: ended(rules.AlertNoReadyPods, "payment", at(9))})
	r = st.Records[0]
	if r.Open || r.EndedAt != at(9) || r.EndEstimated || r.Duration() != 9*time.Minute {
		t.Errorf("closed record %+v", r)
	}
	if v := r.View(); v.EndedAt == nil || *v.DurationSeconds != 540 || v.Open {
		t.Errorf("closed view %+v", v)
	}
	if len(closed) != 1 || closed[0].ID != r.ID {
		t.Errorf("OnClose got %+v", closed)
	}
	// Nothing changes on a repeat.
	observe(t, s, Observation{System: "shop", Complete: true, Ended: ended(rules.AlertNoReadyPods, "payment", at(9))})
	if len(closed) != 1 {
		t.Errorf("closed twice: %+v", closed)
	}
}

func TestResolveWithoutAlertmanager(t *testing.T) {
	c := &clock{t: at(1)}
	s := newService(c)
	inc := incident(at(0), []string{"payment"}, nil, alert(rules.AlertNoReadyPods, "payment", at(0)), alert(rules.AlertErrorRate, "payment", at(0)))
	observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{inc}})
	// One of two root alerts resolved and Alertmanager did not answer: still open.
	st := observe(t, s, Observation{System: "shop", Ended: ended(rules.AlertNoReadyPods, "payment", at(5))})
	if !st.Records[0].Open {
		t.Fatal("closed while one root alert still fires")
	}
	e := ended(rules.AlertNoReadyPods, "payment", at(5))
	e[EndKey(rules.AlertErrorRate, "payment")] = at(7)
	st = observe(t, s, Observation{System: "shop", Ended: e})
	if st.Records[0].Open || st.Records[0].EndedAt != at(7) {
		t.Errorf("record %+v", st.Records[0])
	}
}

func TestMissedResolve(t *testing.T) {
	c := &clock{t: at(1)}
	s := newService(c)
	inc := incident(at(0), []string{"payment"}, nil, alert(rules.AlertNoReadyPods, "payment", at(0)))
	observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{inc}, Complete: true})

	// The sweep sees the system without the incident a few minutes later: it
	// ended now.
	c.add(5 * time.Minute)
	st := observe(t, s, Observation{System: "shop", Complete: true})
	if r := st.Records[0]; r.Open || r.EndedAt != at(6) || r.EndEstimated {
		t.Errorf("swept %+v", r)
	}

	// Seen long ago and checked only now: ends when last seen, estimated.
	inc2 := incident(at(100), []string{"cart"}, nil, alert(rules.AlertErrorRate, "cart", at(100)))
	c.t = at(101)
	observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{inc2}, Complete: true})
	c.add(time.Hour)
	st = observe(t, s, Observation{System: "shop", Complete: true})
	if r := st.Records[1]; r.Open || r.EndedAt != at(101) || !r.EndEstimated {
		t.Errorf("late %+v", r)
	}

	// Alertmanager never answers again: the record closes after StaleAfter,
	// whatever system is observed.
	inc3 := incident(at(300), []string{"ad"}, nil, alert(rules.AlertErrorRate, "ad", at(300)))
	c.t = at(301)
	observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{inc3}})
	c.add(StaleAfter - time.Minute)
	st = observe(t, s, Observation{System: "shop"})
	if !st.Records[2].Open {
		t.Fatal("closed before StaleAfter")
	}
	c.add(2 * time.Minute)
	st = observe(t, s, Observation{System: "other"})
	if r := st.Records[2]; r.Open || r.EndedAt != at(301) || !r.EndEstimated {
		t.Errorf("stale %+v", r)
	}
	// The incident comes back with the same start (Kamerton was away): the
	// record reopens instead of a duplicate.
	st = observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{inc3}, Complete: true})
	if len(st.Records) != 3 || !st.Records[2].Open || !st.Records[2].EndedAt.IsZero() {
		t.Errorf("reopened %+v", st.Records)
	}
}

func TestRecordAdoptedWhenTheCauseShowsUp(t *testing.T) {
	c := &clock{t: at(1)}
	s := newService(c)
	first := incident(at(0), []string{"checkout"}, nil, alert(rules.AlertErrorRate, "checkout", at(0)))
	st := observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{first}, Complete: true})
	id := st.Records[0].ID
	c.add(time.Minute)
	later := incident(at(1), []string{"payment"}, []string{"checkout"}, alert(rules.AlertNoReadyPods, "payment", at(1)))
	st = observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{later}, Complete: true})
	if len(st.Records) != 1 {
		t.Fatalf("records %+v", st.Records)
	}
	r := st.Records[0]
	if r.ID != id || !r.Open || r.Roots[0] != "payment" || r.StartedAt != at(0) || r.Incident != "shop:payment" {
		t.Errorf("adopted %+v", r)
	}
}

func TestBoundsAndTrimming(t *testing.T) {
	now := t0
	var st State
	for i := 0; i < MaxRecords+30; i++ {
		start := now.Add(-time.Duration(MaxRecords+30-i) * time.Hour)
		st.Records = append(st.Records, Record{ID: fmt.Sprintf("%016x", i), System: "shop", Roots: []string{"payment"}, StartedAt: start, EndedAt: start.Add(time.Minute)})
	}
	st.Records[0].Note = &Note{Fix: "kept longer"}
	st.Records = append(st.Records,
		Record{ID: "old", System: "shop", StartedAt: now.Add(-MaxAge - 48*time.Hour), EndedAt: now.Add(-MaxAge - 47*time.Hour)},
		Record{ID: "open", System: "shop", Open: true, StartedAt: now.Add(-MaxAge - 48*time.Hour), LastSeen: now})
	st.prune(now)
	if len(st.Records) != MaxRecords {
		t.Fatalf("records = %d", len(st.Records))
	}
	ids := map[string]bool{}
	for _, r := range st.Records {
		ids[r.ID] = true
	}
	if ids["old"] || !ids["open"] || !ids[fmt.Sprintf("%016x", 0)] || ids[fmt.Sprintf("%016x", 1)] {
		t.Errorf("kept the wrong records: old %v open %v noted %v oldest %v", ids["old"], ids["open"], ids[fmt.Sprintf("%016x", 0)], ids[fmt.Sprintf("%016x", 1)])
	}

	// Every string at its cap: the state still fits a ConfigMap.
	long := strings.Repeat("ж", 2000)
	st = State{}
	for i := 0; i < MaxRecords; i++ {
		r := Record{ID: fmt.Sprintf("%016x", i), System: "shop", StartedAt: now.Add(time.Duration(i) * time.Minute), EndedAt: now.Add(time.Duration(i+1) * time.Minute)}
		inc := incident(r.StartedAt, []string{"payment"}, nil)
		for j := 0; j < 10; j++ {
			inc.RootAlerts = append(inc.RootAlerts, triage.Alert{Name: fmt.Sprintf("Kamerton%d", j), Service: "payment", Title: long})
		}
		for j := 0; j < 30; j++ {
			inc.Affected = append(inc.Affected, fmt.Sprintf("service-with-a-long-name-%02d", j))
		}
		r.update(inc, now)
		r.Open = false
		ev := trimEvidence(generator.EvidenceSummary{Cause: long, Changes: []string{long, long, long, long}, Operation: long, LogLine: long,
			TracesURL: "https://grafana.example.com/" + strings.Repeat("x", 2000), Explains: []string{"a", "b"}})
		r.Evidence = &ev
		r.Note = &Note{Cause: strings.Repeat("ж", MaxCause), Fix: strings.Repeat("ж", MaxFix), Source: SourceUI}
		st.Records = append(st.Records, r)
	}
	if r := st.Records[0]; len(r.RootAlerts) != maxRootAlerts || len(r.Affected) != maxAffected || len([]rune(r.RootAlerts[0].Title)) != maxTitle ||
		len(r.Evidence.Changes) != maxChanges || r.Evidence.TracesURL != "" || len([]rune(r.Evidence.LogLine)) != maxLine {
		t.Errorf("not trimmed: %d alerts, %d affected, %+v", len(r.RootAlerts), len(r.Affected), r.Evidence)
	}
	st.prune(now)
	raw, _ := json.Marshal(st)
	if len(raw) > maxBytes || len(raw) > 1<<20 {
		t.Errorf("state is %d bytes", len(raw))
	}
	if len(st.Records) == 0 || st.Records[len(st.Records)-1].ID != fmt.Sprintf("%016x", MaxRecords-1) {
		t.Errorf("the newest records must stay; kept %d", len(st.Records))
	}
}

func TestConcurrentWriters(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	stores := map[string]Store{
		"memory":    &MemoryStore{},
		"configmap": &ConfigMapStore{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Namespace: "kamerton", Name: "kamerton-incident-history"},
	}
	for name, store := range stores {
		c := &clock{t: at(1)}
		s := &Service{Store: store, Now: c.now}
		ctx := context.Background()
		const n = 8
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				svc := fmt.Sprintf("svc%d", i)
				inc := incident(at(0), []string{svc}, nil, alert(rules.AlertErrorRate, svc, at(0)))
				if _, err := s.Observe(ctx, Observation{System: "shop", Incidents: []triage.Incident{inc}}); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}(i)
		}
		wg.Wait()
		list, err := s.List(ctx, Filter{})
		if err != nil || len(list) != n {
			t.Fatalf("%s: %d records, %v", name, len(list), err)
		}
		for _, r := range list {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if _, err := s.SetNote(ctx, id, NoteInput{Fix: str("restarted " + id)}, SourceUI); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}(r.ID)
		}
		wg.Wait()
		list, _ = s.List(ctx, Filter{})
		for _, r := range list {
			if r.Note == nil || r.Note.Fix != "restarted "+r.ID {
				t.Errorf("%s: note of %s lost: %+v", name, r.ID, r.Note)
			}
		}
	}
}

func TestNotes(t *testing.T) {
	c := &clock{t: at(1)}
	s := newService(c)
	ctx := context.Background()
	inc := incident(at(0), []string{"payment"}, nil, alert(rules.AlertNoReadyPods, "payment", at(0)))
	st := observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{inc}})
	id := st.Records[0].ID
	r, err := s.SetNote(ctx, id, NoteInput{Cause: str("  OOM after release \n"), Fix: str("rolled back")}, SourceUI)
	if err != nil || r.Note.Cause != "OOM after release" || r.Note.Fix != "rolled back" || r.Note.Source != SourceUI || r.Note.UpdatedAt != at(1) {
		t.Fatalf("note %+v, %v", r.Note, err)
	}
	// A field left out is kept; a later fix replaces the earlier one.
	r, _ = s.SetNote(ctx, id, NoteInput{Fix: str("raised the memory limit")}, SourceTelegram)
	if r.Note.Cause != "OOM after release" || r.Note.Fix != "raised the memory limit" || r.Note.Source != SourceTelegram {
		t.Errorf("replaced %+v", r.Note)
	}
	if _, err := s.SetNote(ctx, id, NoteInput{Fix: str(strings.Repeat("я", MaxFix+1))}, SourceUI); err != ErrTooLong {
		t.Errorf("too long fix: %v", err)
	}
	if _, err := s.SetNote(ctx, id, NoteInput{Cause: str(strings.Repeat("я", MaxCause+1))}, SourceUI); err != ErrTooLong {
		t.Errorf("too long cause: %v", err)
	}
	if _, err := s.SetNote(ctx, "0123456789abcdef", NoteInput{Fix: str("x")}, SourceUI); err != ErrNotFound {
		t.Errorf("unknown id: %v", err)
	}
	r, _ = s.SetNote(ctx, id, NoteInput{Cause: str(""), Fix: str(" ")}, SourceUI)
	if r.Note != nil {
		t.Errorf("an empty note is removed: %+v", r.Note)
	}
}

func closedRecord(id, kind, svc string, start time.Time, d time.Duration, note *Note) Record {
	return Record{ID: id, System: "shop", Roots: []string{svc}, RootAlerts: []AlertRef{{Name: kind, Service: svc}},
		StartedAt: start, EndedAt: start.Add(d), Note: note, Evidence: &generator.EvidenceSummary{Cause: "collected cause of " + id}}
}

func TestRecallSelection(t *testing.T) {
	now := t0
	live := incident(now, []string{"payment"}, nil, alert("KamertonSLOBurnRateFast", "payment", now), alert(rules.AlertNoReadyPods, "payment", now.Add(time.Minute)))
	st := &State{}
	if p := st.Recall("shop", live, now); p != nil {
		t.Errorf("no history: %+v", p)
	}
	st.Records = []Record{
		closedRecord("a", rules.AlertNoReadyPods, "payment", now.Add(-40*24*time.Hour), 5*time.Minute, &Note{Cause: "node drained", Fix: "cordoned the node"}),
		closedRecord("b", rules.AlertNoReadyPods, "payment", now.Add(-20*24*time.Hour), 12*time.Minute, nil),
		closedRecord("c", rules.AlertErrorRate, "payment", now.Add(-2*24*time.Hour), 3*time.Minute, &Note{Fix: "restarted"}),
		closedRecord("d", rules.AlertNoReadyPods, "payment", now.Add(-10*24*time.Hour), 7*time.Minute, &Note{Fix: "scaled up"}),
		closedRecord("e", rules.AlertNoReadyPods, "cart", now.Add(-time.Hour), time.Minute, nil),
		{ID: "f", System: "other", Roots: []string{"payment"}, RootAlerts: []AlertRef{{Name: rules.AlertNoReadyPods, Service: "payment"}}, StartedAt: now.Add(-time.Hour), EndedAt: now},
		{ID: "live", System: "shop", Open: true, Incident: "shop:payment", Roots: []string{"payment"}, RootAlerts: []AlertRef{{Name: rules.AlertNoReadyPods, Service: "payment"}}, StartedAt: now},
	}
	// No ready pods explains more than a burning budget: same kind, most recent.
	p := st.Recall("shop", live, now)
	if p == nil || p.ID != "d" || p.Fix != "scaled up" || p.Cause != "collected cause of d" || p.DurationSeconds != 420 || p.Count30d != 2 || !p.Noted {
		t.Fatalf("recall %+v", p)
	}
	// Only another kind on record: fall back to the same service.
	live2 := incident(now, []string{"payment"}, nil, alert(rules.AlertRequestsStopped, "payment", now))
	st.Records = st.Records[2:3]
	p = st.Recall("shop", live2, now)
	if p == nil || p.ID != "c" || p.Count30d != 1 || p.Cause != "collected cause of c" {
		t.Errorf("fallback %+v", p)
	}
	if r := p.Recall(); r.Duration != 3*time.Minute || r.Fix != "restarted" || !r.Noted {
		t.Errorf("generator recall %+v", r)
	}
	// A note's cause wins over the collected one.
	st.Records[0].Note.Cause = "bad config"
	if p = st.Recall("shop", live2, now); p.Cause != "bad config" {
		t.Errorf("cause %q", p.Cause)
	}
	if p := st.Recall("shop", incident(now, []string{"ad"}, nil), now); p != nil {
		t.Errorf("other service: %+v", p)
	}
}

func TestExperience(t *testing.T) {
	s := newService(&clock{t: t0})
	ctx := context.Background()
	_, _ = s.store().Update(ctx, func(st *State) error {
		for i := 0; i < 8; i++ {
			var note *Note
			if i != 3 {
				note = &Note{Fix: fmt.Sprintf("fix %d", i)}
			}
			st.Records = append(st.Records, closedRecord(fmt.Sprintf("r%d", i), rules.AlertNoReadyPods, "payment", t0.Add(time.Duration(i)*time.Hour), time.Minute, note))
		}
		st.Records = append(st.Records, closedRecord("x", rules.AlertNoReadyPods, "cart", t0.Add(10*time.Hour), time.Minute, &Note{Fix: "other"}))
		return nil
	})
	exp, err := s.Experience(ctx, "shop", "payment")
	if err != nil || len(exp) != maxExperience || exp[0].ID != "r7" || exp[0].Fix != "fix 7" || exp[0].Cause != "collected cause of r7" || exp[0].DurationSeconds != 60 {
		t.Fatalf("experience %+v, %v", exp, err)
	}
	for _, e := range exp {
		if e.ID == "r3" {
			t.Error("a record without a note is not experience")
		}
	}
	if exp, _ := s.Experience(ctx, "shop", "ad"); exp == nil || len(exp) != 0 {
		t.Errorf("no experience: %#v", exp)
	}
}

func TestRecordFollowsTriage(t *testing.T) {
	c := &clock{t: at(1)}
	s := newService(c)
	// First seen with two roots, then explained by payment.
	early := incident(at(0), []string{"checkout", "frontend"}, nil, alert(rules.AlertErrorRate, "checkout", at(0)), alert(rules.AlertOperationErrorRate, "frontend", at(0)))
	observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{early}, Complete: true})
	c.add(time.Minute)
	late := incident(at(1), []string{"payment"}, []string{"checkout", "frontend"}, alert(rules.AlertNoReadyPods, "payment", at(1)))
	observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{late}, Complete: true})
	c.add(time.Minute)
	late.RootAlerts = []triage.Alert{alert(rules.AlertRequestsStopped, "payment", at(2))}
	late.Affected = []string{"checkout"}
	st := observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{late}, Complete: true})
	r := st.Records[0]
	if len(st.Records) != 1 || r.Incident != "shop:payment" || r.StartedAt != at(0) || strings.Join(r.Affected, ",") != "checkout,frontend" {
		t.Errorf("record %+v", r)
	}
	if len(r.RootAlerts) != 2 || r.RootAlerts[0].Name != rules.AlertNoReadyPods || r.RootAlerts[1].Name != rules.AlertRequestsStopped ||
		strings.Join(r.Firing, ",") != rules.AlertRequestsStopped+"@payment" {
		t.Errorf("root alerts %+v, firing %v", r.RootAlerts, r.Firing)
	}
	// Closed records keep their incident ID.
	c.add(time.Minute)
	st = observe(t, s, Observation{System: "shop", Complete: true})
	if st.Records[0].Open || st.Records[0].Incident != "shop:payment" {
		t.Errorf("closed %+v", st.Records[0])
	}
}

func TestMergeWhenAttributionArrivesLate(t *testing.T) {
	c := &clock{t: at(1)}
	s := newService(c)
	ctx := context.Background()
	caller := incident(at(0), []string{"checkout"}, nil, alert(rules.AlertErrorRate, "checkout", at(0)))
	st := observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{caller}, Complete: true})
	callerID := st.Records[0].ID
	if err := s.FollowUpSent(ctx, "shop", "shop:checkout", []string{"oncall"}); err != nil {
		t.Fatal(err)
	}
	// Alertmanager did not answer: the notification alone shows payment as
	// an incident of its own, so it gets a record.
	c.add(time.Minute)
	cause := incident(at(1), []string{"payment"}, nil, alert(rules.AlertNoReadyPods, "payment", at(1)))
	st = observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{cause}})
	if len(st.Records) != 2 {
		t.Fatalf("records %+v", st.Records)
	}
	// Now the full view: checkout is affected by payment.
	c.add(time.Minute)
	cause.Affected = []string{"checkout"}
	var closed []Record
	s.OnClose = func(_ context.Context, r []Record) { closed = append(closed, r...) }
	st = observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{cause}, Complete: true})
	if len(st.Records) != 1 || len(closed) != 0 {
		t.Fatalf("records %+v, closed %+v", st.Records, closed)
	}
	r := st.Records[0]
	if r.Roots[0] != "payment" || r.StartedAt != at(0) || strings.Join(r.Merged, ",") != callerID || strings.Join(r.FollowUpTo, ",") != "oncall" ||
		strings.Join(r.Affected, ",") != "checkout" {
		t.Errorf("merged %+v", r)
	}
}

func TestAftermathRules(t *testing.T) {
	c := &clock{t: at(1)}
	s := newService(c)
	cause := incident(at(0), []string{"payment"}, []string{"checkout"}, alert(rules.AlertNoReadyPods, "payment", at(0)))
	observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{cause}, Complete: true})
	// The root's own SLO burn after its real alert resolved: the record ends,
	// the burn is its aftermath.
	c.t = at(20)
	burn := incident(at(5), []string{"payment"}, nil, alert(rules.AlertSLOBurnSlow, "payment", at(5)))
	tail := incident(at(2), []string{"checkout"}, nil, alert(rules.AlertSLOBurnSlow, "checkout", at(2)), alert(rules.AlertErrorRate, "checkout", at(2)))
	st := observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{burn, tail}, Complete: true})
	r := st.Records[0]
	if len(st.Records) != 1 || r.Open || r.EndedAt != at(20) || len(r.Aftermath) != 2 || len(r.StillFiring()) != 2 {
		t.Fatalf("records %+v", st.Records)
	}
	if p := st.AftermathOf("shop", tail, c.t); p == nil || p.ID != r.ID {
		t.Errorf("aftermath of %+v", p)
	}
	// A cause of its own is never aftermath.
	own := incident(at(21), []string{"checkout"}, nil, alert(rules.AlertRestarts, "checkout", at(21)))
	if p := st.AftermathOf("shop", own, c.t); p != nil {
		t.Errorf("restarts are not aftermath: %+v", p)
	}
	// A service the incident did not involve is never aftermath.
	if p := st.AftermathOf("shop", incident(at(21), []string{"ad"}, nil, alert(rules.AlertErrorRate, "ad", at(21))), c.t); p != nil {
		t.Errorf("unrelated service: %+v", p)
	}
	// Bounded.
	c.add(time.Minute)
	for i := 0; i < 15; i++ {
		inc := incident(at(2), []string{fmt.Sprintf("svc%d", i)}, nil)
		for j := 0; j < 8; j++ {
			inc.RootAlerts = append(inc.RootAlerts, alert(fmt.Sprintf("KamertonCustom%d", j), inc.Roots[0], at(2)))
		}
		st.Records[0].Affected = append(st.Records[0].Affected, inc.Roots[0])
		st.Records[0].addAftermath(triage.Incident{RootAlerts: inc.RootAlerts}, c.t)
	}
	if a := st.Records[0].Aftermath; len(a) != maxAftermath || len(a[2].Alerts) != maxAftermathAlert {
		t.Errorf("aftermath not bounded: %d", len(a))
	}
	// After the grace period the tail is an incident of its own, started
	// when the grace period ended.
	c.t = at(20).Add(AftermathGrace + 5*time.Minute)
	st = observe(t, s, Observation{System: "shop", Incidents: []triage.Incident{tail}, Complete: true})
	if len(st.Records) != 2 {
		t.Fatalf("records %+v", st.Records)
	}
	p := st.Records[1]
	if !p.Open || !p.Promoted || p.StartedAt != at(20).Add(AftermathGrace) || p.Incident != "shop:checkout" {
		t.Errorf("promoted %+v", p)
	}
	if got := st.SystemsToWatch(c.t); len(got) != 1 {
		t.Errorf("systems to watch %v", got)
	}
	// Nobody confirms it for a day: the 24 hour bound closes it.
	c.add(StaleAfter + time.Minute)
	st = observe(t, s, Observation{System: "shop"})
	if st.Records[1].Open || !st.Records[1].EndEstimated {
		t.Errorf("stale %+v", st.Records[1])
	}
	if got := st.SystemsToWatch(c.t); len(got) != 0 {
		t.Errorf("nothing to watch any more: %v", got)
	}
}
