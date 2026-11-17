// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// The follow-up message: what Kamerton found about the probable cause of a
// firing notification. Alertmanager delivers the notification itself; the
// core sends this second message (internal/followup) through the same bot
// and mail server. Unlike the Alertmanager templates in message.go it is
// rendered here, in Go, from the evidence collected at the time.
//
// Telegram parse mode HTML allows only a few tags; the message uses <b>, <i>
// and <a href>, and every value is escaped with html.EscapeString. The email
// HTML is the same text in a pre-line block, like the alert email; the plain
// text has no tags.

// FollowUp is the evidence about one incident.
type FollowUp struct {
	System      string
	Environment string
	// Alerting are the services of the notification that are not roots
	// (their alert is a consequence of a root). Empty when the notification
	// is about a root itself.
	Alerting []string
	// Roots are the probable causes with their evidence.
	Roots []RootEvidence
	// Affected are the other services whose alerts follow from the roots.
	Affected []string
	// Window is how far back "what changed" looks.
	Window time.Duration
}

// RootEvidence is what was found about one root service. Every part is
// optional.
type RootEvidence struct {
	Service string
	// Title is the plain-words title of the root's alert, Since its start.
	Title string
	Since time.Time
	// Changes in the workload of the service within the window.
	Changes WorkloadChanges
	// Operations with the most failed calls in the window.
	Operations []OperationErrors
	// LogLine is one error line from the logs of the service.
	LogLine string
	// TraceURL opens failing traces of the service in Grafana.
	TraceURL string
}

// WorkloadChanges is what changed in the pods of a service.
type WorkloadChanges struct {
	Rollouts []Rollout
	Restarts []Restart
	// NotReady of Pods are not ready.
	NotReady, Pods int
	Warnings       []Warning
}

// Empty reports whether nothing changed.
func (c WorkloadChanges) Empty() bool {
	return len(c.Rollouts) == 0 && len(c.Restarts) == 0 && c.NotReady == 0 && len(c.Warnings) == 0
}

// Rollout is a new version of a Deployment or StatefulSet.
type Rollout struct {
	// Workload is the Deployment or StatefulSet name.
	Workload string
	At       time.Time
	// OldImage and NewImage are image tags (or short digests); OldImage is
	// empty when unknown.
	OldImage, NewImage string
}

// Restart is a container that restarted within the window.
type Restart struct {
	Pod, Container string
	Count          int
	// Reason is the last termination reason (OOMKilled, Error, ...).
	Reason string
	// ExitCode of the last termination; 0 when unknown.
	ExitCode int32
	// CrashLoop: the container is waiting in CrashLoopBackOff now.
	CrashLoop bool
}

// Warning is a Kubernetes Warning event.
type Warning struct {
	Reason, Message string
	Count           int32
}

// OperationErrors is the number of failed calls of one operation.
type OperationErrors struct {
	Operation string
	Failed    float64
}

// Worth reports whether the evidence says more than the notification did:
// other affected services, a change in the workload, a failing operation or
// a log line. A trace link alone is not worth a message.
func (f *FollowUp) Worth() bool {
	if f == nil {
		return false
	}
	if len(f.Affected) > 0 || len(f.Alerting) > 0 {
		return true
	}
	for _, r := range f.Roots {
		if !r.Changes.Empty() || len(r.Operations) > 0 || r.LogLine != "" {
			return true
		}
	}
	return false
}

// FollowUpMessage is the rendered follow-up in every format.
type FollowUpMessage struct {
	TelegramHTML string
	EmailSubject string
	EmailHTML    string
	EmailText    string
}

type followUpWords struct {
	header, causeIs, cause, since, alsoAffected string
	changed                                     string // service, window
	rollout, restarted, restartedTimes          string
	exitCode, crashLoop, notReady, warning      string
	mostErrors                                  string // operation, count, window
	log, quoteOpen, quoteClose, traces          string
	minutes, and                                string
}

var followUpTexts = map[string]followUpWords{
	rules.LanguageEnglish: {
		header: "Probable cause", causeIs: "probable cause is", cause: "Probable cause:", since: "since",
		alsoAffected: "Also affected:",
		changed:      "What changed in %s in the last %s:",
		rollout:      "new version at %s", restarted: "container %s restarted", restartedTimes: "container %s restarted %d times",
		exitCode: "exit code %d", crashLoop: "keeps crashing (CrashLoopBackOff)", notReady: "%d of %d pods not ready",
		warning:    "warning %s: %s",
		mostErrors: "Most errors: operation %s (%s failed calls in %s).",
		log:        "Log:", quoteOpen: "“", quoteClose: "”", traces: "Failing traces in Grafana",
		minutes: "%d min", and: "and",
	},
	rules.LanguageRussian: {
		header: "Вероятная причина", causeIs: "вероятная причина в", cause: "Вероятная причина:", since: "с",
		alsoAffected: "Также затронуты:",
		changed:      "Что изменилось в %s за последние %s:",
		rollout:      "новая версия в %s", restarted: "контейнер %s перезапустился", restartedTimes: "контейнер %s перезапускался, раз: %d",
		exitCode: "код выхода %d", crashLoop: "падает снова и снова (CrashLoopBackOff)", notReady: "не готовы подов: %d из %d",
		warning:    "предупреждение %s: %s",
		mostErrors: "Больше всего ошибок в операции %s: %s за %s.",
		log:        "Лог:", quoteOpen: "«", quoteClose: "»", traces: "Трассы с ошибками в Grafana",
		minutes: "%d мин", and: "и",
	},
}

// maxLogLine caps the log line quoted in a message.
const maxLogLine = 300

// RenderFollowUp renders the follow-up in the language and time zone of the
// description (spec.notifications.language and timeZone).
func RenderFollowUp(f *FollowUp, lang, timeZone string) FollowUpMessage {
	w, ok := followUpTexts[lang]
	if !ok {
		w = followUpTexts[rules.LanguageEnglish]
	}
	loc, err := description.LoadTimeZone(timeZone)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	tg := followUpBody(f, w, loc, true)
	subject := w.header + ": " + f.System
	if len(f.Roots) > 0 {
		subject += " / " + strings.Join(rootNames(f.Roots), ", ")
	}
	return FollowUpMessage{
		TelegramHTML: tg,
		EmailSubject: subject,
		EmailHTML:    `<div style="font-family:sans-serif;white-space:pre-line">` + tg + `</div>`,
		EmailText:    followUpBody(f, w, loc, false),
	}
}

func rootNames(roots []RootEvidence) []string {
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = r.Service
	}
	return out
}

func followUpBody(f *FollowUp, w followUpWords, loc *time.Location, htmlMode bool) string {
	esc := func(s string) string {
		if htmlMode {
			return html.EscapeString(s)
		}
		return s
	}
	b := func(s string) string {
		if htmlMode {
			return "<b>" + s + "</b>"
		}
		return s
	}
	at := func(t time.Time) string {
		lt := t.In(loc)
		_, off := lt.Zone()
		return lt.Format("15:04") + " " + OffsetLabel(off)
	}
	minutes := fmt.Sprintf(w.minutes, int(f.Window.Minutes()))
	join := func(items []string) string {
		if len(items) <= 1 {
			return strings.Join(items, "")
		}
		return strings.Join(items[:len(items)-1], ", ") + " " + w.and + " " + items[len(items)-1]
	}

	var t strings.Builder
	t.WriteString("🔎 " + b(esc(w.header)) + " · " + esc(f.System))
	if f.Environment != "" {
		t.WriteString(" (" + esc(f.Environment) + ")")
	}
	t.WriteString("\n")

	// Cause by graph.
	var roots, details []string
	for _, r := range f.Roots {
		roots = append(roots, b(esc(r.Service)))
		d := esc(r.Service)
		if r.Title != "" {
			d += ": " + esc(r.Title)
		}
		if !r.Since.IsZero() {
			d += " " + w.since + " " + at(r.Since)
		}
		details = append(details, d)
	}
	cause := join(roots) + " (" + strings.Join(details, "; ") + ")."
	if len(f.Alerting) > 0 {
		names := make([]string, len(f.Alerting))
		for i, s := range f.Alerting {
			names[i] = b(esc(s))
		}
		t.WriteString(strings.Join(names, ", ") + ": " + w.causeIs + " " + cause + "\n")
	} else {
		t.WriteString(w.cause + " " + cause + "\n")
	}
	if len(f.Affected) > 0 {
		names := make([]string, len(f.Affected))
		for i, s := range f.Affected {
			names[i] = esc(s)
		}
		t.WriteString(w.alsoAffected + " " + strings.Join(names, ", ") + ".\n")
	}

	for _, r := range f.Roots {
		if parts := changeParts(r.Changes, w, at, esc); len(parts) > 0 {
			t.WriteString(fmt.Sprintf(w.changed, b(esc(r.Service)), minutes) + " " + strings.Join(parts, "; ") + ".\n")
		}
		if len(r.Operations) > 0 {
			op := r.Operations[0]
			t.WriteString(fmt.Sprintf(w.mostErrors, b(esc(op.Operation)), strconv.FormatFloat(op.Failed, 'f', 0, 64), minutes) + "\n")
		}
		if line := trimLogLine(r.LogLine); line != "" {
			prefix := w.log
			if len(f.Roots) > 1 {
				prefix = esc(r.Service) + " " + w.log
			}
			q := w.quoteOpen + esc(line) + w.quoteClose
			if htmlMode {
				q = "<i>" + q + "</i>"
			}
			t.WriteString(prefix + " " + q + "\n")
		}
		if u := safeLink(r.TraceURL); u != "" {
			text := w.traces
			if len(f.Roots) > 1 {
				text += " (" + r.Service + ")"
			}
			if htmlMode {
				t.WriteString(`<a href="` + html.EscapeString(u) + `">` + html.EscapeString(text) + "</a>\n")
			} else {
				t.WriteString(text + ": " + u + "\n")
			}
		}
	}
	return t.String()
}

func changeParts(c WorkloadChanges, w followUpWords, at func(time.Time) string, esc func(string) string) []string {
	var parts []string
	for _, r := range c.Rollouts {
		p := fmt.Sprintf(w.rollout, at(r.At))
		switch {
		case r.OldImage != "" && r.NewImage != "":
			p += " (" + esc(r.OldImage) + " -> " + esc(r.NewImage) + ")"
		case r.NewImage != "":
			p += " (" + esc(r.NewImage) + ")"
		}
		parts = append(parts, p)
	}
	for _, r := range c.Restarts {
		p := fmt.Sprintf(w.restarted, esc(r.Container))
		if r.Count > 1 {
			p = fmt.Sprintf(w.restartedTimes, esc(r.Container), r.Count)
		}
		var why []string
		if r.Reason != "" {
			why = append(why, esc(r.Reason))
		}
		if r.ExitCode != 0 {
			why = append(why, fmt.Sprintf(w.exitCode, r.ExitCode))
		}
		if len(why) > 0 {
			p += " (" + strings.Join(why, ", ") + ")"
		}
		if r.CrashLoop {
			p += ", " + w.crashLoop
		}
		parts = append(parts, p)
	}
	if c.NotReady > 0 {
		parts = append(parts, fmt.Sprintf(w.notReady, c.NotReady, c.Pods))
	}
	for _, e := range c.Warnings {
		parts = append(parts, fmt.Sprintf(w.warning, esc(e.Reason), esc(trimLine(e.Message, 160))))
	}
	return parts
}

// trimLogLine makes one short line of a log line.
func trimLogLine(s string) string { return trimLine(s, maxLogLine) }

func trimLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}

// safeLink returns an absolute http(s) URL or "".
func safeLink(u string) string {
	if u == "" || (!strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://")) {
		return ""
	}
	return u
}
