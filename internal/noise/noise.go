// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package noise measures how often rules fired and cleared by themselves,
// suggests quieter thresholds and keeps people's "useful / noise" verdicts.
// It never changes a description: the person applies a suggestion.
package noise

import (
	"fmt"
	"sort"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

const (
	DefaultWindow = 7 * 24 * time.Hour
	// Step matches the backtest and the usual 1m rule evaluation interval,
	// and keeps 7 days at 10080 points, under Prometheus' 11000 limit.
	Step = time.Minute
	// ShortUnder: a notification arrives within about a minute, so a firing
	// that cleared sooner than this was gone by the time someone looked.
	// Durations are measured to the step.
	ShortUnder = 5 * time.Minute
	// LongFrom: a firing at least this long is real by any measure, and a
	// suggestion must still catch every one of them. Twice ShortUnder leaves
	// a margin between what is dropped and what must be kept.
	LongFrom = 10 * time.Minute

	// Without verdicts: at least NoisyMinFirings, NoisyMinFiringsPerDay a day
	// (once a shift) and NoisyShortShare short. A bad day of short real
	// outages stays below; a rule flapping tens of times a week does not.
	NoisyMinFirings       = 10
	NoisyMinFiringsPerDay = 3
	NoisyShortShare       = 0.75
	// A rule is noisy by people when at least NoisyMinVotes said "noise" and
	// they outnumber "useful"; the same number of "useful" votes outnumbering
	// "noise" overrules the history.
	NoisyMinVotes = 2

	// MinShortRemovedShare: a suggestion must remove at least this share of
	// the short firings.
	MinShortRemovedShare = 0.8
	// MinReproducedShare: the backtest of the rule as it is must find at
	// least this share of the real firings, else the metrics behind it are
	// gone or renamed and every candidate would look perfect.
	MinReproducedShare = 0.5
	// MaxSuggestedRules bounds one request: each rule costs at most
	// 1 + maxThresholdCandidates range queries.
	MaxSuggestedRules      = 5
	maxThresholdCandidates = 4
	maxForCandidates       = 4
	// MaxSuggestedFor: a longer pending period delays every page by as much.
	MaxSuggestedFor = 10 * time.Minute
	// MaxSuggestedForCritical: with evaluation and group wait (about a minute
	// together) a critical rule still pages within five minutes, the bound
	// the rule library keeps for its own pending periods.
	MaxSuggestedForCritical = 3 * time.Minute
	// SmallThresholdFactor: a threshold at most 1.5 times the current one (a
	// third lower for "<" rules) still means the same kind of trouble, and
	// unlike a longer "for" it adds no delay, so it is tried first.
	SmallThresholdFactor = 1.5
	// MinFiredForSilence: a candidate that would never have fired is offered
	// only for a rule that fired at least this often; for fewer firings it
	// mostly switches the rule off.
	MinFiredForSilence = 20
)

// Windows are the windows the API accepts, as in the backtest.
var Windows = map[string]time.Duration{"1d": 24 * time.Hour, "3d": 3 * 24 * time.Hour, "7d": 7 * 24 * time.Hour}

const (
	VerdictUseful = "useful"
	VerdictNoise  = "noise"
)

// Key identifies a rule the way the UI joins rule cards and rule states:
// alert name, service and the operation label of a rule written for one
// operation ("" for the aggregated per-operation rule).
type Key struct {
	Alert     string `json:"alert"`
	Service   string `json:"service"`
	Operation string `json:"operation"`
}

func (k Key) String() string { return k.Alert + "/" + k.Service + "/" + k.Operation }

// KeyOf maps alert labels to the rule: the aggregated per-operation rules
// put the operation of each series into the label, but the rule card they
// belong to has none.
func KeyOf(sd *v1alpha1.SystemDescription, alert, service, operation string) Key {
	k := Key{Alert: alert, Service: service, Operation: operation}
	if operation == "" || (alert != rules.AlertOperationErrorRate && alert != rules.AlertOperationLatency) {
		return k
	}
	if sd != nil {
		for _, svc := range sd.Spec.Services {
			if svc.Name != service {
				continue
			}
			for _, op := range svc.Operations {
				if op.Name != operation {
					continue
				}
				if (alert == rules.AlertOperationErrorRate && op.ErrorRate != nil) || (alert == rules.AlertOperationLatency && op.Latency != nil) {
					return k
				}
			}
		}
	}
	k.Operation = ""
	return k
}

type Counts struct {
	Useful int `json:"useful"`
	Noise  int `json:"noise"`
}

// Episode is one firing of one alert series.
type Episode struct {
	Labels map[string]string
	Start  time.Time
	End    time.Time
	// Clipped: it was already firing when the window began. Ongoing: still
	// firing at its end. Neither has a known length, so neither is short.
	Clipped bool
	Ongoing bool
}

func (e Episode) Duration() time.Duration { return e.End.Sub(e.Start) }

func (e Episode) Short() bool { return !e.Clipped && !e.Ongoing && e.Duration() < ShortUnder }

func (e Episode) Long() bool { return e.Duration() >= LongFrom }

type Stats struct {
	Fired       int
	Short       int
	Longest     time.Duration
	LastFiredAt time.Time
}

func Summarize(eps []Episode) Stats {
	var s Stats
	for _, e := range eps {
		s.Fired++
		if e.Short() {
			s.Short++
		}
		s.Longest = max(s.Longest, e.Duration())
		if e.Start.After(s.LastFiredAt) {
			s.LastFiredAt = e.Start
		}
	}
	return s
}

// Noisy is the one definition of a noisy rule; see the constants above.
func Noisy(s Stats, fb Counts, window time.Duration) bool {
	if fb.Useful >= NoisyMinVotes && fb.Useful > fb.Noise {
		return false
	}
	if fb.Noise >= NoisyMinVotes && fb.Noise > fb.Useful {
		return true
	}
	days := window.Hours() / 24
	return s.Fired >= NoisyMinFirings && float64(s.Fired) >= NoisyMinFiringsPerDay*days && float64(s.Short) >= NoisyShortShare*float64(s.Fired)
}

type Report struct {
	Window     string       `json:"window"`
	Available  bool         `json:"available"`
	Reason     string       `json:"reason"`
	ShortUnder string       `json:"shortUnder"`
	LongFrom   string       `json:"longFrom"`
	Rules      []RuleReport `json:"rules"`
}

type RuleReport struct {
	Key
	Fired int `json:"fired"`
	Short int `json:"short"`
	// Consequences are firings Alertmanager muted while a dependency's (or
	// the whole system's) alert fired; they are not in Fired or Short.
	Consequences   int         `json:"consequences"`
	LongestSeconds int64       `json:"longestSeconds"`
	LastFiredAt    *time.Time  `json:"lastFiredAt,omitempty"`
	Feedback       Counts      `json:"feedback"`
	Noisy          bool        `json:"noisy"`
	Suggestion     *Suggestion `json:"suggestion,omitempty"`
}

// Suggestion: Current and Suggested are written exactly as the description
// field at Path (relative to the service) takes them.
type Suggestion struct {
	Field     string `json:"field"`
	Current   string `json:"current"`
	Suggested string `json:"suggested"`
	Unit      string `json:"unit"`
	WouldFire int    `json:"wouldFire"`
	WouldMiss int    `json:"wouldMiss"`
	// WouldShort counts the short firings that would remain.
	WouldShort int `json:"wouldShort"`
	// DelaySeconds: how much later a real failure would be reported.
	DelaySeconds int64  `json:"delaySeconds"`
	Path         string `json:"path"`
	Format       string `json:"format"`
}

// ParseWindow accepts the names in Windows; empty means DefaultWindow.
func ParseWindow(s string) (time.Duration, string, error) {
	if s == "" {
		return DefaultWindow, "7d", nil
	}
	if d, ok := Windows[s]; ok {
		return d, s, nil
	}
	return 0, "", fmt.Errorf("window must be 1d, 3d or 7d, got %q", s)
}

func newReport(window string) *Report {
	return &Report{Window: window, ShortUnder: rules.FormatDuration(ShortUnder), LongFrom: rules.FormatDuration(LongFrom), Rules: []RuleReport{}}
}

// Unavailable is the report when the history cannot be read: rules with
// feedback are still listed.
func Unavailable(window, reason string, feedback map[Key]Counts) *Report {
	r := newReport(window)
	r.Reason = reason
	for k, c := range feedback {
		r.Rules = append(r.Rules, RuleReport{Key: k, Feedback: c, Noisy: Noisy(Stats{}, c, 0)})
	}
	sortRules(r.Rules)
	return r
}

func sortRules(rs []RuleReport) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Fired != rs[j].Fired {
			return rs[i].Fired > rs[j].Fired
		}
		return rs[i].String() < rs[j].String()
	})
}
