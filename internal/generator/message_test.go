// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	htmltemplate "html/template"
	"regexp"
	"strings"
	"testing"
	texttemplate "text/template"
	"time"

	"github.com/kamerton-labs/kamerton/internal/rules"
)

// The types below mirror alertmanager/template.Data, so the generated templates
// run exactly as Alertmanager runs them.
type kv map[string]string

type amAlert struct {
	Status       string
	Labels       kv
	Annotations  kv
	StartsAt     time.Time
	EndsAt       time.Time
	GeneratorURL string
	Fingerprint  string
}

type amAlerts []amAlert

func (as amAlerts) Firing() []amAlert   { return as.filter("firing") }
func (as amAlerts) Resolved() []amAlert { return as.filter("resolved") }
func (as amAlerts) filter(s string) []amAlert {
	var out []amAlert
	for _, a := range as {
		if a.Status == s {
			out = append(out, a)
		}
	}
	return out
}

type amData struct {
	Receiver          string
	Status            string
	Alerts            amAlerts
	GroupLabels       kv
	CommonLabels      kv
	CommonAnnotations kv
	ExternalURL       string
}

func sampleData(n int, status string) amData {
	start := time.Date(2026, 11, 15, 14, 3, 0, 0, time.UTC)
	d := amData{Status: status, CommonLabels: kv{"kamerton_system": "shop", "environment": "prod", "severity": "critical", "service": "checkout"}}
	for i := 0; i < n; i++ {
		d.Alerts = append(d.Alerts, amAlert{
			Status: status,
			Labels: kv{"alertname": "KamertonErrorRateHigh", "service": "checkout", "severity": "critical"},
			Annotations: kv{
				rules.AnnotationTitle: "Много ошибочных запросов", rules.AnnotationValue: "12.5%", rules.AnnotationThreshold: "5%",
				"summary": "High error rate: checkout", "runbook_url": "https://kamerton.example.com/runbooks/checkout/KamertonErrorRateHigh",
			},
			StartsAt: start, EndsAt: start.Add(10 * time.Minute),
			GeneratorURL: "http://kamerton-kps-prometheus.kamerton:9090/graph?g0.expr=up",
		})
	}
	return d
}

var amFuncs = map[string]any{
	"reReplaceAll": func(pattern, repl, text string) string {
		return regexp.MustCompile(pattern).ReplaceAllString(text, repl)
	},
}

func execHTML(t *testing.T, tpl string, d amData) string {
	t.Helper()
	tm, err := htmltemplate.New("m").Funcs(amFuncs).Parse(tpl)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, tpl)
	}
	var b strings.Builder
	if err := tm.Execute(&b, d); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return b.String()
}

func execText(t *testing.T, tpl string, d amData) string {
	t.Helper()
	tm, err := texttemplate.New("m").Funcs(amFuncs).Parse(tpl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var b strings.Builder
	if err := tm.Execute(&b, d); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return b.String()
}

var telegramTag = regexp.MustCompile(`</?([a-zA-Z-]+)[^>]*>`)

func checkTelegramHTML(t *testing.T, out string) {
	t.Helper()
	allowed := map[string]bool{"b": true, "i": true, "a": true, "code": true, "pre": true, "u": true, "s": true, "blockquote": true}
	for _, m := range telegramTag.FindAllStringSubmatch(out, -1) {
		if !allowed[m[1]] {
			t.Errorf("tag %q is not allowed by Telegram HTML parse mode:\n%s", m[0], out)
		}
	}
	if len([]rune(out)) > 4096 {
		t.Errorf("message has %d characters, Telegram's limit is 4096", len([]rune(out)))
	}
}

func TestTelegramMessageFiringRussianWithLinks(t *testing.T) {
	m := buildMessages("ru", "https://kamerton.example.com/", utc)
	out := execHTML(t, m.TelegramHTML, sampleData(1, "firing"))
	checkTelegramHTML(t, out)
	for _, want := range []string{
		"🔴 <b>Критично</b> · shop (prod)",
		"<b>checkout</b>: Много ошибочных запросов",
		"сейчас <b>12.5%</b>, порог 5%",
		"с 14:03 15.11 UTC",
		`<a href="https://kamerton.example.com/runbooks/checkout/KamertonErrorRateHigh">Что делать</a>`,
		`<a href="https://kamerton.example.com/alerts?alertname=KamertonErrorRateHigh&amp;service=checkout">Открыть в Kamerton</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("message lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "9090") || strings.Contains(out, "kamerton-kps") {
		t.Errorf("message links to an in-cluster address:\n%s", out)
	}
}

func TestTelegramMessageWithoutExternalURLHasNoLinks(t *testing.T) {
	m := buildMessages("ru", "", utc)
	out := execHTML(t, m.TelegramHTML, sampleData(2, "firing"))
	checkTelegramHTML(t, out)
	if strings.Contains(out, "<a ") || strings.Contains(out, "http") {
		t.Errorf("no links expected without an external URL:\n%s", out)
	}
	if strings.Contains(m.EmailText, "http") || strings.Contains(m.EmailHTML, "href") {
		t.Error("email templates must not contain links without an external URL")
	}
}

func TestTelegramMessageGroupedIsCompactAndBounded(t *testing.T) {
	m := buildMessages("ru", "https://k.example.com", utc)
	out := execHTML(t, m.TelegramHTML, sampleData(30, "firing"))
	checkTelegramHTML(t, out)
	if got := strings.Count(out, "Много ошибочных запросов"); got != maxListedAlerts {
		t.Errorf("listed %d alerts, want %d", got, maxListedAlerts)
	}
	if !strings.Contains(out, "показаны первые 8 из 30") {
		t.Errorf("missing overflow note:\n%s", out)
	}
}

func TestTelegramMessageResolved(t *testing.T) {
	m := buildMessages("ru", "", utc)
	out := execHTML(t, m.TelegramHTML, sampleData(1, "resolved"))
	if !strings.Contains(out, "✅ <b>Решено</b>") || !strings.Contains(out, "до 14:13 15.11 UTC") {
		t.Errorf("resolved message:\n%s", out)
	}
}

func TestTelegramMessageEscapesValues(t *testing.T) {
	m := buildMessages("en", "https://k.example.com", utc)
	d := sampleData(1, "firing")
	d.Alerts[0].Labels["service"] = `<script>x</script>&`
	d.Alerts[0].Annotations[rules.AnnotationTitle] = `a <b>b</b>`
	d.Alerts[0].Annotations["runbook_url"] = `javascript:alert(1)`
	out := execHTML(t, m.TelegramHTML, d)
	checkTelegramHTML(t, out)
	if strings.Contains(out, "<script>") || strings.Contains(out, "a <b>b</b>") || strings.Contains(out, "javascript:") {
		t.Errorf("values must be escaped:\n%s", out)
	}
	if !strings.Contains(out, "Critical") {
		t.Errorf("English words expected:\n%s", out)
	}
}

func TestEmailTemplates(t *testing.T) {
	m := buildMessages("ru", "https://k.example.com", utc)
	d := sampleData(2, "firing")
	if out := execHTML(t, m.EmailHTML, d); !strings.Contains(out, "Много ошибочных запросов") {
		t.Errorf("email html:\n%s", out)
	}
	text := execText(t, m.EmailText, d)
	if strings.Contains(text, "<b>") || !strings.Contains(text, "Что делать: https://kamerton.example.com/runbooks/checkout/KamertonErrorRateHigh") ||
		!strings.Contains(text, "Открыть в Kamerton: https://k.example.com/alerts?alertname=KamertonErrorRateHigh&service=checkout") {
		t.Errorf("email text:\n%s", text)
	}
	d.CommonAnnotations = kv{rules.AnnotationTitle: "Много ошибочных запросов"}
	if subj := execText(t, m.EmailSubject, d); subj != "Проблема: shop / checkout - Много ошибочных запросов (2)" {
		t.Errorf("subject: %q", subj)
	}
}

func TestSafeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                         "",
		"http://localhost:8080/":   "http://localhost:8080",
		"ftp://x":                  "",
		"https://k.example.com/{{": "",
		`https://k.example.com/"a`: "https://k.example.com/&#34;a",
	} {
		if got := safeBaseURL(in); got != want {
			t.Errorf("safeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

var testNow = time.Date(2026, 11, 16, 4, 0, 0, 0, time.UTC)

var utc = newZoneClock("", testNow)
