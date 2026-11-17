// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package triage

import (
	"sort"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/internal/rules"
)

// A symptom target of a service whose dependency has a symptom source is a
// consequence: inhibit rules mute it and it is listed as affected. Both use
// MutingEdges, so "also affected" in the UI is what Alertmanager mutes.

type Alert struct {
	Fingerprint string    `json:"fingerprint,omitempty"`
	Name        string    `json:"name"`
	Service     string    `json:"service"`
	Severity    string    `json:"severity"`
	StartsAt    time.Time `json:"startsAt"`
	Title       string    `json:"title,omitempty"`
	Value       string    `json:"value,omitempty"`
	// Muted means Alertmanager inhibits the alert while its cause fires.
	Muted bool `json:"muted"`
}

// SystemRoot ends the ID of the incident of alerts about the whole system
// (no service label); such an incident has no roots.
const SystemRoot = "_system"

type Incident struct {
	// ID is system plus roots, so it is stable while the roots stay the same.
	ID         string   `json:"id"`
	System     string   `json:"system"`
	Roots      []string `json:"roots"`
	RootAlerts []Alert  `json:"rootAlerts"`
	// Affected services' symptom alerts are muted in Alertmanager when grouping by cause is on.
	Affected       []string  `json:"affected"`
	AffectedAlerts []Alert   `json:"affectedAlerts"`
	StartsAt       time.Time `json:"startsAt"`
}

// MutingEdges maps a service to its direct callers, whose symptom alerts it mutes.
// Links inside a dependency cycle are left out: two services calling each other
// would mute each other and nobody would be told. Lists are sorted.
func (g *Graph) MutingEdges() map[string][]string {
	comp := g.components()
	out := map[string][]string{}
	for caller, deps := range g.deps {
		for _, d := range deps {
			if _, known := g.deps[d]; !known || d == caller || comp[d] == comp[caller] {
				continue
			}
			out[d] = appendUnique(out[d], caller)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// components numbers strongly connected components (iterative Tarjan, so a long
// chain cannot overflow the stack). Services in one cycle share a number.
func (g *Graph) components() map[string]int {
	names := make([]string, 0, len(g.deps))
	for n := range g.deps {
		names = append(names, n)
	}
	sort.Strings(names)
	index, low := map[string]int{}, map[string]int{}
	onStack := map[string]bool{}
	comp := map[string]int{}
	var stack []string
	next, nextComp := 0, 0
	type frame struct {
		n string
		i int
	}
	for _, start := range names {
		if _, seen := index[start]; seen {
			continue
		}
		work := []frame{{n: start}}
		index[start], low[start] = next, next
		next++
		stack = append(stack, start)
		onStack[start] = true
		for len(work) > 0 {
			f := &work[len(work)-1]
			deps := g.deps[f.n]
			if f.i < len(deps) {
				d := deps[f.i]
				f.i++
				if _, known := g.deps[d]; !known {
					continue
				}
				if _, seen := index[d]; !seen {
					index[d], low[d] = next, next
					next++
					stack = append(stack, d)
					onStack[d] = true
					work = append(work, frame{n: d})
				} else if onStack[d] && index[d] < low[f.n] {
					low[f.n] = index[d]
				}
				continue
			}
			n := f.n
			work = work[:len(work)-1]
			if len(work) > 0 {
				p := work[len(work)-1].n
				if low[n] < low[p] {
					low[p] = low[n]
				}
			}
			if low[n] == index[n] {
				for {
					top := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[top] = false
					comp[top] = nextComp
					if top == n {
						break
					}
				}
				nextComp++
			}
		}
	}
	return comp
}

// Incidents groups alerts, muted ones included, by cause, sorted by start time, then ID.
func (g *Graph) Incidents(system string, alerts []Alert) []Incident {
	callers := g.MutingEdges()
	// deps restricted to muting links: service -> dependencies it is muted by.
	mutedBy := map[string][]string{}
	for dep, cs := range callers {
		for _, c := range cs {
			mutedBy[c] = append(mutedBy[c], dep)
		}
	}
	alerts, wide := systemWide(system, alerts)
	bySvc := map[string][]Alert{}
	source, target, ownCause := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var services []string
	for _, a := range alerts {
		if _, seen := bySvc[a.Service]; !seen {
			services = append(services, a.Service)
		}
		bySvc[a.Service] = append(bySvc[a.Service], a)
		source[a.Service] = source[a.Service] || rules.IsSymptomSource(a.Name)
		target[a.Service] = target[a.Service] || rules.IsSymptomTarget(a.Name)
		ownCause[a.Service] = ownCause[a.Service] || (rules.IsSymptomSource(a.Name) && !rules.IsSymptomTarget(a.Name))
	}
	sort.Strings(services)

	// A service with a symptom target is affected when a dependency it is
	// muted by has a symptom source, unless it has a cause of its own (no
	// ready pods). Muting links have no cycles, so walking down always ends.
	isRoot := func(s string) bool {
		if !target[s] || ownCause[s] {
			return true
		}
		for _, d := range mutedBy[s] {
			if source[d] {
				return false
			}
		}
		return true
	}
	memo := map[string][]string{}
	var rootsOf func(s string, visiting map[string]bool) []string
	rootsOf = func(s string, visiting map[string]bool) []string {
		if r, ok := memo[s]; ok {
			return r
		}
		if visiting[s] {
			return nil // cannot happen without cycles; kept as a guard
		}
		visiting[s] = true
		defer delete(visiting, s)
		if isRoot(s) {
			memo[s] = []string{s}
			return memo[s]
		}
		set := map[string]bool{}
		for _, d := range mutedBy[s] {
			if source[d] {
				for _, r := range rootsOf(d, visiting) {
					set[r] = true
				}
			}
		}
		memo[s] = sortedKeys(set)
		return memo[s]
	}

	// Union roots that share an affected service.
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			if ra < rb {
				parent[rb] = ra
			} else {
				parent[ra] = rb
			}
		}
	}
	rootOfAffected := map[string][]string{}
	for _, s := range services {
		if !source[s] && !target[s] {
			find(s) // a service with other alerts only: its own group
			continue
		}
		rs := rootsOf(s, map[string]bool{})
		for _, r := range rs {
			find(r)
		}
		for i := 1; i < len(rs); i++ {
			union(rs[0], rs[i])
		}
		if len(rs) != 1 || rs[0] != s {
			rootOfAffected[s] = rs
		}
	}

	groups := map[string]*Incident{}
	group := func(key string) *Incident {
		inc := groups[key]
		if inc == nil {
			inc = &Incident{System: system, RootAlerts: []Alert{}, Affected: []string{}, AffectedAlerts: []Alert{}}
			groups[key] = inc
		}
		return inc
	}
	for _, s := range services {
		if rs, affected := rootOfAffected[s]; affected {
			inc := group(find(rs[0]))
			inc.Affected = append(inc.Affected, s)
			inc.AffectedAlerts = append(inc.AffectedAlerts, bySvc[s]...)
			continue
		}
		inc := group(find(s))
		inc.Roots = append(inc.Roots, s)
		inc.RootAlerts = append(inc.RootAlerts, bySvc[s]...)
	}
	out := make([]Incident, 0, len(groups))
	for _, inc := range groups {
		sort.Strings(inc.Roots)
		sort.Strings(inc.Affected)
		sortAlerts(inc.RootAlerts)
		sortAlerts(inc.AffectedAlerts)
		for _, a := range inc.RootAlerts {
			if inc.StartsAt.IsZero() || a.StartsAt.Before(inc.StartsAt) {
				inc.StartsAt = a.StartsAt
			}
		}
		inc.ID = system + ":" + strings.Join(inc.Roots, "+")
		out = append(out, *inc)
	}
	if wide != nil {
		out = append(out, *wide)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartsAt.Equal(out[j].StartsAt) {
			return out[i].StartsAt.Before(out[j].StartsAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// systemWide takes out the alerts about the whole system into an incident of
// their own. While one fires, the per-service "no data" alerts it mutes are
// listed as affected there.
func systemWide(system string, alerts []Alert) ([]Alert, *Incident) {
	var wide *Incident
	for _, a := range alerts {
		if a.Service != "" {
			continue
		}
		if wide == nil {
			wide = &Incident{ID: system + ":" + SystemRoot, System: system, Roots: []string{}, Affected: []string{}, AffectedAlerts: []Alert{}}
		}
		wide.RootAlerts = append(wide.RootAlerts, a)
		if wide.StartsAt.IsZero() || a.StartsAt.Before(wide.StartsAt) {
			wide.StartsAt = a.StartsAt
		}
	}
	if wide == nil {
		return alerts, nil
	}
	rest := make([]Alert, 0, len(alerts))
	for _, a := range alerts {
		switch {
		case a.Service == "":
		case a.Name == rules.AlertServiceTelemetryMissing:
			wide.Affected = appendUnique(wide.Affected, a.Service)
			wide.AffectedAlerts = append(wide.AffectedAlerts, a)
		default:
			rest = append(rest, a)
		}
	}
	sort.Strings(wide.Affected)
	sortAlerts(wide.RootAlerts)
	sortAlerts(wide.AffectedAlerts)
	return rest, wide
}

// IncidentOf finds the incident of a service; "" finds the system-wide one.
func IncidentOf(incidents []Incident, service string) *Incident {
	if service == "" {
		for i := range incidents {
			if strings.HasSuffix(incidents[i].ID, ":"+SystemRoot) {
				return &incidents[i]
			}
		}
		return nil
	}
	for i := range incidents {
		for _, r := range incidents[i].Roots {
			if r == service {
				return &incidents[i]
			}
		}
		for _, a := range incidents[i].Affected {
			if a == service {
				return &incidents[i]
			}
		}
	}
	return nil
}

func sortAlerts(a []Alert) {
	sort.SliceStable(a, func(i, j int) bool {
		if a[i].Service != a[j].Service {
			return a[i].Service < a[j].Service
		}
		if !a[i].StartsAt.Equal(a[j].StartsAt) {
			return a[i].StartsAt.Before(a[j].StartsAt)
		}
		return a[i].Name < a[j].Name
	})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
