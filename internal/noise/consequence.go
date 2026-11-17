// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
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
	deps     map[string][]string
	sources  map[string][]Episode
	systemND []Episode
}

func newInhibition(sd *v1alpha1.SystemDescription, eps []Episode) *inhibition {
	in := &inhibition{deps: map[string][]string{}, sources: map[string][]Episode{}}
	if sd.Spec.Notifications.GroupsByCause() {
		for dep, callers := range triage.NewGraph(&sd.Spec).MutingEdges() {
			for _, c := range callers {
				in.deps[c] = append(in.deps[c], dep)
			}
		}
	}
	for _, e := range eps {
		name, svc := e.Labels["alertname"], e.Labels[rules.LabelService]
		switch {
		case name == rules.AlertTelemetryMissing:
			in.systemND = append(in.systemND, e)
		case svc != "" && rules.IsSymptomSource(name):
			in.sources[svc] = append(in.sources[svc], e)
		}
	}
	return in
}

// overlaps: episodes cover (Start, End].
func overlaps(e Episode, start, end time.Time) bool {
	return e.Start.Before(end) && start.Before(e.End)
}

// muted reports whether Alertmanager muted a firing of this alert between
// start and end. A warning source mutes only warnings, as in inhibit.go.
func (in *inhibition) muted(service, alert, severity string, start, end time.Time) bool {
	if alert == rules.AlertServiceTelemetryMissing {
		for _, s := range in.systemND {
			if overlaps(s, start, end) {
				return true
			}
		}
		return false
	}
	if service == "" || !rules.IsSymptomTarget(alert) {
		return false
	}
	for _, d := range in.deps[service] {
		for _, s := range in.sources[d] {
			if (s.Labels[rules.LabelSeverity] == string(v1alpha1.SeverityCritical) || severity == string(v1alpha1.SeverityWarning)) && overlaps(s, start, end) {
				return true
			}
		}
	}
	return false
}

func (in *inhibition) mutedEpisode(e Episode) bool {
	return in.muted(e.Labels[rules.LabelService], e.Labels["alertname"], e.Labels[rules.LabelSeverity], e.Start, e.End)
}
