// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStarvedLabel(t *testing.T) {
	s := &Starvation{SystemLabel: "kamerton_system", System: "shop", Service: "email", Feeders: []Feeder{
		{Service: "checkout", Others: []string{"payment", "cart"}},
		{Service: "producer", Queue: true},
	}}
	expr := oneLine(s.wrap(`x{service_name="email"}`))
	if s := (*Starvation)(nil); s.wrap("x") != "x" {
		t.Error("no starvation must leave the expression alone")
	}
	alert := func(svc, name string, labels ...string) string {
		return fmt.Sprintf(`  ALERTS{alertname=%q, service=%q, alertstate="firing", kamerton_system="shop"%s} 1x10`+"\n", name, svc, strings.Join(labels, ""))
	}
	plain, starved := `  {service_name="email"} 5`, `  {service_name="email", kamerton_starved="true"} 5`
	for _, tc := range []struct{ name, alerts, want string }{
		{"no feeder alert", "", plain},
		{"the caller fails because of another dependency", alert("checkout", AlertErrorRate) + alert("payment", AlertNoReadyPods), starved},
		{"the caller fails, nothing else explains it: the service may be the cause", alert("checkout", AlertErrorRate), plain},
		{"the explaining dependency's alert is itself starved", alert("checkout", AlertErrorRate) + alert("payment", AlertRequestsStopped, `, kamerton_starved="true"`), plain},
		{"the caller has no ready pods", alert("checkout", AlertNoReadyPods), starved},
		{"the caller is starved itself: the chain goes on", alert("checkout", AlertServiceTelemetryMissing, `, kamerton_starved="true"`), starved},
		{"a producer of the consumed queue fails", alert("producer", AlertLatency), starved},
		{"the service has errors of its own", alert("checkout", AlertNoReadyPods) + alert("email", AlertErrorRate), plain},
		{"its own restarts do not count", alert("checkout", AlertNoReadyPods) + alert("email", AlertRestarts), starved},
		{"another system", strings.ReplaceAll(alert("checkout", AlertNoReadyPods), `"shop"`, `"other"`), plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runPromQL(t, "load 1m\n  x{service_name=\"email\"} 5x10\n"+tc.alerts+"\neval instant at 5m "+expr+"\n"+tc.want+"\n")
		})
	}
}

func TestStarvedRulesValidate(t *testing.T) {
	s := &Starvation{SystemLabel: "kamerton_system", System: "shop", Service: "checkout", Feeders: []Feeder{{Service: "frontend"}}}
	tp := TrafficParams{Meta: meta(), Target: spanTarget(), Threshold: 0.1, MinRequestsPerSecond: 0.02, SystemServices: []string{"frontend", "checkout"}, Starvation: s}
	nd := ServiceNoData(ServiceNoDataParams{Meta: meta(), Target: spanTarget(), After: 3 * time.Minute, Others: []string{"frontend"}, MinRequestsPerSecond: 0.02, Traffic: &tp, Starvation: s})
	rs := TrafficStopped(tp)
	mustValidate(t, nd, rs)
	for _, r := range []string{nd.Expr, rs.Expr} {
		if !strings.Contains(r, `* on () group_left (kamerton_starved)`) || !strings.Contains(r, `ALERTS{service="frontend", alertname=~`) {
			t.Errorf("expression without the starved label:\n%s", r)
		}
	}
	if strings.Count(nd.Expr, "group_left") != 1 {
		t.Errorf("the traffic rule's terms inside no-data must not carry the label:\n%s", nd.Expr)
	}
}
