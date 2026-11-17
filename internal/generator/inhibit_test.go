// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

type amMatcher struct {
	Name, Value, MatchType string
}

func (m amMatcher) matches(labels map[string]string) bool {
	v := labels[m.Name]
	switch m.MatchType {
	case "=":
		return v == m.Value
	case "!=":
		return v != m.Value
	case "=~":
		return regexp.MustCompile("^(?:" + m.Value + ")$").MatchString(v)
	case "!~":
		return !regexp.MustCompile("^(?:" + m.Value + ")$").MatchString(v)
	}
	panic("unknown match type " + m.MatchType)
}

type amInhibitRule struct {
	SourceMatch []amMatcher `json:"sourceMatch"`
	TargetMatch []amMatcher `json:"targetMatch"`
	Equal       []string    `json:"equal"`
}

func all(ms []amMatcher, labels map[string]string) bool {
	for _, m := range ms {
		if !m.matches(labels) {
			return false
		}
	}
	return true
}

// inhibited evaluates rules like Alertmanager's inhibitor: an alert matching both
// sides is not muted by another alert matching both sides.
func inhibited(rules []amInhibitRule, alerts []map[string]string) map[int]bool {
	out := map[int]bool{}
	for ti, target := range alerts {
		for _, r := range rules {
			if !all(r.TargetMatch, target) {
				continue
			}
			for si, source := range alerts {
				if si == ti || !all(r.SourceMatch, source) {
					continue
				}
				if all(r.TargetMatch, source) && all(r.SourceMatch, target) {
					continue
				}
				eq := true
				for _, l := range r.Equal {
					if source[l] != target[l] {
						eq = false
					}
				}
				if eq {
					out[ti] = true
				}
			}
		}
	}
	return out
}

func inhibitRulesOf(t *testing.T, sd *v1alpha1.SystemDescription) []amInhibitRule {
	t.Helper()
	b, err := Generate(sd, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.AlertmanagerConfig == nil {
		t.Fatal("no AlertmanagerConfig")
	}
	raw, _ := json.Marshal(b.AlertmanagerConfig["inhibitRules"])
	var out []amInhibitRule
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func alert(sys, svc, name, sev string) map[string]string {
	return map[string]string{
		"alertname": name, rules.LabelService: svc, rules.LabelSeverity: sev,
		rules.LabelManaged: "true", LabelSystem: sys,
	}
}

func shop(groupByCause *bool) *v1alpha1.SystemDescription {
	return &v1alpha1.SystemDescription{
		ObjectMeta: metav1.ObjectMeta{Name: "shop"},
		Spec: v1alpha1.SystemDescriptionSpec{
			Services: []v1alpha1.Service{
				{Name: "frontend-proxy", Type: v1alpha1.ServiceTypeHTTP, Critical: true, DependsOn: []string{"frontend"}},
				{Name: "frontend", Type: v1alpha1.ServiceTypeHTTP, Critical: true, DependsOn: []string{"checkout", "ad"}},
				{Name: "checkout", Type: v1alpha1.ServiceTypeGRPC, Critical: true, DependsOn: []string{"payment", "cart"}},
				{Name: "payment", Type: v1alpha1.ServiceTypeGRPC, Critical: true},
				{Name: "cart", Type: v1alpha1.ServiceTypeGRPC},
				{Name: "ad", Type: v1alpha1.ServiceTypeGRPC},
				{Name: "loop-a", Type: v1alpha1.ServiceTypeHTTP, DependsOn: []string{"loop-b"}},
				{Name: "loop-b", Type: v1alpha1.ServiceTypeHTTP, DependsOn: []string{"loop-a"}},
			},
			Notifications: &v1alpha1.Notifications{
				Receivers:    []v1alpha1.Receiver{{Name: "oncall", Telegram: &v1alpha1.TelegramReceiver{ChatID: -100, BotTokenSecret: v1alpha1.SecretKeyRef{Name: "kamerton-telegram", Key: "bot-token"}}}},
				Routing:      &v1alpha1.Routing{Critical: []string{"oncall"}},
				GroupByCause: groupByCause,
			},
		},
	}
}

func TestInhibitRulesMuteCallersAlongTheChain(t *testing.T) {
	rs := inhibitRulesOf(t, shop(nil))
	alerts := []map[string]string{
		alert("shop", "payment", rules.AlertRequestsStopped, "critical"),          // 0 root
		alert("shop", "checkout", rules.AlertErrorRate, "critical"),               // 1 caller: muted
		alert("shop", "checkout", rules.AlertOperationErrorRate, "critical"),      // 2 caller: muted
		alert("shop", "frontend", rules.AlertLatency, "critical"),                 // 3 caller of a muted alert: muted
		alert("shop", "frontend-proxy", rules.AlertLatencyAboveNormal, "warning"), // 4 chain, warning: muted by critical
		alert("shop", "checkout", rules.AlertMemoryNearLimit, "critical"),         // 5 resource alert: kept
		alert("shop", "checkout", rules.AlertOOMKilled, "critical"),               // 6 resource alert: kept
		alert("shop", "checkout", rules.AlertSLOBurnFast, "critical"),             // 7 SLO burn of a caller: muted
		alert("shop", "checkout", rules.AlertQueueConsumerStopped, "critical"),    // 8 queue alert: kept
		alert("shop", "ad", rules.AlertErrorRate, "warning"),                      // 9 unrelated: kept
		alert("other", "checkout", rules.AlertErrorRate, "critical"),              // 10 another system: kept
		alert("shop", "checkout", rules.AlertServiceTelemetryMissing, "critical"), // 11 no data: kept
		alert("shop", "checkout", rules.AlertNoReadyPods, "critical"),             // 12 own cause of a caller: kept
		alert("shop", "frontend", rules.AlertSLOBurnSlow, "warning"),              // 13 SLO burn up the chain: muted
	}
	got := inhibited(rs, alerts)
	want := map[int]bool{1: true, 2: true, 3: true, 4: true, 7: true, 13: true}
	for i := range alerts {
		if got[i] != want[i] {
			t.Errorf("alert %d (%s %s %s): muted=%v, want %v", i, alerts[i][LabelSystem], alerts[i][rules.LabelService], alerts[i]["alertname"], got[i], want[i])
		}
	}
}

func TestInhibitRulesSeverity(t *testing.T) {
	rs := inhibitRulesOf(t, shop(nil))
	// A warning on the dependency must not hide a critical alert of a caller
	// (warnings may go nowhere), but mutes warnings.
	alerts := []map[string]string{
		alert("shop", "cart", rules.AlertErrorRate, "warning"),
		alert("shop", "checkout", rules.AlertErrorRate, "critical"),
		alert("shop", "checkout", rules.AlertLatency, "warning"),
	}
	got := inhibited(rs, alerts)
	if got[0] || got[1] || !got[2] {
		t.Errorf("muted = %v, want only the warning of checkout", got)
	}
}

func TestInhibitRulesNeverSelfInhibit(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*v1alpha1.SystemDescription{sd, shop(nil)} {
		for i, r := range inhibitRulesOf(t, s) {
			if isNoDataRule(r) {
				continue
			}
			var src string
			var dst *regexp.Regexp
			for _, m := range r.SourceMatch {
				if m.Name == rules.LabelService {
					src = m.Value
				}
			}
			for _, m := range r.TargetMatch {
				if m.Name == rules.LabelService {
					dst = regexp.MustCompile("^(?:" + m.Value + ")$")
				}
			}
			if src == "" || dst == nil {
				t.Fatalf("%s rule %d has no service matchers: %+v", s.Name, i, r)
			}
			if dst.MatchString(src) {
				t.Errorf("%s rule %d: source service %s also matches the target", s.Name, i, src)
			}
			for _, side := range [][]amMatcher{r.SourceMatch, r.TargetMatch} {
				names := map[string]bool{}
				for _, m := range side {
					names[m.Name] = true
				}
				if !names[rules.LabelManaged] || !names[LabelSystem] || !names["alertname"] {
					t.Errorf("%s rule %d: a side lacks the managed/system/alertname matchers: %+v", s.Name, i, side)
				}
			}
		}
	}
	// A whole system firing at once: every alert matches some source, and
	// the root still gets through.
	alerts := []map[string]string{
		alert("shop", "payment", rules.AlertErrorRate, "critical"),
		alert("shop", "checkout", rules.AlertErrorRate, "critical"),
		alert("shop", "frontend", rules.AlertErrorRate, "critical"),
	}
	if got := inhibited(inhibitRulesOf(t, shop(nil)), alerts); got[0] {
		t.Error("the root alert is muted")
	}
}

func TestInhibitRulesCycleDoesNotMute(t *testing.T) {
	rs := inhibitRulesOf(t, shop(nil))
	alerts := []map[string]string{
		alert("shop", "loop-a", rules.AlertErrorRate, "warning"),
		alert("shop", "loop-b", rules.AlertErrorRate, "warning"),
	}
	if got := inhibited(rs, alerts); len(got) != 0 {
		t.Errorf("services in a cycle mute each other: %v", got)
	}
}

func TestInhibitRulesSwitchOff(t *testing.T) {
	off := false
	b, err := Generate(shop(&off), Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b.AlertmanagerConfig["inhibitRules"])
	var left []amInhibitRule
	_ = json.Unmarshal(raw, &left)
	if len(left) != 1 || !isNoDataRule(left[0]) {
		t.Errorf("groupByCause: false must leave only the system no-data rule: %+v", left)
	}
	if rs := inhibitRulesOf(t, shop(nil)); len(rs) == 0 {
		t.Error("no inhibit rules by default")
	}
}

func TestInhibitRulesOtelDemo(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(sd, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.RuleCount != 195 {
		t.Errorf("rule count = %d, want 195", b.RuleCount)
	}
	var callers []string
	for _, r := range inhibitRulesOf(t, sd) {
		for _, m := range r.SourceMatch {
			if m.Name == rules.LabelService && m.Value == "payment" {
				for _, tm := range r.TargetMatch {
					if tm.Name == rules.LabelService {
						callers = append(callers, tm.Value)
					}
				}
			}
		}
	}
	sort.Strings(callers)
	if strings.Join(callers, ",") != "checkout,checkout" {
		t.Errorf("payment mutes %v, want checkout (two severity rules)", callers)
	}
	// 1 system no-data rule, 2 per service with callers (13), 2 per fed service (15).
	if n := len(inhibitRulesOf(t, sd)); n != 57 {
		t.Errorf("%d inhibit rules, want 57", n)
	}
}

func isNoDataRule(r amInhibitRule) bool {
	for _, m := range r.SourceMatch {
		if m.Name == "alertname" && m.Value == rules.AlertTelemetryMissing {
			return true
		}
	}
	return false
}

func systemAlert(sys, name string) map[string]string {
	a := alert(sys, "", name, "critical")
	delete(a, rules.LabelService)
	return a
}

func TestInhibitRulesNoData(t *testing.T) {
	rs := inhibitRulesOf(t, shop(nil))
	alerts := []map[string]string{
		systemAlert("shop", rules.AlertTelemetryMissing),                         // 0 system: kept
		alert("shop", "payment", rules.AlertServiceTelemetryMissing, "critical"), // 1 muted by the system alert
		alert("shop", "cart", rules.AlertServiceTelemetryMissing, "warning"),     // 2 muted by the system alert
		alert("shop", "checkout", rules.AlertErrorRate, "critical"),              // 3 not a no-data alert: kept
		alert("other", "cart", rules.AlertServiceTelemetryMissing, "warning"),    // 4 another system: kept
	}
	got := inhibited(rs, alerts)
	want := map[int]bool{1: true, 2: true}
	for i := range alerts {
		if got[i] != want[i] {
			t.Errorf("alert %d (%v): muted=%v, want %v", i, alerts[i], got[i], want[i])
		}
	}
	// Without the system alert, a per-service no-data alert is never muted, and it never mutes callers.
	alerts = []map[string]string{
		alert("shop", "payment", rules.AlertServiceTelemetryMissing, "critical"),
		alert("shop", "checkout", rules.AlertErrorRate, "critical"),
		alert("shop", "checkout", rules.AlertSLOBurnFast, "critical"),
		alert("shop", "payment", rules.AlertNoReadyPods, "critical"),
	}
	if got := inhibited(rs, alerts); got[0] || got[3] || !got[1] || !got[2] {
		t.Errorf("muted = %v, want the caller's error and SLO alerts only (by no ready pods)", got)
	}
	alerts = alerts[:3]
	if got := inhibited(rs, alerts); len(got) != 0 {
		t.Errorf("a no-data alert muted %v", got)
	}
	// The SLO burn alert of a caller is only a target: it mutes nothing.
	alerts = []map[string]string{
		alert("shop", "checkout", rules.AlertSLOBurnFast, "critical"),
		alert("shop", "frontend", rules.AlertErrorRate, "critical"),
	}
	if got := inhibited(rs, alerts); len(got) != 0 {
		t.Errorf("an SLO alert muted %v", got)
	}

	off := false
	sd := shop(nil)
	sd.Spec.NoData = &v1alpha1.SystemNoDataPolicy{Enabled: &off}
	for _, r := range inhibitRulesOf(t, sd) {
		if isNoDataRule(r) {
			t.Error("system no-data alert off, its inhibit rule stays")
		}
	}
}

func TestWebhookRoute(t *testing.T) {
	wh := &WebhookOptions{URL: "http://kamerton.kamerton.svc:8080/api/v1/hooks/alertmanager", Secret: v1alpha1.SecretKeyRef{Name: "kamerton-webhook", Key: "token"}, Namespace: "kamerton"}
	sd := shop(nil)
	sd.Namespace = "kamerton"
	b, err := Generate(sd, Options{Webhook: wh})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b.AlertmanagerConfig)
	var amc struct {
		Route struct {
			Receiver string `json:"receiver"`
			Routes   []struct {
				Receiver string           `json:"receiver"`
				Continue bool             `json:"continue"`
				Matchers []map[string]any `json:"matchers"`
			} `json:"routes"`
		} `json:"route"`
		Receivers []map[string]any `json:"receivers"`
	}
	if err := json.Unmarshal(raw, &amc); err != nil {
		t.Fatal(err)
	}
	routes := amc.Route.Routes
	if len(routes) != 2 || routes[0].Receiver != "oncall" || !routes[0].Continue {
		t.Fatalf("existing delivery changed: %+v", routes)
	}
	last := routes[len(routes)-1]
	if last.Receiver != webhookReceiver || !last.Continue || len(last.Matchers) != 0 {
		t.Errorf("webhook route = %+v", last)
	}
	j := string(raw)
	for _, want := range []string{
		`"url":"http://kamerton.kamerton.svc:8080/api/v1/hooks/alertmanager"`,
		`"authorization":{"credentials":{"key":"token","name":"kamerton-webhook"},"type":"Bearer"}`,
		`"sendResolved":true`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("AlertmanagerConfig lacks %s", want)
		}
	}

	other := shop(nil)
	other.Namespace = "team-a"
	off := false
	quiet := shop(nil)
	quiet.Namespace = "kamerton"
	quiet.Spec.Notifications.EvidenceFollowUp = &off
	for _, s := range []*v1alpha1.SystemDescription{other, quiet} {
		b, err := Generate(s, Options{Webhook: wh})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(b.AlertmanagerConfig)
		if strings.Contains(string(raw), webhookReceiver) {
			t.Errorf("%s: webhook wired: %s", s.Namespace, raw)
		}
	}
}

func starvedAlert(sys, svc, name, sev string) map[string]string {
	a := alert(sys, svc, name, sev)
	a[rules.LabelStarved] = "true"
	return a
}

func demo(t *testing.T) *v1alpha1.SystemDescription {
	t.Helper()
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return sd
}

// Payment scaled to zero in the demo: checkout fails, email gets no calls and
// accounting and fraud-detection no orders.
func TestInhibitRulesMuteStarvedServices(t *testing.T) {
	rs := inhibitRulesOf(t, demo(t))
	alerts := []map[string]string{
		alert("otel-demo", "payment", rules.AlertNoReadyPods, "critical"),                            // 0 root
		alert("otel-demo", "payment", rules.AlertRequestsStopped, "critical"),                        // 1 its own (no label): kept
		alert("otel-demo", "checkout", rules.AlertErrorRate, "critical"),                             // 2 caller: muted by payment
		starvedAlert("otel-demo", "email", rules.AlertServiceTelemetryMissing, "warning"),            // 3 starved callee: muted
		starvedAlert("otel-demo", "accounting", rules.AlertServiceTelemetryMissing, "warning"),       // 4 starved consumer: muted
		starvedAlert("otel-demo", "fraud-detection", rules.AlertServiceTelemetryMissing, "warning"),  // 5 starved consumer: muted
		alert("otel-demo", "email", rules.AlertErrorRate, "warning"),                                 // 6 own errors of a starved service: kept
		alert("otel-demo", "email", rules.AlertRestarts, "warning"),                                  // 7 own restarts: kept
		alert("otel-demo", "quote", rules.AlertServiceTelemetryMissing, "warning"),                   // 8 no label: kept
		starvedAlert("otel-demo", "quote", rules.AlertRequestsStopped, "critical"),                   // 9 feeder shipping quiet: kept
		starvedAlert("otel-demo", "valkey-cart", rules.AlertServiceTelemetryMissing, "critical"),     // 10 feeder cart quiet: kept
		starvedAlert("otel-demo", "product-catalog", rules.AlertServiceTelemetryMissing, "critical"), // 11 feeder checkout fires: muted
		starvedAlert("other", "email", rules.AlertServiceTelemetryMissing, "warning"),                // 12 another system: kept
	}
	got := inhibited(rs, alerts)
	want := map[int]bool{2: true, 3: true, 4: true, 5: true, 11: true}
	for i := range alerts {
		if got[i] != want[i] {
			t.Errorf("alert %d (%s %s): muted=%v, want %v", i, alerts[i][rules.LabelService], alerts[i]["alertname"], got[i], want[i])
		}
	}
}

func TestInhibitRulesStarvationChainAndLoop(t *testing.T) {
	rs := inhibitRulesOf(t, demo(t))
	// A chain: frontend-proxy fails, frontend is starved (muted), and its
	// muted alert still mutes the services frontend starves.
	chain := []map[string]string{
		alert("otel-demo", "frontend-proxy", rules.AlertNoReadyPods, "critical"),
		starvedAlert("otel-demo", "frontend", rules.AlertRequestsStopped, "critical"),
		starvedAlert("otel-demo", "ad", rules.AlertRequestsStopped, "critical"),
	}
	if got := inhibited(rs, chain); got[0] || !got[1] || !got[2] {
		t.Errorf("chain: muted = %v, want frontend and ad", got)
	}
	// A starved "requests stopped" mutes no caller, so the two never mute each other.
	loop := []map[string]string{
		starvedAlert("otel-demo", "payment", rules.AlertRequestsStopped, "critical"),
		alert("otel-demo", "checkout", rules.AlertErrorRate, "critical"),
	}
	if got := inhibited(rs, loop); got[1] || !got[0] {
		t.Errorf("loop: muted = %v, want only payment's starved alert", got)
	}
	// A warning of the feeder does not hide a critical starved alert.
	sev := []map[string]string{
		alert("otel-demo", "checkout", rules.AlertLatency, "warning"),
		starvedAlert("otel-demo", "email", rules.AlertServiceTelemetryMissing, "critical"),
		starvedAlert("otel-demo", "accounting", rules.AlertServiceTelemetryMissing, "warning"),
	}
	if got := inhibited(rs, sev); got[1] || !got[2] {
		t.Errorf("severity: muted = %v, want the warning only", got)
	}
}

func TestStarvationOffWithoutGroupingByCause(t *testing.T) {
	sd := demo(t)
	off := false
	sd.Spec.Notifications.GroupByCause = &off
	b, err := Generate(sd, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			if strings.Contains(r.Expr, "ALERTS") {
				t.Errorf("%s %s reads ALERTS without grouping by cause", r.Alert, r.Labels[rules.LabelService])
			}
		}
	}
	on := byService(loadExample(t), rules.AlertServiceTelemetryMissing)
	if !strings.Contains(on["email"].Expr, `group_left (kamerton_starved)`) || !strings.Contains(on["accounting"].Expr, `ALERTS{service="checkout", alertname=~`) {
		t.Errorf("email and accounting are starved by checkout:\n%s\n%s", on["email"].Expr, on["accounting"].Expr)
	}
	if !strings.Contains(trafficRules(loadExample(t))["payment"].Expr, `group_left (kamerton_starved)`) {
		t.Error("payment's traffic rule can be starved by checkout")
	}
}
