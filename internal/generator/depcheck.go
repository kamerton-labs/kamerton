// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

type Check struct {
	Alert string
	Expr  string
}

// DependencyChecks are instant queries that show a service failing now, before
// the pending periods of its alerts end. They use the service's merged
// thresholds even where its rule is turned off, and count vanished series as no
// calls at hours that normally have traffic.
func DependencyChecks(sd *v1alpha1.SystemDescription, svc *v1alpha1.Service) []Check {
	g := newGen(sd, Options{})
	d := g.defaults()
	var out []Check
	if g.tel.Containers.ReadyMetric != description.DisabledMetric {
		r := rules.NoReadyPods(rules.NoReadyPodsParams{Meta: g.meta(svc, 0), Target: g.podTarget(svc)})
		out = append(out, Check{Alert: r.Alert, Expr: r.Expr})
	}
	if g.tel.Spans.CallsMetric == description.DisabledMetric {
		return out
	}
	tp := mergeTraffic(svc, d.TrafficDrop, svc.TrafficDrop)
	traffic := rules.TrafficStopped(rules.TrafficParams{
		Meta: g.meta(svc, 0), Target: g.spanTarget(svc),
		Threshold: tp.threshold, MinRequestsPerSecond: tp.minRPS, SystemServices: g.telemetryNames(),
	})
	ep := mergeErrorRate(d.ErrorRate, svc.ErrorRate)
	errs := rules.ErrorRate(rules.ErrorRateParams{
		Meta: g.meta(svc, 0), Target: g.spanTarget(svc), Threshold: ep.threshold, MinRequestsPerSecond: ep.minRPS,
	})
	return append(out, Check{Alert: traffic.Alert, Expr: traffic.Expr}, Check{Alert: errs.Alert, Expr: errs.Expr})
}
