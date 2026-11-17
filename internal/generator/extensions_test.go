// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strings"
	"testing"

	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

const extended = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
  annotations:
    kamerton.dev/layout: '{"api":[0,0]}'
spec:
  services:
    - name: api
      type: http
      critical: true
      errorRate: {severity: warning}
      latency: {thresholdMs: 2000}
      saturation:
        memory: 512Mi
        restartsEnabled: false
        severity: warning
      customRules:
        - name: orders-stuck
          title: Заказы не обрабатываются
          condition: {metric: orders_pending, labels: {shop: main}, operator: gt, threshold: 100}
          unit: шт
        - name: no-traffic
          title: Нет трафика
          expr: sum(rate(http_requests_total[5m])) == 0
          for: 10m
          severity: warning
        - name: unused
          title: Выключено
          enabled: false
          expr: up == 0
`

func TestSeverityRestartsAndCustomRules(t *testing.T) {
	res := description.Validate([]byte(extended))
	if !res.Valid {
		t.Fatalf("invalid: %+v", res.Errors)
	}
	b, err := Generate(res.Document, Options{RunbookBaseURL: "http://k"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]string{}
	exprs := map[string]string{}
	fors := map[string]string{}
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			got[r.Alert] = r.Labels
			exprs[r.Alert] = r.Expr
			fors[r.Alert] = r.For.String()
			if r.Annotations[rules.AnnotationTitle] == "" {
				t.Errorf("%s has no plain-words title", r.Alert)
			}
		}
	}
	if got[rules.AlertErrorRate]["severity"] != "warning" {
		t.Errorf("error rate severity override ignored: %v", got[rules.AlertErrorRate])
	}
	if got[rules.AlertLatency]["severity"] != "critical" {
		t.Errorf("latency keeps the service severity: %v", got[rules.AlertLatency])
	}
	if got[rules.AlertMemory]["severity"] != "warning" {
		t.Errorf("saturation severity: %v", got[rules.AlertMemory])
	}
	if _, ok := got[rules.AlertRestarts]; ok {
		t.Error("restarts alert must be off with restartsEnabled: false")
	}
	stuck := got["KamertonCustomOrdersStuck"]
	if stuck == nil || stuck["severity"] != "critical" || stuck[rules.LabelRule] != "orders-stuck" || stuck["kamerton_system"] != "shop" || stuck["service"] != "api" {
		t.Errorf("custom rule labels: %v", stuck)
	}
	if exprs["KamertonCustomOrdersStuck"] != `max (orders_pending{shop="main"}) > 100` || fors["KamertonCustomOrdersStuck"] != "2m" {
		t.Errorf("custom condition: %s for %s", exprs["KamertonCustomOrdersStuck"], fors["KamertonCustomOrdersStuck"])
	}
	if got["KamertonCustomNoTraffic"]["severity"] != "warning" || fors["KamertonCustomNoTraffic"] != "10m" {
		t.Errorf("raw custom rule: %v %s", got["KamertonCustomNoTraffic"], fors["KamertonCustomNoTraffic"])
	}
	if _, ok := got["KamertonCustomUnused"]; ok {
		t.Error("disabled custom rule generated")
	}
	if !strings.Contains(string(b.RulesYAML), "runbook_url: http://k/runbooks/api/KamertonCustomOrdersStuck") {
		t.Error("custom rule has no runbook link")
	}
	if !strings.Contains(b.Runbooks["api.md"], "KamertonCustomNoTraffic") {
		t.Error("runbook does not list custom rules")
	}
}
