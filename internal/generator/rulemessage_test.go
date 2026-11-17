// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strings"
	"testing"

	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

func TestCheckMessageText(t *testing.T) {
	for text, want := range map[string]string{
		"": "",
		"Қате көп: {value}, шек {threshold}":  "", // Kazakh
		"错误率 {value} · {service}/{operation}": "",
		"Ошибок < 5 & всё {since} в {system}": "",
		"Порог {limit}":                       "unknown placeholder {limit}",
		"Текст {value":                        "braces are only for placeholders",
		"{{ $value }}":                        "unknown placeholder",
		"<b>жирно</b>":                        "markup",
		`<a href="https://x">ссылка</a>`:      "markup",
		"<script>alert(1)</script>":           "markup",
		"значение } лишняя скобка":            "braces are only for placeholders",
	} {
		got := description.CheckMessageText(text)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%q: got %q, want containing %q", text, got, want)
		}
	}
}

func TestMessageValidationPaths(t *testing.T) {
	doc := strings.Replace(extended, "      latency: {thresholdMs: 2000}", "      latency:\n        thresholdMs: 2000\n        message: {title: 'Медленно {oops}', action: '<b>x</b>'}", 1)
	res := description.Validate([]byte(doc))
	paths := map[string]bool{}
	for _, e := range res.Errors {
		paths[e.Path] = true
	}
	if res.Valid || !paths["/spec/services/0/latency/message/title"] || !paths["/spec/services/0/latency/message/action"] {
		t.Errorf("errors: %+v", res.Errors)
	}
}

const withMessages = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  services:
    - name: api
      type: http
      critical: true
      errorRate:
        perOperation: true
        message:
          title: 'Қателер көп: {service}'
          text: 'Сейчас {value} при пороге {threshold} в {system}, операция {operation}, с {since}'
          action: 'Проверьте выкладку & откатите, если < 5 минут'
      operations:
        - name: 'Pay/Charge "v2"'
          errorRate: {threshold: 0.01}
      saturation:
        memory: 256Mi
        memoryMessage: {title: 'Память кончается'}
`

func TestMessagesInRuleAnnotations(t *testing.T) {
	res := description.Validate([]byte(withMessages))
	if !res.Valid {
		t.Fatalf("invalid: %+v", res.Errors)
	}
	b, err := Generate(res.Document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]map[string]string{}
	for _, g := range b.RuleGroups.Groups {
		for _, r := range g.Rules {
			byKey[r.Alert+"/"+r.Labels[rules.LabelOperation]] = r.Annotations
		}
	}
	svc := byKey[rules.AlertErrorRate+"/"]
	if svc[AnnotationMessageTitle] != "Қателер көп: api" {
		t.Errorf("title: %q", svc[AnnotationMessageTitle])
	}
	if want := "Сейчас " + svc[rules.AnnotationValue] + " при пороге 5% в shop, операция , с {since}"; svc[AnnotationMessageText] != want {
		t.Errorf("text:\n got %q\nwant %q", svc[AnnotationMessageText], want)
	}
	if svc[AnnotationMessageAction] != "Проверьте выкладку & откатите, если < 5 минут" {
		t.Errorf("action: %q", svc[AnnotationMessageAction])
	}
	perOp := byKey[rules.AlertOperationErrorRate+"/"]
	if !strings.Contains(perOp[AnnotationMessageText], `операция {{ index $labels "span_name" }}`) {
		t.Errorf("per-operation text: %q", perOp[AnnotationMessageText])
	}
	exact := byKey[rules.AlertOperationErrorRate+`/Pay/Charge "v2"`]
	if !strings.Contains(exact[AnnotationMessageText], `операция {{ "Pay/Charge \"v2\"" }}`) || !strings.Contains(exact[AnnotationMessageText], "пороге 1%") {
		t.Errorf("exact operation text: %q", exact[AnnotationMessageText])
	}
	if byKey[rules.AlertMemory+"/"][AnnotationMessageTitle] != "Память кончается" {
		t.Errorf("memory title: %v", byKey[rules.AlertMemory+"/"])
	}
	if _, ok := byKey[rules.AlertLatency+"/"][AnnotationMessageTitle]; ok {
		t.Error("latency has no message and must keep the default text")
	}
	// The rule file with these annotations passes Prometheus' own checks
	// (Generate validates it; repeat explicitly for clarity).
	if err := rules.Validate(b.RulesYAML); err != nil {
		t.Fatal(err)
	}
}

func customAlert(status string) amAlert {
	a := sampleData(1, status).Alerts[0]
	a.Annotations[AnnotationMessageTitle] = "Қателер көп: checkout"
	a.Annotations[AnnotationMessageText] = `Сейчас 12.5% при пороге 5%, с {since} - "кавычки" & <теги>`
	a.Annotations[AnnotationMessageAction] = "Откатите последнюю выкладку"
	return a
}

func TestTeamTextSingleNotification(t *testing.T) {
	m := buildMessages("ru", "https://kamerton.example.com", utc)
	d := sampleData(1, "firing")
	d.Alerts[0] = customAlert("firing")
	out := execHTML(t, m.TelegramHTML, d)
	checkTelegramHTML(t, out)
	for _, want := range []string{
		"<b>checkout</b>: Қателер көп: checkout",
		"Сейчас 12.5% при пороге 5%, с 14:03 15.11 UTC - &#34;кавычки&#34; &amp; &lt;теги&gt;",
		"👉 Откатите последнюю выкладку",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Много ошибочных запросов") || strings.Contains(out, "{since}") {
		t.Errorf("default title or placeholder left:\n%s", out)
	}
	text := execText(t, m.EmailText, d)
	if !strings.Contains(text, `- "кавычки" & <теги>`) || !strings.Contains(text, "👉 Откатите") {
		t.Errorf("email text:\n%s", text)
	}
	d.CommonAnnotations = kv{AnnotationMessageTitle: "Сбой с {since}", rules.AnnotationTitle: "Много ошибочных запросов"}
	if subj := execText(t, m.EmailSubject, d); !strings.HasSuffix(subj, " - Сбой с") {
		t.Errorf("subject: %q", subj)
	}
}

func TestTeamTextGroupedNotificationStaysCompact(t *testing.T) {
	m := buildMessages("ru", "", utc)
	d := sampleData(3, "firing")
	for i := range d.Alerts {
		d.Alerts[i] = customAlert("firing")
	}
	out := execHTML(t, m.TelegramHTML, d)
	checkTelegramHTML(t, out)
	if strings.Count(out, "Қателер көп: checkout") != 3 || !strings.Contains(out, "сейчас <b>12.5%</b>") {
		t.Errorf("grouped list must keep title and value:\n%s", out)
	}
	if strings.Contains(out, "Откатите") || strings.Contains(out, "кавычки") {
		t.Errorf("grouped message must not repeat text and action:\n%s", out)
	}
	resolved := sampleData(2, "resolved")
	for i := range resolved.Alerts {
		resolved.Alerts[i] = customAlert("resolved")
	}
	if out := execHTML(t, m.TelegramHTML, resolved); !strings.Contains(out, "✅ <b>checkout</b>: Қателер көп: checkout") {
		t.Errorf("resolved list:\n%s", out)
	}
}
