// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package triage finds the probable source of an incident and groups alerts by cause.
package triage

import (
	"sort"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

type Graph struct {
	deps map[string][]string
}

func NewGraph(spec *v1alpha1.SystemDescriptionSpec) *Graph {
	g := &Graph{deps: make(map[string][]string, len(spec.Services))}
	for _, s := range spec.Services {
		g.deps[s.Name] = append([]string(nil), s.DependsOn...)
	}
	return g
}

type Finding struct {
	Alerting string `json:"alerting"`
	// ProbableSource is the deepest unhealthy dependency whose own dependencies are
	// healthy, or Alerting itself when no dependency is unhealthy.
	ProbableSource string   `json:"probableSource"`
	Chain          []string `json:"chain"`
}

// ProbableSource walks the dependencies of alerting; unhealthy holds services with firing alerts.
func (g *Graph) ProbableSource(alerting string, unhealthy map[string]bool) Finding {
	// BFS gives the shortest path to every reachable service; the visited set
	// makes cycles harmless.
	parent := map[string]string{alerting: ""}
	depth := map[string]int{alerting: 0}
	order := []string{alerting}
	for i := 0; i < len(order); i++ {
		n := order[i]
		deps := append([]string(nil), g.deps[n]...)
		sort.Strings(deps)
		for _, d := range deps {
			if _, seen := parent[d]; seen {
				continue
			}
			parent[d] = n
			depth[d] = depth[n] + 1
			order = append(order, d)
		}
	}

	best := alerting
	for _, n := range order[1:] {
		if !unhealthy[n] || g.hasUnhealthyDependency(n, unhealthy) {
			continue
		}
		if depth[n] > depth[best] || (depth[n] == depth[best] && n < best) {
			best = n
		}
	}
	var chain []string
	for n := best; n != ""; n = parent[n] {
		chain = append([]string{n}, chain...)
	}
	return Finding{Alerting: alerting, ProbableSource: best, Chain: chain}
}

func (g *Graph) hasUnhealthyDependency(n string, unhealthy map[string]bool) bool {
	seen := map[string]bool{n: true}
	stack := append([]string(nil), g.deps[n]...)
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[d] {
			continue
		}
		seen[d] = true
		if unhealthy[d] {
			return true
		}
		stack = append(stack, g.deps[d]...)
	}
	return false
}
