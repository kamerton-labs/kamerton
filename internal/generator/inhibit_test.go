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

// amMatcher and amInhibitRule mirror the generated spec, so the test can
// evaluate the rules the way Alertmanager does.
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

// inhibited evaluates inhibit rules like Alertmanager's inhibitor: an alert
// is muted when some rule's target matches it and another firing alert
// (muted or not) matches that rule's source with equal labels. Like
// Alertmanager, an alert matching both sides is not muted by an alert that
// also matches both sides, so no alert inhibits itself through a rule.
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
		alert("shop", "checkout", rules.AlertSLOBurnFast, "critical"),             // 7 SLO alert: kept
		alert("shop", "checkout", rules.AlertQueueConsumerStopped, "critical"),    // 8 queue alert: kept
		alert("shop", "ad", rules.AlertErrorRate, "warning"),                      // 9 unrelated: kept
		alert("other", "checkout", rules.AlertErrorRate, "critical"),              // 10 another system: kept
	}
	got := inhibited(rs, alerts)
	want := map[int]bool{1: true, 2: true, 3: true, 4: true}
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
	if _, ok := b.AlertmanagerConfig["inhibitRules"]; ok {
		t.Error("groupByCause: false still generates inhibit rules")
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
	if b.RuleCount != 164 {
		t.Errorf("rule count = %d, want 164", b.RuleCount)
	}
	// payment is called by checkout only.
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

	// Another namespace (the Secret is not there), or the follow-up off:
	// no webhook, delivery unchanged.
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
