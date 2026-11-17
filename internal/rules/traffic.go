// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
)

const AlertRequestsStopped = "KamertonRequestsStopped"

const SignalTraffic Signal = "traffic"

// These are not settings: they define what "normal traffic" and "the telemetry
// is flowing" mean.
const (
	// TrafficBaselineRange: a day survives gaps in the data. The average uses only
	// steps with data, so an empty hour neither lowers the normal nor removes it.
	TrafficBaselineRange = 24 * time.Hour
	// TrafficBaselineStep: the normal averages rates over windows of this length.
	TrafficBaselineStep = 10 * time.Minute
	TrafficMinHistory   = time.Hour
	// ExpectedWindow: traffic is expected now when every 10-minute step of the
	// window centred on this time a week ago had data and their average is above
	// the floor. Every step, so that a start or stop moving by up to half the
	// window is not taken for an outage.
	ExpectedWindow = 2 * time.Hour
	// trafficVisibleShare of the system's services must still have data for the
	// rule to trust a missing series. Below it the telemetry pipeline is down or
	// restarting, and the rule stays silent instead of paging per service.
	trafficVisibleShare = 0.5
	trafficSeenStep     = time.Hour
	expectedWeek        = 7 * 24 * time.Hour
	expectedDay         = 24 * time.Hour
)

// onLabels renders the grouping of a per-service (one label) or system-wide
// (no label) expression.
func onLabels(lbls []string) string { return "(" + strings.Join(lbls, ", ") + ")" }

func sumBy(lbls []string) string {
	if len(lbls) == 0 {
		return "sum"
	}
	return "sum by " + onLabels(lbls)
}

// avgRate is the average of the 10-minute rates of sel over rng, ending offset
// ago, taken only at steps with data, and present only with at least points of them.
func avgRate(sel string, lbls []string, rng, offset time.Duration, points int, indent string) string {
	sub := fmt.Sprintf("(%s (rate(%s[%s])))[%s:%s]", sumBy(lbls), sel, dur(TrafficBaselineStep), dur(rng), dur(TrafficBaselineStep))
	if offset > 0 {
		sub += " offset " + dur(offset)
	}
	return fmt.Sprintf("(\n%[1]s  avg_over_time(%[2]s)\n%[1]sand on %[3]s\n%[1]s  count_over_time(%[2]s) >= %[4]d\n%[1]s)",
		indent, sub, onLabels(lbls), points)
}

// expectedNow is present (per label set of lbls) when sel normally has traffic
// above floor at this time: the window around this time a week ago, or a day
// ago while sel has no data from the day before that week. Its value is that
// average rate per second. History shorter than a day gives nothing.
func expectedNow(sel string, lbls []string, floor float64) string {
	points := int(ExpectedWindow / TrafficBaselineStep)
	week := avgRate(sel, lbls, ExpectedWindow, expectedWeek-ExpectedWindow/2, points, "  ")
	day := avgRate(sel, lbls, ExpectedWindow, expectedDay-ExpectedWindow/2, points, "    ")
	known := fmt.Sprintf("count_over_time((group by %s (%s))[%s:%s] offset %s)", onLabels(lbls), sel, dur(expectedDay), dur(trafficSeenStep), dur(expectedWeek))
	on := onLabels(lbls)
	return fmt.Sprintf("(\n  %s > %s\nor on %s\n  (\n    %s > %s\n  unless on %s\n    %s\n  )\n)",
		week, num(floor), on, day, num(floor), on, known)
}

type TrafficParams struct {
	Meta
	// Target selects incoming calls; Target.Window is the window of the current rate.
	Target SpanTarget
	// Threshold is the share of normal traffic (0..1) at or below which the alert
	// fires; 0 fires only when calls stop completely.
	Threshold float64
	// MinRequestsPerSecond is the normal traffic below which the rule does not
	// apply, and the floor of expectedNow.
	MinRequestsPerSecond float64
	SystemServices       []string
	// Starvation, when set, marks the alert as starved while a feeder fires.
	Starvation *Starvation
}

func (p TrafficParams) normal(indent string) string {
	t := p.Target
	return avgRate(t.matchers().Selector(t.CallsMetric), []string{t.ServiceLabel}, TrafficBaselineRange, 0, int(TrafficMinHistory/TrafficBaselineStep), indent)
}

func (p TrafficParams) expected() string {
	t := p.Target
	return expectedNow(t.matchers().Selector(t.CallsMetric), []string{t.ServiceLabel}, p.MinRequestsPerSecond)
}

func (p TrafficParams) visible(offset time.Duration) string {
	t := p.Target
	svc := t.ServiceLabel
	all := Matchers{}.Re(svc, quoteAll(p.SystemServices)...).Selector(t.CallsMetric)
	off := ""
	if offset > 0 {
		off = " offset " + dur(offset)
	}
	return fmt.Sprintf("  count(sum by (%s) (rate(%s[%s]%s)))\n>= %s * count(count_over_time((group by (%s) (%s))[%s:%s]%s))",
		svc, all, dur(t.Window), off, num(trafficVisibleShare), svc, all, dur(TrafficBaselineRange), dur(trafficSeenStep), off)
}

// stopsCovered is present while a service without any call now would be
// reported by TrafficStopped: ServiceNoData steps aside for it.
func (p TrafficParams) stopsCovered() string {
	svc := p.Target.ServiceLabel
	return fmt.Sprintf("(\n  %s > %s\nand on (%s)\n  %s\nand on ()\n(\n%s\n)\nand on ()\n(\n%s\n)\n)",
		p.normal("  "), num(p.MinRequestsPerSecond), svc, indentLines(p.expected(), "  "), p.visible(0), p.visible(p.Target.Window))
}

func indentLines(s, indent string) string { return strings.ReplaceAll(s, "\n", "\n"+indent) }

// TrafficStopped sums normal traffic over all series of the service, so a
// collector restart that re-creates series does not reset it. No calls at all
// (a flat counter or no series, which the Collector drops 2m after the last
// span) count only while expectedNow holds: an idle hour is not an outage.
// The value is in calls per minute.
func TrafficStopped(p TrafficParams) rulefmt.Rule {
	t := p.Target
	svc := t.ServiceLabel
	normal := p.normal("    ")
	current := t.callRate(t.matchers(), []string{svc}, t.Window)
	expr := fmt.Sprintf("60 * (\n  (\n    %s > 0\n  or on (%s)\n    0 * %s\n  )\n  <= on (%s)\n  %s * (%s > %s)\n)\nand on ()\n(\n%s\n)\nand on ()\n(\n%s\n)",
		current, svc, indentLines(p.expected(), "    "), svc, num(p.Threshold), normal, num(p.MinRequestsPerSecond),
		p.visible(0), p.visible(t.Window))
	expr = p.Starvation.wrap(expr)

	r := p.rule(AlertRequestsStopped, SignalTraffic, expr, nil)
	minPerMinute := num(round(p.MinRequestsPerSecond*60, 2))
	p.annotate(&r,
		fmt.Sprintf("Requests stopped arriving: %s", p.Service),
		fmt.Sprintf(`Incoming calls fell to {{ $value | printf "%%.1f" }}/min over the last %s, at or below %s of the normal rate (the average over the last day where data exists; the rule applies when the normal rate is above %s/min). No calls at all count only at hours that had traffic a week ago (a day ago in the first week).`,
			dur(t.Window), percent(p.Threshold), minPerMinute))
	ph := p.phrases()
	value := `{{ $value | printf "%.1f" }} ` + ph.perMinute
	// The normal rate is added with a query of the same expression; a query
	// can only be embedded as a raw string, which cannot contain a backtick.
	normalQuery := "60 * " + normal
	if !strings.Contains(normalQuery, "`") {
		value += " (" + ph.normally + " {{ range query `" + normalQuery + "` }}{{ . | value | printf \"%.1f\" }}{{ end }})"
	}
	threshold := percent(p.Threshold) + " " + ph.ofNormal
	if p.Threshold == 0 {
		threshold = ph.noneAtAll
	}
	p.explain(&r, "", value, threshold)
	return r
}
