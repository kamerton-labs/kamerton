// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/rulefmt"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

func rulesOf(t *testing.T, doc string) []rulefmt.Rule {
	t.Helper()
	res := description.Validate([]byte(doc))
	if !res.Valid {
		t.Fatalf("invalid: %+v", res.Errors)
	}
	b, err := Generate(res.Document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rules.Validate(b.RulesYAML); err != nil {
		t.Fatal(err)
	}
	var out []rulefmt.Rule
	for _, g := range b.RuleGroups.Groups {
		out = append(out, g.Rules...)
	}
	return out
}

func alertsOf(rs []rulefmt.Rule, service string) map[string]rulefmt.Rule {
	out := map[string]rulefmt.Rule{}
	for _, r := range rs {
		if r.Labels[rules.LabelService] == service && r.Labels[rules.LabelOperation] == "" {
			out[r.Alert] = r
		}
	}
	return out
}

const plainService = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  services:
    - {name: api, type: http}
    - {name: db, type: database}
`

func TestRulesOnByDefault(t *testing.T) {
	rs := rulesOf(t, plainService)
	api := alertsOf(rs, "api")
	for _, a := range []string{
		rules.AlertOperationErrorRate, rules.AlertOperationLatency, rules.AlertLatencyAboveNormal,
		rules.AlertMemoryNearLimit, rules.AlertCPUNearLimit, rules.AlertCPUAboveNormal, rules.AlertOOMKilled,
	} {
		if _, ok := api[a]; !ok {
			t.Errorf("api has no %s", a)
		}
	}
	if f := api[rules.AlertOperationLatency].For.String(); f != "30s" {
		t.Errorf("per-operation latency for %s, want 30s", f)
	}
	if f := api[rules.AlertLatencyAboveNormal].For.String(); f != "1m" {
		t.Errorf("slower-than-normal for %s, want 1m", f)
	}
	if f := api[rules.AlertOOMKilled].For.String(); f != "0s" {
		t.Errorf("OOMKilled for %s, want 0s", f)
	}
	// The service latency rule does not cut off rare traffic by rate.
	if e := api[rules.AlertLatency].Expr; strings.Contains(e, "> 0.05") || !strings.Contains(e, ">= 3") {
		t.Errorf("latency volume condition:\n%s", e)
	}
	db := alertsOf(rs, "db")
	if _, ok := db[rules.AlertOperationLatency]; ok {
		t.Error("database got request rules")
	}
	if _, ok := db[rules.AlertOOMKilled]; !ok {
		t.Error("database pod has no OOMKilled rule")
	}
}

func TestExplicitThresholdsAndSwitches(t *testing.T) {
	rs := rulesOf(t, `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  services:
    - name: api
      type: http
      errorRate: {perOperation: false}
      latency: {perOperation: false, aboveNormalFactor: 0}
      saturation: {memory: 512Mi, cpuCores: 2, oomKilledEnabled: false}
    - name: web
      type: http
      latency: {aboveNormalFactor: 10, for: 15s}
      saturation: {memoryLimitShare: 0.8, cpuLimitShare: 0, cpuAboveNormalFactor: 4}
      operations:
        - name: GET /slow
          latency: {thresholdMs: 5000}
`)
	api := alertsOf(rs, "api")
	for _, a := range []string{
		rules.AlertOperationErrorRate, rules.AlertOperationLatency, rules.AlertLatencyAboveNormal,
		rules.AlertMemoryNearLimit, rules.AlertCPUNearLimit, rules.AlertCPUAboveNormal, rules.AlertOOMKilled,
	} {
		if _, ok := api[a]; ok {
			t.Errorf("api: %s must be off", a)
		}
	}
	for _, a := range []string{rules.AlertMemory, rules.AlertCPU} {
		if _, ok := api[a]; !ok {
			t.Errorf("api: explicit %s missing", a)
		}
	}
	web := alertsOf(rs, "web")
	if _, ok := web[rules.AlertCPUNearLimit]; ok {
		t.Error("web: cpuLimitShare 0 must turn the rule off")
	}
	if e := web[rules.AlertMemoryNearLimit].Expr; !strings.Contains(e, "> 0.8\n") {
		t.Errorf("web memory share:\n%s", e)
	}
	if e := web[rules.AlertCPUAboveNormal].Expr; !strings.Contains(e, "4 * (") {
		t.Errorf("web CPU factor:\n%s", e)
	}
	an := web[rules.AlertLatencyAboveNormal]
	if !strings.Contains(an.Expr, "10 * (") || an.For.String() != "15s" {
		t.Errorf("web above normal: for %s\n%s", an.For, an.Expr)
	}
	if e := web[rules.AlertOperationLatency].Expr; !strings.Contains(e, `GET /slow`) || !strings.Contains(e, "span_name!~") {
		t.Errorf("per-operation latency does not exclude GET /slow:\n%s", e)
	}
	if f := web[rules.AlertOperationLatency].For.String(); f != "15s" {
		t.Errorf("a shorter latency for wins: %s", f)
	}
}

func TestDisabledMetricsTurnRulesOff(t *testing.T) {
	rs := rulesOf(t, `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  telemetry:
    containers: {limitsMetric: "-", terminatedReasonMetric: "-"}
  services:
    - {name: api, type: http}
`)
	api := alertsOf(rs, "api")
	for _, a := range []string{rules.AlertMemoryNearLimit, rules.AlertCPUNearLimit, rules.AlertOOMKilled} {
		if _, ok := api[a]; ok {
			t.Errorf("%s must be off without its metric", a)
		}
	}
	if _, ok := api[rules.AlertCPUAboveNormal]; !ok {
		t.Error("CPU above normal needs only the CPU metric")
	}
}

const queueSystem = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  notifications: {language: ru}
  telemetry:
    queues: {membersMetric: my_members, offsetMetric: my_offset}
  services:
    - name: orders
      type: queue
      critical: true
      queue: {topic: orders, consumerGroups: [billing], maxLag: 100, severity: warning, for: 5m,
        consumerStoppedMessage: {title: "Никто не читает {service}", text: "Сообщений: {value}; условие: {threshold}"}}
    - name: events
      type: queue
      queue: {topic: events, maxLag: 10, for: 30s, consumerStoppedEnabled: false}
    - name: audit
      type: queue
      queue: {topic: audit, maxLag: 10, for: 30s}
`

func TestQueueConsumerStoppedRule(t *testing.T) {
	rs := rulesOf(t, queueSystem)
	orders := alertsOf(rs, "orders")
	r, ok := orders[rules.AlertQueueConsumerStopped]
	if !ok {
		t.Fatalf("no stopped consumer rule next to the lag rule: %v", orders)
	}
	if _, ok := orders[rules.AlertQueueLag]; !ok {
		t.Fatal("lag rule missing")
	}
	if r.Labels[rules.LabelSeverity] != "warning" || r.For.String() != "1m" {
		t.Errorf("severity %s for %s", r.Labels[rules.LabelSeverity], r.For)
	}
	for _, want := range []string{"my_members{", "my_offset{", `topic="orders"`, `group=~"billing"`} {
		if !strings.Contains(r.Expr, want) {
			t.Errorf("expr does not contain %q:\n%s", want, r.Expr)
		}
	}
	if got := r.Annotations[AnnotationMessageTitle]; got != "Никто не читает orders" {
		t.Errorf("message title %q", got)
	}
	if got := r.Annotations[AnnotationMessageText]; !strings.Contains(got, `{{ $value | printf "%.0f" }} сообщений`) || !strings.Contains(got, "нет потребителей") {
		t.Errorf("message text %q", got)
	}
	if _, ok := alertsOf(rs, "events")[rules.AlertQueueConsumerStopped]; ok {
		t.Error("consumerStoppedEnabled: false still generates the rule")
	}
	if r := alertsOf(rs, "audit")[rules.AlertQueueConsumerStopped]; r.For.String() != "30s" || r.Labels[rules.LabelSeverity] != "warning" {
		t.Errorf("audit: for %s severity %s", r.For, r.Labels[rules.LabelSeverity])
	}

	// Both metrics off: no rule; the lag metric off: no queue rules at all.
	for _, tel := range []string{"{membersMetric: '-', offsetMetric: '-'}", "{lagMetric: '-'}"} {
		doc := strings.Replace(queueSystem, "{membersMetric: my_members, offsetMetric: my_offset}", tel, 1)
		if _, ok := alertsOf(rulesOf(t, doc), "orders")[rules.AlertQueueConsumerStopped]; ok {
			t.Errorf("telemetry %s still generates the rule", tel)
		}
	}
	doc := strings.Replace(queueSystem, "  telemetry:\n    queues: {membersMetric: my_members, offsetMetric: my_offset}\n", "", 1)
	r = alertsOf(rulesOf(t, doc), "orders")[rules.AlertQueueConsumerStopped]
	if !strings.Contains(r.Expr, "kafka_consumer_group_members{") || !strings.Contains(r.Expr, "kafka_consumer_group_offset_ratio{") {
		t.Errorf("default metrics:\n%s", r.Expr)
	}
	rb := runbook(&v1alpha1.SystemDescription{}, &v1alpha1.Service{Name: "orders"}, []rulefmt.Rule{r})
	if !strings.Contains(rb, "stopped reading the topic") || !strings.Contains(rb, "## Escalation") {
		t.Errorf("runbook has no hint:\n%s", rb)
	}
	ru := &v1alpha1.SystemDescription{Spec: v1alpha1.SystemDescriptionSpec{Notifications: &v1alpha1.Notifications{Language: "ru"}}}
	rb = runbook(ru, &v1alpha1.Service{Name: "orders"}, []rulefmt.Rule{r})
	if !strings.Contains(rb, "перестала читать топик") || !strings.Contains(rb, "## Эскалация") {
		t.Errorf("Russian runbook:\n%s", rb)
	}
}
