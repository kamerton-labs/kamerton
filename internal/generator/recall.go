// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/internal/rules"
)

// Recall is the earlier incident a follow-up reminds of. Noted: Cause or Fix
// was written by a person, which alone is worth a follow-up.
type Recall struct {
	StartedAt time.Time
	Duration  time.Duration
	Cause     string
	Fix       string
	Count30d  int
	Noted     bool
}

// IncidentOver is the message after an incident that had a follow-up: how
// long it lasted and a request to reply with what helped.
type IncidentOver struct {
	System      string
	Environment string
	Roots       []string
	Title       string
	Duration    time.Duration
}

type recallWords struct {
	before, lasted, cause, fix, count         string
	over, overLasted, ask                     string
	underMinute, minutes, hours, hoursMinutes string
}

var recallTexts = map[string]recallWords{
	rules.LanguageEnglish: {
		before: "Happened before", lasted: "lasted %s", cause: "Cause:", fix: "What helped:",
		count: "Times in the last 30 days: %d.",
		over:  "Incident over", overLasted: "lasted %s",
		ask:         "What helped? Reply to this message, and Kamerton will remind you the next time.",
		underMinute: "under a minute", minutes: "%d min", hours: "%d h", hoursMinutes: "%d h %d min",
	},
	rules.LanguageRussian: {
		before: "Уже было", lasted: "длилось %s", cause: "Причина:", fix: "Что помогло:",
		count: "Случаев за последние 30 дней: %d.",
		over:  "Инцидент закончился", overLasted: "длился %s",
		ask:         "Что помогло? Ответьте на это сообщение, и в следующий раз Kamerton напомнит.",
		underMinute: "меньше минуты", minutes: "%d мин", hours: "%d ч", hoursMinutes: "%d ч %d мин",
	},
}

func recallLanguage(lang string) recallWords {
	if w, ok := recallTexts[lang]; ok {
		return w
	}
	return recallTexts[rules.LanguageEnglish]
}

func duration(d time.Duration, w recallWords) string {
	m := int(d.Round(time.Minute) / time.Minute)
	switch {
	case d < time.Minute:
		return w.underMinute
	case m < 60:
		return fmt.Sprintf(w.minutes, m)
	case m%60 == 0:
		return fmt.Sprintf(w.hours, m/60)
	}
	return fmt.Sprintf(w.hoursMinutes, m/60, m%60)
}

// recallBlock renders r with the follow-up's escaping; "" for nil.
func recallBlock(r *Recall, lang string, loc *time.Location, esc, b func(string) string) string {
	if r == nil || r.StartedAt.IsZero() {
		return ""
	}
	w := recallLanguage(lang)
	lt := r.StartedAt.In(loc)
	_, off := lt.Zone()
	var t strings.Builder
	t.WriteString("🕘 " + b(esc(w.before)) + ": " + lt.Format("15:04 02.01") + " " + OffsetLabel(off))
	if r.Duration > 0 {
		t.WriteString(", " + fmt.Sprintf(w.lasted, duration(r.Duration, w)))
	}
	t.WriteString(".\n")
	if c := trimLine(r.Cause, maxLogLine); c != "" {
		t.WriteString(w.cause + " " + esc(c) + "\n")
	}
	if f := strings.TrimSpace(r.Fix); f != "" {
		t.WriteString(w.fix + " " + esc(f) + "\n")
	}
	if r.Count30d > 0 {
		t.WriteString(fmt.Sprintf(w.count, r.Count30d) + "\n")
	}
	return t.String()
}

// RenderIncidentOver returns Telegram HTML; values are escaped.
func RenderIncidentOver(o IncidentOver, lang string) string {
	w := recallLanguage(lang)
	var t strings.Builder
	t.WriteString("✅ <b>" + html.EscapeString(w.over) + "</b> · " + html.EscapeString(o.System))
	if o.Environment != "" {
		t.WriteString(" (" + html.EscapeString(o.Environment) + ")")
	}
	if len(o.Roots) > 0 {
		t.WriteString(": <b>" + html.EscapeString(strings.Join(o.Roots, ", ")) + "</b>")
	}
	if o.Title != "" {
		t.WriteString(" (" + html.EscapeString(trimLine(o.Title, 160)) + ")")
	}
	t.WriteString(", " + fmt.Sprintf(w.overLasted, duration(o.Duration, w)) + ".\n")
	t.WriteString(w.ask)
	return t.String()
}
