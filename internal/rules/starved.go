// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"slices"
	"strings"
)

// LabelStarved marks a "no data" or "requests stopped" alert of a service
// that has no symptom of its own while a feeder has an alert the service did
// not cause: its traffic is missing because of the feeder. An inhibit rule
// mutes it while the feeder alert fires; it never mutes the service's callers.
const LabelStarved = "kamerton_starved"

// FeederAlerts are the alerts of a feeder that explain missing traffic
// downstream; the starved alerts are among them, so starvation chains.
func FeederAlerts() []string {
	out := slices.Clone(symptomSources)
	for _, a := range append(slices.Clone(symptomTargets), AlertServiceTelemetryMissing) {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func StarvedAlerts() []string { return []string{AlertRequestsStopped, AlertServiceTelemetryMissing} }

// OwnCauseAlerts of a service keep its "no data" and "requests stopped" its
// own, whatever its feeders do.
func OwnCauseAlerts() []string {
	return slices.DeleteFunc(slices.Clone(symptomSources), func(a string) bool { return a == AlertRequestsStopped })
}

// SilentAlerts of a direct caller say it stopped calling for a reason of its
// own: its failing callee cannot have caused them. Its other alerts explain
// a starved callee only while another dependency of the caller explains them.
func SilentAlerts() []string {
	return []string{AlertNoReadyPods, AlertRequestsStopped, AlertServiceTelemetryMissing}
}

type Feeder struct {
	Service string
	// Queue: the feeder produces to a queue the service consumes, so the
	// service cannot be what makes the feeder fail.
	Queue bool
	// Others are the feeder's dependencies, other than the service, whose
	// symptoms mute the feeder's alerts.
	Others []string
}

type Starvation struct {
	// SystemLabel and System select the alerts of the system in ALERTS.
	SystemLabel, System string
	Service             string
	Feeders             []Feeder
}

// wrap adds LabelStarved to expr while a feeder alert that the service did not
// cause fires (ALERTS of other groups lag one evaluation). The value of expr
// is kept.
func (s *Starvation) wrap(expr string) string {
	if s == nil || len(s.Feeders) == 0 {
		return expr
	}
	alerts := func(m Matchers) string {
		return "group(" + m.Eq("alertstate", "firing").Eq(s.SystemLabel, s.System).Selector("ALERTS") + ")"
	}
	of := func(svc string, names []string) string {
		return alerts(Matchers{}.Eq(LabelService, svc).Re("alertname", names...))
	}
	var why []string
	for _, f := range s.Feeders {
		if f.Queue {
			why = append(why, of(f.Service, FeederAlerts()))
			continue
		}
		w := of(f.Service, SilentAlerts())
		if len(f.Others) > 0 {
			explained := alerts(Matchers{}.Re(LabelService, quoteAll(f.Others)...).Re("alertname", symptomSources...).NotRe(LabelStarved, "true"))
			w += fmt.Sprintf("\n  or on ()\n    (%s and on () %s)", of(f.Service, FeederAlerts()), explained)
		}
		why = append(why, w)
	}
	return fmt.Sprintf("(\n%s\n)\n* on () group_left (%s)\n(\n  label_replace(\n    (\n    %s\n    )\n    unless on () %s,\n    %q, \"true\", \"\", \"\"\n  )\nor on ()\n  vector(1)\n)",
		expr, LabelStarved, strings.Join(why, "\n    or on ()\n    "), of(s.Service, OwnCauseAlerts()), LabelStarved)
}
