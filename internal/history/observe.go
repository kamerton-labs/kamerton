// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package history

import (
	"sort"
	"time"

	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

const (
	// AftermathGrace: after an incident's roots resolve, symptom alerts of the
	// services it involved belong to it this long. The slow SLO burn alert
	// needs its 30 minute short window free of the outage's errors before it
	// clears; 5 more minutes cover rule evaluation and Alertmanager.
	AftermathGrace = 35 * time.Minute
	// lastSeenStep: LastSeen is refreshed at most this often, so frequent
	// observations of an unchanged incident do not write.
	lastSeenStep      = time.Minute
	maxAftermath      = 10
	maxAftermathAlert = 5
	maxMerged         = 10
)

func firingKey(alert, service string) string { return alert + "@" + service }

// symptomsOnly: every root alert is something a failing dependency causes
// (errors, latency, stopped requests, SLO burn), nothing of the service's
// own such as no ready pods, restarts or memory.
func symptomsOnly(inc triage.Incident) bool {
	if len(inc.RootAlerts) == 0 {
		return false
	}
	for _, a := range inc.RootAlerts {
		if !rules.IsSymptomTarget(a.Name) {
			return false
		}
	}
	return true
}

// lingering: only SLO burn alerts are left, which keep firing for a while
// after the errors stopped because their window still holds them.
func lingering(inc triage.Incident) bool {
	if len(inc.RootAlerts) == 0 {
		return false
	}
	for _, a := range inc.RootAlerts {
		if a.Name != rules.AlertSLOBurnFast && a.Name != rules.AlertSLOBurnSlow {
			return false
		}
	}
	return true
}

// onlySLO: the record never had more than SLO burn alerts of its roots
// (a slow burn of its own), so their SLO burn is not a tail.
func (r *Record) onlySLO() bool {
	for _, a := range r.RootAlerts {
		if a.Name != rules.AlertSLOBurnFast && a.Name != rules.AlertSLOBurnSlow {
			return false
		}
	}
	return true
}

func (r *Record) involves(services []string) bool {
	for _, s := range services {
		if !contains(r.Roots, s) && !contains(r.Affected, s) {
			return false
		}
	}
	return len(services) > 0
}

// parent finds the record an incident is the aftermath of: one of the same
// system that involved all its roots and is open or ended within
// AftermathGrace. -1 when there is none or the incident shows a cause of
// its own.
func (st *State) parent(system string, inc triage.Incident, now time.Time) int {
	if !symptomsOnly(inc) {
		return -1
	}
	best := -1
	for i := range st.Records {
		r := &st.Records[i]
		if r.System != system || !r.involves(inc.Roots) {
			continue
		}
		if !r.Open && now.Sub(r.EndedAt) > AftermathGrace {
			continue
		}
		if best < 0 || r.StartedAt.After(st.Records[best].StartedAt) {
			best = i
		}
	}
	return best
}

// AftermathOf returns the record a live incident is the aftermath of; such
// an incident gets no record and no follow-up of its own.
func (st *State) AftermathOf(system string, inc triage.Incident, now time.Time) *Record {
	if st == nil || len(inc.Roots) == 0 || st.openFor(system, inc.ID) >= 0 {
		return nil
	}
	if i := st.parent(system, inc, now); i >= 0 {
		r := st.Records[i]
		return &r
	}
	return nil
}

// promotedStart: aftermath still firing after the grace period is an
// incident of its own from the moment the grace period of the incident it
// followed ended.
func (st *State) promotedStart(system string, inc triage.Incident) (time.Time, bool) {
	if !symptomsOnly(inc) {
		return time.Time{}, false
	}
	var start time.Time
	for _, r := range st.Records {
		if r.Open || r.System != system || !r.involves(inc.Roots) {
			continue
		}
		if end := r.EndedAt.Add(AftermathGrace); inc.StartsAt.Before(end) && end.After(start) {
			start = end
		}
	}
	return start, !start.IsZero()
}

func (r *Record) addAftermath(inc triage.Incident, now time.Time) (changed bool) {
	for _, a := range inc.RootAlerts {
		i := -1
		for j := range r.Aftermath {
			if r.Aftermath[j].Service == a.Service {
				i = j
			}
		}
		if i < 0 {
			if len(r.Aftermath) >= maxAftermath {
				continue
			}
			r.Aftermath = append(r.Aftermath, Aftermath{Service: a.Service, Since: now})
			i = len(r.Aftermath) - 1
		}
		x := &r.Aftermath[i]
		if now.Sub(x.LastSeen) >= lastSeenStep {
			x.LastSeen, changed = now, true
		}
		if !contains(x.Alerts, a.Name) && len(x.Alerts) < maxAftermathAlert {
			x.Alerts, changed = append(x.Alerts, a.Name), true
		}
	}
	return changed
}

// WatchAfterEnd: a system whose incident left aftermath is still swept this
// long after the end, so a tail that outlives AftermathGrace is promoted
// even when no notification comes.
const WatchAfterEnd = AftermathGrace + 15*time.Minute

// SystemsToWatch returns the systems with open records or with recent aftermath.
func (st *State) SystemsToWatch(now time.Time) []string {
	var out []string
	for _, r := range st.Records {
		if (r.Open || len(r.Aftermath) > 0 && now.Sub(r.EndedAt) <= WatchAfterEnd) && !contains(out, r.System) {
			out = append(out, r.System)
		}
	}
	sort.Strings(out)
	return out
}

// StillFiring returns the aftermath seen at or after the end of the record.
func (r Record) StillFiring() []Aftermath {
	var out []Aftermath
	for _, a := range r.Aftermath {
		if !r.EndedAt.IsZero() && !a.LastSeen.Before(r.EndedAt) {
			out = append(out, a)
		}
	}
	return out
}

// observe applies one view of a system. In order: live incidents update
// their open records; other open records whose roots a live incident now
// explains are merged into its record; the remaining incidents reopen their
// record, are the aftermath of an earlier incident, take over an open
// record, or open one; then open records without a live incident close.
func (st *State) observe(o Observation, now time.Time) (closed []Record, changed bool) {
	matched := map[int]bool{}
	ended := map[int]time.Time{}
	var rest []triage.Incident
	type pair struct {
		inc triage.Incident
		id  string
	}
	var pairs []pair
	for _, inc := range o.Incidents {
		if len(inc.Roots) == 0 || inc.System != o.System {
			continue
		}
		i := st.openFor(o.System, inc.ID)
		if i >= 0 && o.Complete && lingering(inc) && !st.Records[i].onlySLO() {
			// The roots resolved; their SLO burn is the tail.
			ended[i] = now
			rest = append(rest, inc)
			continue
		}
		if i < 0 {
			rest = append(rest, inc)
			continue
		}
		matched[i] = true
		changed = st.Records[i].update(inc, now) || changed
		pairs = append(pairs, pair{inc, st.Records[i].ID})
	}

	var later []triage.Incident
	for _, inc := range rest {
		if lingering(inc) {
			later = append(later, inc)
			continue
		}
		id := recordID(o.System, inc.ID, inc.StartsAt)
		if i := st.find(id); i >= 0 && !matched[i] {
			if _, closing := ended[i]; !closing {
				matched[i] = true
				st.Records[i].update(inc, now)
				changed = true
				pairs = append(pairs, pair{inc, id})
				continue
			}
		}
		later = append(later, inc)
	}

	for _, p := range pairs {
		if st.mergeInto(p.id, p.inc, matched, ended) {
			changed = true
		}
	}

	for _, inc := range later {
		if i := st.parent(o.System, inc, now); i >= 0 {
			changed = st.Records[i].addAftermath(inc, now) || changed
			continue
		}
		i := st.adoptable(o.System, inc, matched)
		if i < 0 {
			start := inc.StartsAt.UTC()
			promoted := false
			if s, ok := st.promotedStart(o.System, inc); ok {
				start, promoted = s.UTC(), true
			}
			st.Records = append(st.Records, Record{ID: recordID(o.System, inc.ID, start), System: o.System, StartedAt: start, Promoted: promoted})
			i = len(st.Records) - 1
		}
		st.Records[i].update(inc, now)
		matched[i] = true
		changed = true
		st.mergeInto(st.Records[i].ID, inc, matched, ended)
	}

	var closedIDs []string
	for i := range st.Records {
		r := &st.Records[i]
		if !r.Open || matched[i] {
			continue
		}
		end, ok := r.endedBy(o.Ended)
		forced, lingered := ended[i]
		switch {
		case r.System == o.System && (o.Complete || ok || lingered):
			if lingered && !ok {
				end, ok = forced, true
			}
			if !ok {
				end = now
				if now.Sub(r.LastSeen) > lateEnd {
					end, r.EndEstimated = r.LastSeen, true
				}
			}
		case now.Sub(r.LastSeen) > StaleAfter:
			end, r.EndEstimated = r.LastSeen, true
		default:
			continue
		}
		if end.Before(r.StartedAt) {
			end = r.StartedAt
		}
		r.Open, r.EndedAt = false, end.UTC()
		closedIDs = append(closedIDs, r.ID)
		changed = true
	}
	for _, id := range closedIDs {
		if i := st.find(id); i >= 0 {
			closed = append(closed, st.Records[i])
		}
	}
	return closed, changed
}

// mergeInto folds other open records whose roots the incident now explains
// into the record with the given ID: they were opened for a caller before
// the cause's alert fired. The merged record keeps the earliest start, its
// follow-up receivers, and its note when the target has none.
func (st *State) mergeInto(id string, inc triage.Incident, matched map[int]bool, ended map[int]time.Time) bool {
	in := map[string]bool{}
	for _, x := range append(append([]string{}, inc.Roots...), inc.Affected...) {
		in[x] = true
	}
	var drop []string
	for i := range st.Records {
		q := st.Records[i]
		if q.ID == id || !q.Open || matched[i] || q.System != inc.System || len(q.Roots) == 0 {
			continue
		}
		if _, closing := ended[i]; closing {
			continue
		}
		all := true
		for _, x := range q.Roots {
			all = all && in[x]
		}
		if all {
			drop = append(drop, q.ID)
		}
	}
	if len(drop) == 0 {
		return false
	}
	for _, qid := range drop {
		t, q := st.find(id), st.find(qid)
		target, src := &st.Records[t], st.Records[q]
		if src.StartedAt.Before(target.StartedAt) {
			target.StartedAt = src.StartedAt
		}
		for _, n := range src.FollowUpTo {
			if !contains(target.FollowUpTo, n) {
				target.FollowUpTo = append(target.FollowUpTo, n)
			}
		}
		if target.Note == nil {
			target.Note = src.Note
		}
		if target.Evidence == nil {
			target.Evidence = src.Evidence
		}
		for _, x := range append(src.Roots, src.Affected...) {
			if !contains(target.Roots, x) && !contains(target.Affected, x) && len(target.Affected) < maxAffected {
				target.Affected = append(target.Affected, x)
			}
		}
		sort.Strings(target.Affected)
		target.Merged = append(target.Merged, append(src.Merged, src.ID)...)
		if len(target.Merged) > maxMerged {
			target.Merged = target.Merged[len(target.Merged)-maxMerged:]
		}
		st.Records = append(st.Records[:q], st.Records[q+1:]...)
		// Indexes after q moved down by one.
		shift := func(m map[int]bool) {
			for k := range m {
				if k > q {
					delete(m, k)
					m[k-1] = true
				}
			}
		}
		shift(matched)
		moved := map[int]time.Time{}
		for k, v := range ended {
			if k > q {
				k--
			}
			moved[k] = v
		}
		for k := range ended {
			delete(ended, k)
		}
		for k, v := range moved {
			ended[k] = v
		}
	}
	return true
}

// endedBy: the resolved notification covers every root alert of the last
// view; the latest end is the record's end.
func (r *Record) endedBy(ended map[string]time.Time) (time.Time, bool) {
	if len(ended) == 0 || len(r.Firing) == 0 {
		return time.Time{}, false
	}
	var end time.Time
	for _, k := range r.Firing {
		var t time.Time
		found := false
		for _, a := range r.RootAlerts {
			if firingKey(a.Name, a.Service) == k {
				t, found = ended[EndKey(a.Name, a.Service)]
			}
		}
		if !found {
			return time.Time{}, false
		}
		if t.After(end) {
			end = t
		}
	}
	return end, true
}

func (st *State) openFor(system, incident string) int {
	for i, r := range st.Records {
		if r.Open && r.System == system && r.Incident == incident {
			return i
		}
	}
	return -1
}

// adoptable finds an open record without a note whose roots the incident now
// explains (they are among its roots or affected services): the same
// incident seen first from a caller, before the dependency's alert fired.
func (st *State) adoptable(system string, inc triage.Incident, matched map[int]bool) int {
	in := map[string]bool{}
	for _, x := range append(append([]string{}, inc.Roots...), inc.Affected...) {
		in[x] = true
	}
	for i, r := range st.Records {
		if !r.Open || matched[i] || r.System != system || r.Note != nil || len(r.Roots) == 0 {
			continue
		}
		all := true
		for _, x := range r.Roots {
			all = all && in[x]
		}
		if all {
			return i
		}
	}
	return -1
}

// update makes the record follow the incident as triage sees it now: its
// roots, the alerts of those roots (earlier ones of the same roots kept,
// alerts of services that are no longer roots dropped) and the services it
// affected over its life; the earliest start is kept. It reports whether
// anything stored changed.
func (r *Record) update(inc triage.Incident, now time.Time) bool {
	formerRoots := r.Roots
	changed := !r.Open || r.Incident != inc.ID || !equal(r.Roots, inc.Roots)
	r.Open, r.Incident, r.EndedAt, r.EndEstimated = true, inc.ID, time.Time{}, false
	r.Roots = append([]string{}, inc.Roots...)
	if start := inc.StartsAt.UTC(); !start.IsZero() && (r.StartedAt.IsZero() || start.Before(r.StartedAt)) && !r.Promoted {
		r.StartedAt, changed = start, true
	}
	var alerts []AlertRef
	var firing []string
	for _, a := range inc.RootAlerts {
		if !contains(inc.Roots, a.Service) {
			continue
		}
		firing = append(firing, firingKey(a.Name, a.Service))
		if len(alerts) < maxRootAlerts && !hasAlert(alerts, a.Name, a.Service) {
			alerts = append(alerts, AlertRef{Name: a.Name, Service: a.Service, Title: trim(a.Title, maxTitle), Severity: a.Severity})
		}
	}
	for _, a := range r.RootAlerts {
		if contains(inc.Roots, a.Service) && len(alerts) < maxRootAlerts && !hasAlert(alerts, a.Name, a.Service) {
			alerts = append(alerts, a)
		}
	}
	sort.SliceStable(alerts, func(i, j int) bool { return rank(alerts[i].Name) > rank(alerts[j].Name) })
	if !equalAlerts(r.RootAlerts, alerts) || !equal(r.Firing, firing) {
		r.RootAlerts, r.Firing, changed = alerts, firing, true
	}
	var affected []string
	// A former root that the new roots now explain was affected too.
	for _, a := range append(append(append([]string{}, inc.Affected...), r.Affected...), formerRoots...) {
		if !contains(inc.Roots, a) && !contains(affected, a) && len(affected) < maxAffected {
			affected = append(affected, a)
		}
	}
	sort.Strings(affected)
	if !equal(r.Affected, affected) {
		r.Affected, changed = affected, true
	}
	if changed || now.Sub(r.LastSeen) >= lastSeenStep {
		r.LastSeen, changed = now, true
	}
	return changed
}

func rank(name string) int {
	switch {
	case rules.IsSymptomSource(name) && !rules.IsSymptomTarget(name):
		return 3
	case rules.IsSymptomSource(name):
		return 2
	case rules.IsSymptomTarget(name):
		return 0
	}
	return 1
}

func hasAlert(list []AlertRef, name, service string) bool {
	for _, a := range list {
		if a.Name == name && a.Service == service {
			return true
		}
	}
	return false
}

func equalAlerts(a, b []AlertRef) bool {
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
