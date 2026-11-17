// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

func loadExample(t *testing.T) *Bundle {
	t.Helper()
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(sd, Options{RunbookBaseURL: "http://kamerton.local"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGenerateExample(t *testing.T) {
	b := loadExample(t)
	if b.RuleCount == 0 {
		t.Fatal("no rules generated")
	}
	if err := rules.Validate(b.RulesYAML); err != nil {
		t.Fatal(err)
	}
	y := string(b.RulesYAML)
	for _, want := range []string{
		"KamertonErrorRateHigh",
		"KamertonOperationErrorRateHigh",
		"KamertonLatencyHigh",
		"KamertonPodRestarting",
		"KamertonMemoryHigh",
		"KamertonQueueLagHigh",
		"KamertonQueueConsumerStopped",
		"KamertonSLOBurnRateFast",
		"KamertonSLOBurnRateSlow",
		`oteldemo.CheckoutService/PlaceOrder`,
		`flagd\\.evaluation\\.v2\\.Service/EventStream`,
		`runbook_url: http://kamerton.local/runbooks/checkout`,
	} {
		if !strings.Contains(y, want) {
			t.Errorf("rule file does not contain %q", want)
		}
	}
}

func TestPerOperationRuleExcludesExplicitOperations(t *testing.T) {
	b := loadExample(t)
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			if r.Alert == rules.AlertOperationErrorRate && r.Labels[rules.LabelService] == "checkout" && r.Labels[rules.LabelOperation] == "" {
				if !strings.Contains(r.Expr, `oteldemo\\.CheckoutService/PlaceOrder`) {
					t.Errorf("aggregated per-operation rule must exclude PlaceOrder (it has its own threshold):\n%s", r.Expr)
				}
				return
			}
		}
	}
	t.Fatal("no aggregated per-operation rule for checkout")
}

func TestDatabaseServiceHasNoRequestRules(t *testing.T) {
	b := loadExample(t)
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			sig := r.Labels[rules.LabelSignal]
			if r.Labels[rules.LabelService] == "valkey-cart" && sig != string(rules.SignalSaturation) && sig != string(rules.SignalAvailability) {
				t.Errorf("database service got a %s rule", r.Labels[rules.LabelSignal])
			}
		}
	}
}

func TestDefaultPodRegexDoesNotOverlap(t *testing.T) {
	// "frontend" must not match pods of "frontend-proxy".
	b := loadExample(t)
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			if r.Labels[rules.LabelService] == "frontend" && r.Alert == rules.AlertRestarts {
				if !strings.Contains(r.Expr, `pod=~"frontend-([a-z0-9]+-[a-z0-9]{5}|[0-9]+)"`) {
					t.Errorf("unexpected pod matcher: %s", r.Expr)
				}
				return
			}
		}
	}
	t.Fatal("no restart rule for frontend")
}

func TestAlertmanagerConfig(t *testing.T) {
	b := loadExample(t)
	if b.AlertmanagerConfig == nil {
		t.Fatal("no Alertmanager config")
	}
	raw, _ := json.Marshal(b.AlertmanagerConfig)
	s := string(raw)
	for _, want := range []string{`"telegramConfigs"`, `"botToken":{"key":"bot-token","name":"kamerton-telegram"}`, `"authPassword":{"key":"password","name":"kamerton-smtp"}`} {
		if !strings.Contains(s, want) {
			t.Errorf("Alertmanager config missing %s: %s", want, s)
		}
	}
	route := b.AlertmanagerConfig["route"].(map[string]any)
	if got := len(route["routes"].([]any)); got != 3 {
		t.Errorf("want 3 routes (2 critical + 1 warning), got %d", got)
	}
}

func TestDashboardIsJSON(t *testing.T) {
	b := loadExample(t)
	for name, d := range b.Dashboards {
		var v map[string]any
		if err := json.Unmarshal(d, &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(v["panels"].([]any)) == 0 {
			t.Errorf("%s has no panels", name)
		}
	}
}

func TestRunbooks(t *testing.T) {
	b := loadExample(t)
	rb, ok := b.Runbooks["checkout.md"]
	if !ok {
		t.Fatal("no runbook for checkout")
	}
	if !strings.Contains(rb, "Called by: frontend") || !strings.Contains(rb, "TODO") {
		t.Errorf("unexpected runbook:\n%s", rb)
	}
}

func TestAlertmanagerConfigUsesKamertonTemplates(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(sd, Options{})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(b.AlertmanagerConfig)
	s := string(j)
	if !strings.Contains(s, `"message":`) || !strings.Contains(s, `"html":`) || !strings.Contains(s, `"Subject"`) {
		t.Errorf("templates missing:\n%s", s)
	}
	if strings.Contains(s, "http://") || strings.Contains(s, "https://") || strings.Contains(s, "GeneratorURL") {
		t.Errorf("links without an external URL:\n%s", s)
	}
	if strings.Contains(string(b.RulesYAML), "runbook_url") {
		t.Error("runbook_url without an external URL")
	}
	if !strings.Contains(string(b.RulesYAML), "kamerton_title: Too many failed requests") {
		t.Error("English kamerton_title annotation missing")
	}
	b, _ = Generate(sd, Options{RunbookBaseURL: "http://localhost:8080/"})
	if !strings.Contains(string(b.RulesYAML), "runbook_url: http://localhost:8080/runbooks/checkout/KamertonErrorRateHigh") {
		t.Error("per-alert runbook URL missing")
	}
	j, _ = json.Marshal(b.AlertmanagerConfig)
	if !strings.Contains(string(j), "http://localhost:8080/alerts?alertname=") {
		t.Error("alert link missing")
	}
	sd.Spec.Notifications.Language = "ru"
	b, _ = Generate(sd, Options{})
	if !strings.Contains(string(b.RulesYAML), "kamerton_title: Много ошибочных запросов") {
		t.Error("Russian kamerton_title missing")
	}
}
