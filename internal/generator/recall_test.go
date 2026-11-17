// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strings"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/internal/rules"
)

func sampleRecall() *Recall {
	return &Recall{
		StartedAt: time.Date(2026, 10, 27, 9, 2, 0, 0, time.UTC), Duration: 72 * time.Minute,
		Cause: "payment pods <OOMKilled> after a release", Fix: "rolled back & raised the limit", Count30d: 4, Noted: true,
	}
}

func TestRenderRecallBlock(t *testing.T) {
	for _, tc := range []struct {
		lang string
		want []string
	}{
		{rules.LanguageEnglish, []string{
			"🕘 <b>Happened before</b>: 14:02 27.10 UTC+5, lasted 1 h 12 min.\n",
			"Cause: payment pods &lt;OOMKilled&gt; after a release\n",
			"What helped: rolled back &amp; raised the limit\n",
			"Times in the last 30 days: 4.\n",
		}},
		{rules.LanguageRussian, []string{
			"🕘 <b>Уже было</b>: 14:02 27.10 UTC+5, длилось 1 ч 12 мин.\n",
			"Причина: payment pods &lt;OOMKilled&gt; after a release\n",
			"Что помогло: rolled back &amp; raised the limit\n",
			"Случаев за последние 30 дней: 4.\n",
		}},
	} {
		f := sampleFollowUp()
		f.Previous = sampleRecall()
		m := RenderFollowUp(f, tc.lang, "Asia/Almaty")
		for _, w := range tc.want {
			if !strings.Contains(m.TelegramHTML, w) {
				t.Errorf("%s: lacks %q:\n%s", tc.lang, w, m.TelegramHTML)
			}
		}
		if !strings.Contains(m.EmailText, "rolled back & raised the limit") || strings.Contains(m.EmailText, "<b>") {
			t.Errorf("%s: plain text %q", tc.lang, m.EmailText)
		}
	}
	// Without a note and with no earlier case in 30 days: no fix line, no count.
	r := &Recall{StartedAt: time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC), Duration: 30 * time.Second, Cause: "payment: No ready pods"}
	got := recallBlock(r, rules.LanguageEnglish, time.UTC, func(s string) string { return s }, func(s string) string { return s })
	if got != "🕘 Happened before: 04:00 01.09 UTC, lasted under a minute.\nCause: payment: No ready pods\n" {
		t.Errorf("block %q", got)
	}
	if recallBlock(nil, rules.LanguageEnglish, time.UTC, nil, nil) != "" {
		t.Error("nil recall renders nothing")
	}
}

func TestRecallNoteIsWorthAFollowUp(t *testing.T) {
	f := &FollowUp{System: "shop", Roots: []RootEvidence{{Service: "payment"}}}
	if f.Worth() {
		t.Fatal("bare follow-up is worth nothing")
	}
	f.Previous = &Recall{StartedAt: time.Now(), Cause: "collected cause"}
	if f.Worth() {
		t.Error("a recall without a person's note is not worth a message alone")
	}
	f.Previous.Noted = true
	if !f.Worth() {
		t.Error("a note is worth a follow-up")
	}
}

func TestRenderIncidentOver(t *testing.T) {
	o := IncidentOver{System: "shop", Environment: "prod", Roots: []string{"payment"}, Title: "No <ready> pods", Duration: 12*time.Minute + 20*time.Second}
	en := RenderIncidentOver(o, rules.LanguageEnglish)
	want := "✅ <b>Incident over</b> · shop (prod): <b>payment</b> (No &lt;ready&gt; pods), lasted 12 min.\n" +
		"What helped? Reply to this message, and Kamerton will remind you the next time."
	if en != want {
		t.Errorf("en:\n%s\nwant\n%s", en, want)
	}
	o.Duration = 2 * time.Hour
	ru := RenderIncidentOver(o, rules.LanguageRussian)
	want = "✅ <b>Инцидент закончился</b> · shop (prod): <b>payment</b> (No &lt;ready&gt; pods), длился 2 ч.\n" +
		"Что помогло? Ответьте на это сообщение, и в следующий раз Kamerton напомнит."
	if ru != want {
		t.Errorf("ru:\n%s\nwant\n%s", ru, want)
	}
}

func TestRenderIncidentOverWithAftermath(t *testing.T) {
	o := IncidentOver{System: "shop", Roots: []string{"payment"}, Title: "No ready pods", Duration: 11 * time.Minute,
		StillFiring: []Aftermath{{Service: "checkout", Alerts: []string{rules.AlertSLOBurnSlow}}}}
	en := RenderIncidentOver(o, rules.LanguageEnglish)
	if !strings.Contains(en, "\nStill firing after it: checkout (SLO error budget is burning). This is expected for a while after an outage.\nWhat helped?") {
		t.Errorf("en:\n%s", en)
	}
	ru := RenderIncidentOver(o, rules.LanguageRussian)
	if !strings.Contains(ru, "\nЕщё срабатывают после него: checkout (Бюджет ошибок SLO сгорает). Так бывает какое-то время после сбоя.\nЧто помогло?") {
		t.Errorf("ru:\n%s", ru)
	}
}
