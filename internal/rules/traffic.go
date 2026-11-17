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
	// trafficVisibleShare of the system's services must still have data for the
	// rule to trust a missing series. Below it the telemetry pipeline is down or
	// restarting, and the rule stays silent instead of paging per service.
	trafficVisibleShare = 0.5
	trafficSeenStep     = time.Hour
)

type TrafficParams struct {
	Meta
	// Target selects incoming calls; Target.Window is the window of the current rate.
	Target SpanTarget
	// Threshold is the share of normal traffic (0..1) at or below which the alert
	// fires; 0 fires only when calls stop completely.
	Threshold float64
	// MinRequestsPerSecond is the normal traffic below which the rule does not apply.
	MinRequestsPerSecond float64
	SystemServices       []string
	// PresentWithin, when set, requires some span-metric series of the service
	// to have a sample within it: a service without any data is left to
	// ServiceNoData with the same window. Zero: an absent series counts as zero.
	PresentWithin time.Duration
}

// TrafficStopped counts an absent series as zero. Normal traffic is summed over
// all series of the service, so a collector restart that re-creates series does
// not reset it. The value is in calls per minute.
func TrafficStopped(p TrafficParams) rulefmt.Rule {
	t := p.Target
	svc := t.ServiceLabel
	calls := t.matchers().Selector(t.CallsMetric)
	sub := fmt.Sprintf("(sum by (%s) (rate(%s[%s])))[%s:%s]", svc, calls, dur(TrafficBaselineStep), dur(TrafficBaselineRange), dur(TrafficBaselineStep))
	points := int(TrafficMinHistory / TrafficBaselineStep)
	normal := fmt.Sprintf("(\n      avg_over_time(%s)\n    and on (%s)\n      count_over_time(%s) >= %d\n    )", sub, svc, sub, points)
	current := t.callRate(t.matchers(), []string{svc}, t.Window)

	all := Matchers{}.Re(svc, quoteAll(p.SystemServices)...).Selector(t.CallsMetric)
	visible := func(offset string) string {
		return fmt.Sprintf("  count(sum by (%s) (rate(%s[%s]%s)))\n>= %s * count(count_over_time((group by (%s) (%s))[%s:%s]%s))",
			svc, all, dur(t.Window), offset, num(trafficVisibleShare), svc, all, dur(TrafficBaselineRange), dur(trafficSeenStep), offset)
	}
	expr := fmt.Sprintf("60 * (\n  (\n    %s\n  or on (%s)\n    0 * %s\n  )\n  <= on (%s)\n  %s * (%s > %s)\n)\nand on ()\n(\n%s\n)\nand on ()\n(\n%s\n)",
		current, svc, normal, svc, num(p.Threshold), normal, num(p.MinRequestsPerSecond),
		visible(""), visible(" offset "+dur(t.Window)))
	if p.PresentWithin > 0 {
		expr += fmt.Sprintf("\nand on (%s)\n  group by (%s) (%s)", svc, svc, t.present(t.serviceSpans(t.ServiceValue), p.PresentWithin))
	}

	r := p.rule(AlertRequestsStopped, SignalTraffic, expr, nil)
	minPerMinute := num(round(p.MinRequestsPerSecond*60, 2))
	p.annotate(&r,
		fmt.Sprintf("Requests stopped arriving: %s", p.Service),
		fmt.Sprintf(`Incoming calls fell to {{ $value | printf "%%.1f" }}/min over the last %s, at or below %s of the normal rate (the average over the last day where data exists; the rule applies when the normal rate is above %s/min).`,
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
