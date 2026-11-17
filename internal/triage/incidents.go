// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package triage

import (
	"sort"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/internal/rules"
)

// Incidents group the active alerts of one system by cause.
//
// A symptom alert (rules.IsSymptom) of a service that calls a dependency
// with a symptom alert of its own is a consequence: the generated
// Alertmanager inhibit rules mute it, and here it is listed as "also
// affected" under the dependency. Following such links down to services
// whose dependencies have no symptom alerts gives the roots. Both use the
// same links (MutingEdges), so what the UI calls "also affected" is exactly
// what Alertmanager mutes when grouping by cause is on.
//
// Roots that share an affected service form one incident (checkout fails
// because both cart and payment are down). Alerts that are not symptoms stay
// with their service: in the incident where the service is a root or
// affected, or in an incident of their own.

// Alert is one active alert of a system, as Alertmanager reports it.
type Alert struct {
	Fingerprint string    `json:"fingerprint,omitempty"`
	Name        string    `json:"name"`
	Service     string    `json:"service"`
	Severity    string    `json:"severity"`
	StartsAt    time.Time `json:"startsAt"`
	// Title and Value are the plain-words annotations of the rule.
	Title string `json:"title,omitempty"`
	Value string `json:"value,omitempty"`
	// Muted is true when Alertmanager inhibits the alert (it is not
	// delivered while the cause is firing).
	Muted bool `json:"muted"`
}

// Incident is one cause and its consequences.
type Incident struct {
	// ID is stable while the roots stay the same: system and roots.
	ID     string `json:"id"`
	System string `json:"system"`
	// Roots are the services where the problem probably starts.
	Roots []string `json:"roots"`
	// RootAlerts are the alerts of the roots.
	RootAlerts []Alert `json:"rootAlerts"`
	// Affected are the services whose symptom alerts follow from the roots
	// (muted in Alertmanager when grouping by cause is on).
	Affected []string `json:"affected"`
	// AffectedAlerts are the alerts of the affected services.
	AffectedAlerts []Alert `json:"affectedAlerts"`
	// StartsAt is the start of the earliest root alert.
	StartsAt time.Time `json:"startsAt"`
}

// MutingEdges maps a service to the services that call it directly
// (dependsOn), the callers whose symptom alerts it mutes. Links inside a
// dependency cycle are left out: two services that call each other would
// otherwise mute each other and nobody would be told. Lists are sorted.
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

// components numbers the strongly connected components of the declared
// graph (Tarjan's algorithm, iterative so a long chain cannot overflow the
// stack). Services in one cycle share a number.
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

// Incidents groups the alerts of one system (muted ones included) by cause.
// The result is sorted by start time, then ID.
func (g *Graph) Incidents(system string, alerts []Alert) []Incident {
	callers := g.MutingEdges()
	// deps restricted to muting links: service -> dependencies it is muted by.
	mutedBy := map[string][]string{}
	for dep, cs := range callers {
		for _, c := range cs {
			mutedBy[c] = append(mutedBy[c], dep)
		}
	}
	bySvc := map[string][]Alert{}
	symptom := map[string]bool{}
	var services []string
	for _, a := range alerts {
		if _, seen := bySvc[a.Service]; !seen {
			services = append(services, a.Service)
		}
		bySvc[a.Service] = append(bySvc[a.Service], a)
		if rules.IsSymptom(a.Name) {
			symptom[a.Service] = true
		}
	}
	sort.Strings(services)

	// A symptomatic service is affected when a dependency it is muted by is
	// symptomatic too; otherwise it is a root. Muting links have no cycles
	// (MutingEdges drops them), so walking down always ends.
	isRoot := func(s string) bool {
		for _, d := range mutedBy[s] {
			if symptom[d] {
				return false
			}
		}
		return true
	}
	// rootsOf returns the roots reachable from s over symptomatic links.
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
			if symptom[d] {
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
		if !symptom[s] {
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
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartsAt.Equal(out[j].StartsAt) {
			return out[i].StartsAt.Before(out[j].StartsAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// IncidentOf returns the incident that holds an alert of service, or nil.
func IncidentOf(incidents []Incident, service string) *Incident {
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
