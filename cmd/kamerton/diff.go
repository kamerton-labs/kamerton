// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/prometheus/prometheus/model/rulefmt"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// diff prints which alerting rules a change of a description changes, for a review: the
// description changes by a line, and this says what that line does.
func diff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	file := fs.String("f", "", "the description after the change")
	against := fs.String("against", "", "the description before the change; empty or missing file: the system is new")
	exitCode := fs.Bool("exit-code", false, "exit with status 2 when the rules differ")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("diff: -f is required")
	}
	after, name, err := rulesOf(*file)
	if err != nil {
		return err
	}
	var before map[string]rulefmt.Rule
	if *against != "" {
		if _, statErr := os.Stat(*against); statErr == nil {
			if before, _, err = rulesOf(*against); err != nil {
				return fmt.Errorf("the description before the change: %w", err)
			}
		}
	}
	if printDiff(os.Stdout, name, before, after) && *exitCode {
		os.Exit(2)
	}
	return nil
}

func rulesOf(file string) (map[string]rulefmt.Rule, string, error) {
	sd, _, err := description.LoadFile(file)
	if err != nil {
		return nil, "", err
	}
	return rulesByKey(sd)
}

// rulesByKey names every rule by service and alert; a service with several rules of one alert
// gets a counter.
func rulesByKey(sd *v1alpha1.SystemDescription) (map[string]rulefmt.Rule, string, error) {
	b, err := generator.Generate(sd, generator.Options{})
	if err != nil {
		return nil, "", err
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
	return out, sd.Name, nil
}

// printDiff reports whether anything differs.
func printDiff(w io.Writer, system string, before, after map[string]rulefmt.Rule) bool {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	var added, removed, changed []string
	for k, a := range after {
		b, ok := before[k]
		if !ok {
			added = append(added, "- "+k+ruleBrief(a))
			continue
		}
		if d := ruleChanges(b, a); len(d) > 0 {
			changed = append(changed, "- "+k+": "+strings.Join(d, "; "))
		}
	}
	for k, b := range before {
		if _, ok := after[k]; !ok {
			removed = append(removed, "- "+k+ruleBrief(b))
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	if before == nil {
		say("System %s is new: %d rules.\n", system, len(after))
	} else if len(added)+len(removed)+len(changed) == 0 {
		say("System %s: no rule changes (%d rules).\n", system, len(after))
		return false
	} else {
		say("System %s: %d changed, %d added, %d removed (%d rules after).\n", system, len(changed), len(added), len(removed), len(after))
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		say("\n%s\n", title)
		for _, l := range lines {
			say("%s\n", l)
		}
	}
	section("Changed", changed)
	if before != nil {
		section("Added", added)
	}
	section("Removed", removed)
	return true
}

func ruleBrief(r rulefmt.Rule) string {
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
	return " (" + strings.Join(parts, ", ") + ")"
}

// ruleChanges words what a person set first, and falls back to "the expression changed".
func ruleChanges(b, a rulefmt.Rule) []string {
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
	if len(out) == 0 && (!sameMap(b.Labels, a.Labels) || !sameMap(b.Annotations, a.Annotations)) {
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

func sameMap(x, y map[string]string) bool {
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
