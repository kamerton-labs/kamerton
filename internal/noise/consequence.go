// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"slices"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// inhibition replays the inhibit rules of generator/inhibit.go on the alert
// history, which knows nothing about muting. Direct links are enough: a muted
// source still mutes its own callers, so chains follow from the history.
type inhibition struct {
	// deps maps a caller to the dependencies whose symptom sources mute it.
	deps    map[string][]string
	sources map[string][]Episode
	// feeders and feeding replay the starvation rules: a starved alert is
	// muted while an alert of a feeder fires (rules.LabelStarved).
	feeders  map[string][]string
	feeding  map[string][]Episode
	systemND []Episode
}

func newInhibition(sd *v1alpha1.SystemDescription, eps []Episode) *inhibition {
	in := &inhibition{deps: map[string][]string{}, sources: map[string][]Episode{}, feeders: map[string][]string{}, feeding: map[string][]Episode{}}
	if sd.Spec.Notifications.GroupsByCause() {
		g := triage.NewGraph(&sd.Spec)
		for dep, callers := range g.MutingEdges() {
			for _, c := range callers {
				in.deps[c] = append(in.deps[c], dep)
			}
		}
		for svc, fs := range g.Feeders() {
			for _, f := range fs {
				in.feeders[svc] = append(in.feeders[svc], f.Service)
			}
		}
	}
	for _, e := range eps {
		name, svc := e.Labels["alertname"], e.Labels[rules.LabelService]
		if name == rules.AlertTelemetryMissing {
			in.systemND = append(in.systemND, e)
			continue
		}
		if svc == "" {
			continue
		}
		if slices.Contains(rules.FeederAlerts(), name) {
			in.feeding[svc] = append(in.feeding[svc], e)
		}
		if rules.IsSymptomSource(name) && e.Labels[rules.LabelStarved] != "true" {
			in.sources[svc] = append(in.sources[svc], e)
		}
	}
	return in
}

// mutes applies the severity rule of inhibit.go: a warning source mutes only warnings.
func mutes(source Episode, severity string) bool {
	return source.Labels[rules.LabelSeverity] == string(v1alpha1.SeverityCritical) || severity == string(v1alpha1.SeverityWarning)
}

// overlaps: episodes cover (Start, End].
func overlaps(e Episode, start, end time.Time) bool {
	return e.Start.Before(end) && start.Before(e.End)
}

// muted reports whether Alertmanager muted a firing of this alert between
// start and end. A warning source mutes only warnings, as in inhibit.go.
func (in *inhibition) muted(service, alert, severity string, starved bool, start, end time.Time) bool {
	if alert == rules.AlertServiceTelemetryMissing {
		for _, s := range in.systemND {
			if overlaps(s, start, end) {
				return true
			}
		}
	}
	if service == "" {
		return false
	}
	if starved && slices.Contains(rules.StarvedAlerts(), alert) {
		for _, f := range in.feeders[service] {
			for _, s := range in.feeding[f] {
				if mutes(s, severity) && overlaps(s, start, end) {
					return true
				}
			}
		}
	}
	if !rules.IsSymptomTarget(alert) {
		return false
	}
	for _, d := range in.deps[service] {
		for _, s := range in.sources[d] {
			if mutes(s, severity) && overlaps(s, start, end) {
				return true
			}
		}
	}
	return false
}

func (in *inhibition) mutedEpisode(e Episode) bool {
	return in.muted(e.Labels[rules.LabelService], e.Labels["alertname"], e.Labels[rules.LabelSeverity], e.Labels[rules.LabelStarved] == "true", e.Start, e.End)
}
