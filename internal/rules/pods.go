// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
)

const AlertNoReadyPods = "KamertonNoReadyPods"

const SignalAvailability Signal = "availability"

// PodsGoneRange: with no pod left, the alert lasts this long after the last
// ready pod. Metrics cannot tell a failure from a deliberate scale to zero, so
// a service kept at zero stops alerting after it and never alerts when new.
const PodsGoneRange = time.Hour

type NoReadyPodsParams struct {
	Meta
	// Target.ReadyMetric is kube-state-metrics' kube_pod_status_ready: one
	// series per pod and condition, 1 on the current one.
	Target PodTarget
}

// NoReadyPods fires while pods exist and none is ready, or when no pod is left
// within PodsGoneRange of the last ready one; the value is the number of pods.
// A deleted pod's series ends at the next scrape (staleness marker), not 5m later.
func NoReadyPods(p NoReadyPodsParams) rulefmt.Rule {
	t := p.Target
	ns := t.NamespaceLabel
	ready := t.matchers().Eq("condition", "true").Selector(t.ReadyMetric)
	readyNow := fmt.Sprintf("sum by (%s) (%s)", ns, ready)
	pods := fmt.Sprintf("count by (%s) (%s)", ns, ready)
	expr := fmt.Sprintf("(\n    %s\n  unless on (%s)\n    %s > 0\n)\nor on (%s)\n(\n    0 * (max_over_time((%s)[%s:1m]) > 0)\n  unless on (%s)\n    %s\n)",
		pods, ns, readyNow, ns, readyNow, dur(PodsGoneRange), ns, pods)
	r := p.rule(AlertNoReadyPods, SignalAvailability, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("No ready pods: %s", p.Service),
		fmt.Sprintf(`None of the {{ $value | printf "%%.0f" }} pods of %s in namespace %s is ready (0 pods: all are gone within %s of the last ready one). Callers of the service are failing because of it.`,
			p.Service, labelRef(ns), dur(PodsGoneRange)))
	ph := p.phrases()
	p.explain(&r, "", fmt.Sprintf(ph.readyOf, `{{ $value | printf "%.0f" }}`), ph.anyReady)
	return r
}
