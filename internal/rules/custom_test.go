// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
)

func TestCustomAlertName(t *testing.T) {
	for in, want := range map[string]string{
		"orders-stuck": "KamertonCustomOrdersStuck",
		"x":            "KamertonCustomX",
		"rule-2":       "KamertonCustomRule2",
	} {
		if got := CustomAlertName(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestCustomConditionExpr(t *testing.T) {
	c := CustomCondition{
		Metric: "http_requests_total", Labels: map[string]string{"service": "shop", "code": `5"00`},
		Function: "rate", Aggregation: "sum", By: []string{"pod"}, Operator: "gte", Threshold: 2.5, Window: 5 * time.Minute,
	}
	want := `sum by (pod) (rate(http_requests_total{code="5\"00", service="shop"}[5m])) >= 2.5`
	if got := c.Expr(); got != want {
		t.Errorf("expr:\n got %s\nwant %s", got, want)
	}
	if err := ValidateExpr(c.Expr()); err != nil {
		t.Errorf("rendered condition does not parse: %v", err)
	}
	plain := CustomCondition{Metric: "queue_depth", Operator: "lt", Threshold: 1}
	if got := plain.Expr(); got != "max (queue_depth) < 1" {
		t.Errorf("plain: %s", got)
	}
}

func TestValidateExpr(t *testing.T) {
	if err := ValidateExpr(`up == 0`); err != nil {
		t.Errorf("valid: %v", err)
	}
	if err := ValidateExpr(`sum(rate(x[5m]) > `); err == nil {
		t.Error("syntax error not reported")
	}
	if err := ValidateExpr(`1 > bool 0`); err == nil || !strings.Contains(err.Error(), "vector") {
		t.Errorf("scalar expression must be rejected: %v", err)
	}
	if err := ValidateExpr(`rate(x[5m])[10m:1m]`); err == nil {
		t.Error("range vector must be rejected")
	}
}

func TestCustomRule(t *testing.T) {
	meta := Meta{Service: "checkout", Severity: "critical", For: 5 * time.Minute, RunbookURL: "http://k/runbooks/checkout", RunbookPerAlert: true,
		Labels: map[string]string{"kamerton_system": "shop"}, Language: LanguageRussian}
	built := Custom(CustomParams{Meta: meta, Name: "orders-stuck", Title: "Заказы не обрабатываются", Unit: "шт",
		Condition: &CustomCondition{Metric: "orders_pending", Operator: "gt", Threshold: 100}})
	raw := Custom(CustomParams{Meta: meta, Name: "no-traffic", Title: "Нет трафика", Expr: `sum(rate(calls_total[5m])) == 0`})

	if built.Alert != "KamertonCustomOrdersStuck" || built.Expr != "max (orders_pending) > 100" {
		t.Errorf("built: %s %s", built.Alert, built.Expr)
	}
	// A condition that drops out for a minute must not resolve the alert and fire it again.
	if time.Duration(built.KeepFiringFor) != KeepFiringFor || time.Duration(raw.KeepFiringFor) != KeepFiringFor {
		t.Errorf("keep_firing_for: %v %v", built.KeepFiringFor, raw.KeepFiringFor)
	}
	for k, v := range map[string]string{LabelService: "checkout", LabelSeverity: "critical", LabelSignal: "custom", LabelManaged: "true", LabelRule: "orders-stuck", "kamerton_system": "shop"} {
		if built.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, built.Labels[k], v)
		}
	}
	if built.Annotations[AnnotationTitle] != "Заказы не обрабатываются" || built.Annotations[AnnotationThreshold] != "> 100 шт" ||
		!strings.HasSuffix(built.Annotations[AnnotationValue], " шт") {
		t.Errorf("annotations: %v", built.Annotations)
	}
	if built.Annotations["runbook_url"] != "http://k/runbooks/checkout/KamertonCustomOrdersStuck" {
		t.Errorf("runbook: %s", built.Annotations["runbook_url"])
	}
	if raw.Expr != `sum(rate(calls_total[5m])) == 0` || raw.Annotations[AnnotationThreshold] != "" {
		t.Errorf("raw: %s %v", raw.Expr, raw.Annotations)
	}
	if _, err := RenderAndValidate(rulefmt.RuleGroups{Groups: []rulefmt.RuleGroup{{Name: "g", Rules: []rulefmt.Rule{built, raw}}}}); err != nil {
		t.Errorf("Prometheus rejects custom rules: %v", err)
	}
}
