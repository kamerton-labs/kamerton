// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package rulediff says in words what a change of a description does to its alerting rules.
// A description changes by a line; this is what a reviewer needs next to that line.
package rulediff

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/prometheus/prometheus/model/rulefmt"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// Line is one rule: Rule is "service · alert", What the change or a short description.
type Line struct {
	Rule string `json:"rule"`
	What string `json:"what,omitempty"`
}

type Result struct {
	System string `json:"system"`
	// New: there was no description before.
	New     bool   `json:"new"`
	Total   int    `json:"total"`
	Changed []Line `json:"changed"`
	Added   []Line `json:"added"`
	Removed []Line `json:"removed"`
}

// Any reports whether the rules differ.
func (r Result) Any() bool { return r.New || len(r.Changed)+len(r.Added)+len(r.Removed) > 0 }

// Compare generates the rules of both descriptions and compares them; before nil means a new system.
func Compare(before, after *v1alpha1.SystemDescription) (Result, error) {
	a, err := byKey(after)
	if err != nil {
		return Result{}, err
	}
	res := Result{System: after.Name, Total: len(a), New: before == nil, Changed: []Line{}, Added: []Line{}, Removed: []Line{}}
	var b map[string]rulefmt.Rule
	if before != nil {
		if b, err = byKey(before); err != nil {
			return Result{}, fmt.Errorf("the description before the change: %w", err)
		}
	}
	for k, ar := range a {
		br, ok := b[k]
		if !ok {
			if before != nil {
				res.Added = append(res.Added, Line{Rule: k, What: brief(ar)})
			}
			continue
		}
		if d := changes(br, ar); len(d) > 0 {
			res.Changed = append(res.Changed, Line{Rule: k, What: strings.Join(d, "; ")})
		}
	}
	for k, br := range b {
		if _, ok := a[k]; !ok {
			res.Removed = append(res.Removed, Line{Rule: k, What: brief(br)})
		}
	}
	for _, l := range [][]Line{res.Changed, res.Added, res.Removed} {
		sort.Slice(l, func(i, j int) bool { return l[i].Rule < l[j].Rule })
	}
	return res, nil
}

// Print writes the result as text, for a terminal or a CI summary.
func (r Result) Print(w io.Writer) {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	switch {
	case r.New:
		say("System %s is new: %d rules.\n", r.System, r.Total)
	case !r.Any():
		say("System %s: no rule changes (%d rules).\n", r.System, r.Total)
		return
	default:
		say("System %s: %d changed, %d added, %d removed (%d rules after).\n", r.System, len(r.Changed), len(r.Added), len(r.Removed), r.Total)
	}
	section := func(title string, lines []Line, sep string) {
		if len(lines) == 0 {
			return
		}
		say("\n%s\n", title)
		for _, l := range lines {
			if l.What == "" {
				say("- %s\n", l.Rule)
			} else {
				say("- %s%s%s\n", l.Rule, sep, l.What)
			}
		}
	}
	section("Changed", r.Changed, ": ")
	section("Added", r.Added, " ")
	section("Removed", r.Removed, " ")
}

// byKey names every rule by service and alert; a service with several rules of one alert gets a counter.
func byKey(sd *v1alpha1.SystemDescription) (map[string]rulefmt.Rule, error) {
	b, err := generator.Generate(sd, generator.Options{})
	if err != nil {
		return nil, err
	}
	out := map[string]rulefmt.Rule{}
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			svc := r.Labels[rules.LabelService]
			if svc == "" {
				svc = "(system)"
			}
			key := svc + " · " + r.Alert
			for n := 2; ; n++ {
				if _, taken := out[key]; !taken {
					break
				}
				key = fmt.Sprintf("%s · %s #%d", svc, r.Alert, n)
			}
			out[key] = r
		}
	}
	return out, nil
}

func brief(r rulefmt.Rule) string {
	var parts []string
	if t := r.Annotations[rules.AnnotationThreshold]; t != "" {
		parts = append(parts, "threshold "+t)
	}
	if r.For != 0 {
		parts = append(parts, "for "+r.For.String())
	}
	if s := r.Labels[rules.LabelSeverity]; s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// changes words what a person set first, and falls back to "the expression changed".
func changes(b, a rulefmt.Rule) []string {
	var out []string
	pair := func(what, x, y string) {
		if x != y {
			out = append(out, fmt.Sprintf("%s %s -> %s", what, orNone(x), orNone(y)))
		}
	}
	pair("threshold", b.Annotations[rules.AnnotationThreshold], a.Annotations[rules.AnnotationThreshold])
	pair("for", b.For.String(), a.For.String())
	pair("severity", b.Labels[rules.LabelSeverity], a.Labels[rules.LabelSeverity])
	if len(out) == 0 && b.Expr != a.Expr {
		out = append(out, "the expression changed")
	}
	if len(out) == 0 && (!same(b.Labels, a.Labels) || !same(b.Annotations, a.Annotations)) {
		out = append(out, "labels or message text changed")
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func same(x, y map[string]string) bool {
	if len(x) != len(y) {
		return false
	}
	for k, v := range x {
		if y[k] != v {
			return false
		}
	}
	return true
}
