// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
)

const (
	AlertTelemetryMissing        = "KamertonTelemetryMissing"
	AlertServiceTelemetryMissing = "KamertonServiceTelemetryMissing"
)

const SignalNoData Signal = "no_data"

// These are not settings: they define what "sent span metrics before" means.
const (
	// NoDataSeenRange bounds "before": a service that has sent nothing for a
	// day is treated as gone (renamed or removed), so its alert resolves
	// instead of firing forever, and one that never sent anything never fires.
	NoDataSeenRange = 24 * time.Hour
	// NoDataSeenStep: the subquery looks for data every 10 minutes (each step
	// sees the 5 minutes before it), so a service has to send for about a
	// quarter of an hour to count as seen. Raw samples of a day would cost far more.
	NoDataSeenStep = 10 * time.Minute
	// NoDataPending rides out one late flush of the Collector and the first
	// evaluations after a Prometheus restart, before remote write catches up.
	NoDataPending = time.Minute
)

// serviceSpans selects every span-metric series of the given services, of any
// span kind and operation: any of them proves that data arrives.
func (t SpanTarget) serviceSpans(services ...string) string {
	if len(services) == 1 {
		return Matchers{}.Eq(t.ServiceLabel, services[0]).Selector(t.CallsMetric)
	}
	return Matchers{}.Re(t.ServiceLabel, quoteAll(services)...).Selector(t.CallsMetric)
}

func (t SpanTarget) seen(sel string) string {
	return fmt.Sprintf("count_over_time((group by (%s) (%s))[%s:%s])", t.ServiceLabel, sel, dur(NoDataSeenRange), dur(NoDataSeenStep))
}

// present is empty when the selection had no sample within the window. A
// range is used rather than an instant lookup: remote write leaves no
// staleness markers, so an instant lookup would see stopped series for 5 minutes.
func (t SpanTarget) present(sel string, within time.Duration) string {
	return fmt.Sprintf("present_over_time(%s[%s])", sel, dur(within))
}

type SystemNoDataParams struct {
	Meta
	// Target gives the metric and service label; ServiceValue is not used.
	Target SpanTarget
	// After is how long no service may send data before the alert.
	After    time.Duration
	Services []string
	// MinRequestsPerSecond is the floor of the system's total expected traffic.
	MinRequestsPerSecond float64
}

// SystemNoData fires when none of the services has sent span metrics for
// After although some did within NoDataSeenRange, and the system normally has
// traffic at this time. The value is the number of services that did. It has
// no service label.
func SystemNoData(p SystemNoDataParams) rulefmt.Rule {
	t := p.Target
	sel := t.serviceSpans(p.Services...)
	expr := fmt.Sprintf("count(\n  %s\n)\nunless on ()\ngroup(\n  %s\n)\nand on ()\n%s",
		t.seen(sel), t.present(sel, p.After), expectedNow(sel, nil, p.MinRequestsPerSecond))
	m := p.Meta
	m.Service = ""
	m.For = NoDataPending
	r := m.rule(AlertTelemetryMissing, SignalNoData, expr, nil)
	ph := p.phrases()
	m.annotate(&r,
		"No telemetry from the system",
		fmt.Sprintf(`None of the {{ $value | printf "%%.0f" }} services that sent span metrics in the last %s has sent any for %s, at a time when the system normally has traffic. Kamerton cannot see the system: other alerts of this system will not fire until data returns. Check the OpenTelemetry Collector, its remote write to Prometheus and Prometheus itself.`,
			dur(NoDataSeenRange), dur(p.After)))
	m.explain(&r, "", fmt.Sprintf(ph.systemNoData, `{{ $value | printf "%.0f" }}`, dur(p.After)), "")
	return r
}

type ServiceNoDataParams struct {
	Meta
	Target SpanTarget
	After  time.Duration
	// Others are the telemetry names of the other services of the system; at
	// least one of them must still send data.
	Others []string
	// MinRequestsPerSecond is the floor of the service's expected traffic.
	MinRequestsPerSecond float64
	// Traffic is the service's TrafficStopped rule when it is on: a stop it
	// reports is left to it.
	Traffic *TrafficParams
}

// ServiceNoData fires when the service sent span metrics within
// NoDataSeenRange, has sent none for After although it normally has traffic
// at this time, and another service of the system still sends: otherwise the
// pipeline is down and SystemNoData speaks for all.
func ServiceNoData(p ServiceNoDataParams) rulefmt.Rule {
	t := p.Target
	svc := t.ServiceLabel
	self := t.serviceSpans(t.ServiceValue)
	expr := fmt.Sprintf("group by (%s) (\n  %s\n)\nunless on (%s)\n  group by (%s) (%s)\nand on ()\n  group(%s)\nand on (%s)\n%s",
		svc, t.seen(self), svc, svc, t.present(self, p.After), t.present(t.serviceSpans(p.Others...), p.After),
		svc, expectedNow(self, []string{svc}, p.MinRequestsPerSecond))
	if p.Traffic != nil {
		expr += fmt.Sprintf("\nunless on (%s)\n%s", svc, p.Traffic.stopsCovered())
	}
	m := p.Meta
	m.For = NoDataPending
	r := m.rule(AlertServiceTelemetryMissing, SignalNoData, expr, nil)
	m.annotate(&r,
		fmt.Sprintf("No telemetry from %s", p.Service),
		fmt.Sprintf("%s sent span metrics in the last %s and normally has traffic at this time, but has sent none for %s, while other services of the system still do. Kamerton cannot see this service: its other alerts will not fire until data returns.",
			p.Service, dur(NoDataSeenRange), dur(p.After)))
	m.explain(&r, "", fmt.Sprintf(p.phrases().serviceNoData, dur(p.After)), "")
	return r
}
