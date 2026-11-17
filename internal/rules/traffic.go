// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
)

// AlertRequestsStopped fires when a service that normally receives requests
// stops receiving them.
const AlertRequestsStopped = "KamertonRequestsStopped"

// SignalTraffic is the signal of AlertRequestsStopped.
const SignalTraffic Signal = "traffic"

// Fixed parts of the traffic rule. They are not settings: they define what
// "normal traffic" and "the telemetry is flowing" mean.
const (
	// TrafficBaselineRange is how far back normal traffic is looked for.
	// A day survives gaps in the data (a stopped Prometheus, a machine that
	// slept for hours): the average is taken only over the steps that have
	// data, so an empty hour neither lowers the normal nor removes it.
	TrafficBaselineRange = 24 * time.Hour
	// TrafficBaselineStep is the resolution of the normal: the average of
	// rates over consecutive windows of this length.
	TrafficBaselineStep = 10 * time.Minute
	// TrafficMinHistory is the history a service needs before it has a
	// normal. A service that appeared a minute ago has no normal and no alert.
	TrafficMinHistory = time.Hour
	// trafficVisibleShare is the share of the system's services that must
	// still have data (a computable rate, zero included) for the rule to
	// trust a missing series. Below it the telemetry pipeline has stopped or
	// is restarting, and the rule stays silent instead of paging per service.
	trafficVisibleShare = 0.5
	// trafficSeenStep is the resolution of "services seen during the last
	// day", the denominator of trafficVisibleShare.
	trafficSeenStep = time.Hour
)

// TrafficParams parameterize the "requests stopped arriving" template.
type TrafficParams struct {
	Meta
	// Target selects the incoming calls of the service (server and consumer
	// spans, without excluded operations); Target.Window is the window of
	// the current rate.
	Target SpanTarget
	// Threshold is the share of normal traffic (0..1) at or below which the
	// alert fires; 0 fires only when calls stop completely.
	Threshold float64
	// MinRequestsPerSecond is the normal traffic below which the rule does
	// not apply.
	MinRequestsPerSecond float64
	// SystemServices are the service label values of all services of the
	// system. Telemetry is considered flowing while at least half of those
	// seen during the last day still have data.
	SystemServices []string
}

// TrafficStopped renders an alert on a service that stopped receiving calls.
//
// The current rate is the rate of incoming calls over Target.Window; a series
// that is absent counts as zero, like a counter that stopped growing. Normal
// traffic is the average of 10-minute rates over the last day, only where
// data exists, summed over all series of the service (so a collector restart,
// which re-creates every series under new labels, does not reset it), and
// exists only with at least an hour of history. The rule fires when the
// current rate is at or below Threshold × normal and the normal is above
// MinRequestsPerSecond, provided at least half of the system's services seen
// during the last day still have data now and one window ago (otherwise the
// telemetry pipeline itself is down or has just come back, and series are
// missing for everyone).
//
// The value of the alert is the current rate in calls per minute.
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
