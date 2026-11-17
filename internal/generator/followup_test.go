// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strings"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/internal/rules"
)

func sampleFollowUp() *FollowUp {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 16, h, m, 0, 0, time.UTC) }
	return &FollowUp{
		System:      "shop",
		Environment: "prod",
		Alerting:    []string{"checkout"},
		Affected:    []string{"frontend", "frontend-proxy"},
		Window:      30 * time.Minute,
		Roots: []RootEvidence{{
			Service: "payment",
			Title:   "Requests stopped arriving",
			Since:   at(4, 18),
			Changes: WorkloadChanges{
				Rollouts: []Rollout{{Workload: "payment", At: at(4, 14), OldImage: "v1.4.2", NewImage: "v1.4.3"}},
				Restarts: []Restart{{Pod: "payment-7d9-abcde", Container: "payment", Count: 3, Reason: "OOMKilled", ExitCode: 137, CrashLoop: true}},
				NotReady: 1, Pods: 2,
				Warnings: []Warning{{Reason: "BackOff", Message: "Back-off restarting failed container", Count: 4}},
			},
			Operations: []OperationErrors{{Operation: "oteldemo.PaymentService/Charge", Failed: 42}},
			LogLine:    "card declined: <upstream> timeout",
			TraceURL:   "https://grafana.example.com/explore?panes=x&orgId=1",
		}},
	}
}

func TestRenderFollowUpEnglish(t *testing.T) {
	m := RenderFollowUp(sampleFollowUp(), rules.LanguageEnglish, "Asia/Almaty")
	want := "🔎 <b>Probable cause</b> · shop (prod)\n" +
		"<b>checkout</b>: probable cause is <b>payment</b> (payment: Requests stopped arriving since 09:18 UTC+5).\n" +
		"Also affected: frontend, frontend-proxy.\n" +
		"What changed in <b>payment</b> in the last 30 min: new version at 09:14 UTC+5 (v1.4.2 -> v1.4.3); " +
		"container payment restarted 3 times (OOMKilled, exit code 137), keeps crashing (CrashLoopBackOff); " +
		"1 of 2 pods not ready; warning BackOff: Back-off restarting failed container.\n" +
		"Most errors: operation <b>oteldemo.PaymentService/Charge</b> (42 failed calls in 30 min).\n" +
		"Log: <i>\u201ccard declined: &lt;upstream&gt; timeout\u201d</i>\n" +
		`<a href="https://grafana.example.com/explore?panes=x&amp;orgId=1">Failing traces in Grafana</a>` + "\n"
	if m.TelegramHTML != want {
		t.Errorf("Telegram:\n%s\nwant:\n%s", m.TelegramHTML, want)
	}
	if m.EmailSubject != "Probable cause: shop / payment" {
		t.Errorf("subject = %q", m.EmailSubject)
	}
	if !strings.HasPrefix(m.EmailHTML, `<div style="font-family:sans-serif;white-space:pre-line">`) || !strings.Contains(m.EmailHTML, m.TelegramHTML) {
		t.Errorf("email HTML = %s", m.EmailHTML)
	}
	if strings.ContainsAny(m.EmailText, "<>") && !strings.Contains(m.EmailText, "<upstream>") {
		t.Errorf("plain text has markup: %s", m.EmailText)
	}
	if !strings.Contains(m.EmailText, "Failing traces in Grafana: https://grafana.example.com/explore?panes=x&orgId=1") ||
		!strings.Contains(m.EmailText, "Log: \u201ccard declined: <upstream> timeout\u201d") {
		t.Errorf("plain text:\n%s", m.EmailText)
	}
}

func TestRenderFollowUpRussian(t *testing.T) {
	f := sampleFollowUp()
	f.Alerting = nil
	m := RenderFollowUp(f, rules.LanguageRussian, "Europe/Moscow")
	for _, want := range []string{
		"🔎 <b>Вероятная причина</b> · shop (prod)\n",
		"Вероятная причина: <b>payment</b> (payment: Requests stopped arriving с 07:18 UTC+3).\n",
		"Также затронуты: frontend, frontend-proxy.\n",
		"Что изменилось в <b>payment</b> за последние 30 мин: новая версия в 07:14 UTC+3 (v1.4.2 -> v1.4.3); контейнер payment перезапускался, раз: 3 (OOMKilled, код выхода 137), падает снова и снова (CrashLoopBackOff); не готовы подов: 1 из 2; предупреждение BackOff: Back-off restarting failed container.\n",
		"Больше всего ошибок в операции <b>oteldemo.PaymentService/Charge</b>: 42 за 30 мин.\n",
		"Лог: <i>«card declined: &lt;upstream&gt; timeout»</i>\n",
		"Трассы с ошибками в Grafana</a>",
	} {
		if !strings.Contains(m.TelegramHTML, want) {
			t.Errorf("Russian message lacks %q:\n%s", want, m.TelegramHTML)
		}
	}
	if m.EmailSubject != "Вероятная причина: shop / payment" {
		t.Errorf("subject = %q", m.EmailSubject)
	}
}

func TestRenderFollowUpEscapesAndTrims(t *testing.T) {
	f := &FollowUp{
		System: "a<b>", Window: 30 * time.Minute,
		Roots: []RootEvidence{{Service: "svc", Title: "x & y", LogLine: strings.Repeat("token ", 200) + "\n second line"}},
	}
	m := RenderFollowUp(f, "xx", "")
	if strings.Contains(m.TelegramHTML, "a<b>") || !strings.Contains(m.TelegramHTML, "a&lt;b&gt;") || !strings.Contains(m.TelegramHTML, "x &amp; y") {
		t.Errorf("not escaped: %s", m.TelegramHTML)
	}
	for _, line := range strings.Split(m.EmailText, "\n") {
		if strings.HasPrefix(line, "Log:") && len([]rune(line)) > maxLogLine+10 {
			t.Errorf("log line not trimmed: %d runes", len([]rune(line)))
		}
	}
	if strings.Contains(m.EmailText, "\n second line") {
		t.Error("log line is not a single line")
	}
	f.Roots[0].TraceURL = "javascript:alert(1)"
	if m := RenderFollowUp(f, "en", ""); strings.Contains(m.TelegramHTML, "href") {
		t.Errorf("unsafe link rendered: %s", m.TelegramHTML)
	}
}

func TestFollowUpWorth(t *testing.T) {
	cases := []struct {
		name string
		f    *FollowUp
		want bool
	}{
		{"nil", nil, false},
		{"root alone", &FollowUp{Roots: []RootEvidence{{Service: "a", Title: "t"}}}, false},
		{"trace link only", &FollowUp{Roots: []RootEvidence{{Service: "a", TraceURL: "https://g"}}}, false},
		{"affected", &FollowUp{Affected: []string{"b"}, Roots: []RootEvidence{{Service: "a"}}}, true},
		{"caller alerting", &FollowUp{Alerting: []string{"b"}, Roots: []RootEvidence{{Service: "a"}}}, true},
		{"restart", &FollowUp{Roots: []RootEvidence{{Service: "a", Changes: WorkloadChanges{Restarts: []Restart{{Container: "a"}}}}}}, true},
		{"operation", &FollowUp{Roots: []RootEvidence{{Service: "a", Operations: []OperationErrors{{Operation: "x", Failed: 1}}}}}, true},
		{"log", &FollowUp{Roots: []RootEvidence{{Service: "a", LogLine: "boom"}}}, true},
	}
	for _, tc := range cases {
		if got := tc.f.Worth(); got != tc.want {
			t.Errorf("%s: Worth = %v, want %v", tc.name, got, tc.want)
		}
	}
}
