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

// Telegram HTML parse mode allows only a few tags: the follow-up uses <b>, <i> and
// <a href> and escapes every value.

type FollowUp struct {
	System      string
	Environment string
	// Alerting are the services of the notification that are not roots; empty
	// when the notification is about a root itself.
	Alerting []string
	Roots    []RootEvidence
	Affected []string
	Window   time.Duration
	// Likely are dependencies of a root that look failing now although none
	// of their alerts fired yet.
	Likely []LikelyCause
	// Explains are services named by earlier follow-ups that this incident's
	// roots turn out to cause; ExplainsSince is when the earliest one started.
	Explains      []string
	ExplainsSince time.Time
	// Previous is the earlier incident of the same root; nil when none.
	Previous *Recall
}

type LikelyCause struct {
	Service string
	// Of is the root that calls it.
	Of       string
	Observed []Observation
	Evidence RootEvidence
}

// Observation is a rule whose condition holds now; Pending: Prometheus
// reports its alert as pending, otherwise the condition was queried directly.
type Observation struct {
	Alert   string
	Pending bool
}

// RootEvidence parts are all optional.
type RootEvidence struct {
	Service    string
	Title      string
	Since      time.Time
	Changes    WorkloadChanges
	Operations []OperationErrors
	LogLine    string
	TraceURL   string
}

type WorkloadChanges struct {
	Rollouts       []Rollout
	Scalings       []Scaling
	Restarts       []Restart
	NotReady, Pods int
	// NoPods: the workload exists but has no pod at all.
	NoPods   bool
	Warnings []Warning
}

func (c WorkloadChanges) Empty() bool {
	return len(c.Rollouts) == 0 && len(c.Scalings) == 0 && len(c.Restarts) == 0 && c.NotReady == 0 && !c.NoPods && len(c.Warnings) == 0
}

// Scaling is a change of a workload's replica count. From is -1 and At zero
// when unknown (the scaling event is older than the window or gone).
type Scaling struct {
	Workload string
	At       time.Time
	From, To int
}

type Rollout struct {
	Workload string
	At       time.Time
	// OldImage and NewImage are image tags (or short digests); OldImage is
	// empty when unknown.
	OldImage, NewImage string
}

type Restart struct {
	Pod, Container string
	Count          int
	Reason         string
	// ExitCode of the last termination; 0 when unknown.
	ExitCode  int32
	CrashLoop bool
}

type Warning struct {
	Reason, Message string
	Count           int32
}

type OperationErrors struct {
	Operation string
	Failed    float64
}

// Worth reports whether the evidence says more than the notification did.
// A trace link alone is not worth a message.
func (f *FollowUp) Worth() bool {
	if f == nil {
		return false
	}
	if len(f.Affected) > 0 || len(f.Alerting) > 0 || len(f.Likely) > 0 || len(f.Explains) > 0 || (f.Previous != nil && f.Previous.Noted) {
		return true
	}
	for _, r := range f.Roots {
		if !r.Changes.Empty() || len(r.Operations) > 0 || r.LogLine != "" {
			return true
		}
	}
	return false
}

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
	scaledFrom, scaledTo, at, noPods            string
	mostErrors                                  string // operation, count, window
	log, quoteOpen, quoteClose, traces          string
	minutes, and                                string
	// likely takes the dependency, the root and the observations; explains
	// the services and their start time.
	alert, likely, pending, explains string
}

var followUpTexts = map[string]followUpWords{
	rules.LanguageEnglish: {
		header: "Probable cause", causeIs: "probable cause is", cause: "Probable cause:", since: "since",
		alsoAffected: "Also affected:",
		changed:      "What changed in %s in the last %s:",
		rollout:      "new version at %s", restarted: "container %s restarted", restartedTimes: "container %s restarted %d times",
		exitCode: "exit code %d", crashLoop: "keeps crashing (CrashLoopBackOff)", notReady: "%d of %d pods not ready",
		scaledFrom: "%s scaled from %d to %d replicas", scaledTo: "%s scaled to %d replicas", at: "at %s", noPods: "no pods exist",
		warning:    "warning %s: %s",
		mostErrors: "Most errors: operation %s (%s failed calls in %s).",
		log:        "Log:", quoteOpen: "“", quoteClose: "”", traces: "Failing traces in Grafana",
		minutes: "%d min", and: "and",
		alert:    "Alert:",
		likely:   "Likely cause: %s, called by %s (observed now: %s). Its own alerts have not fired yet.",
		pending:  "alert pending",
		explains: "This explains the earlier alert about %s (since %s).",
	},
	rules.LanguageRussian: {
		header: "Вероятная причина", causeIs: "вероятная причина в", cause: "Вероятная причина:", since: "с",
		alsoAffected: "Также затронуты:",
		changed:      "Что изменилось в %s за последние %s:",
		rollout:      "новая версия в %s", restarted: "контейнер %s перезапустился", restartedTimes: "контейнер %s перезапускался, раз: %d",
		exitCode: "код выхода %d", crashLoop: "падает снова и снова (CrashLoopBackOff)", notReady: "не готовы подов: %d из %d",
		scaledFrom: "число реплик %s изменено с %d до %d", scaledTo: "число реплик %s изменено до %d", at: "в %s", noPods: "подов нет",
		warning:    "предупреждение %s: %s",
		mostErrors: "Больше всего ошибок в операции %s: %s за %s.",
		log:        "Лог:", quoteOpen: "«", quoteClose: "»", traces: "Трассы с ошибками в Grafana",
		minutes: "%d мин", and: "и",
		alert:    "Оповещение:",
		likely:   "Вероятно, причина в %s, его вызывает %s (сейчас видно: %s). Его собственные оповещения ещё не сработали.",
		pending:  "оповещение ожидает срабатывания",
		explains: "Это причина более раннего оповещения о %s (с %s).",
	},
}

const maxLogLine = 300

func RenderFollowUp(f *FollowUp, lang, timeZone string) FollowUpMessage {
	w, ok := followUpTexts[lang]
	if !ok {
		w = followUpTexts[rules.LanguageEnglish]
	}
	loc, err := description.LoadTimeZone(timeZone)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	tg := followUpBody(f, w, lang, loc, true)
	subject := w.header + ": " + f.System
	if len(f.Roots) > 0 {
		subject += " / " + strings.Join(rootNames(f.Roots), ", ")
	}
	return FollowUpMessage{
		TelegramHTML: tg,
		EmailSubject: subject,
		EmailHTML:    `<div style="font-family:sans-serif;white-space:pre-line">` + tg + `</div>`,
		EmailText:    followUpBody(f, w, lang, loc, false),
	}
}

func rootNames(roots []RootEvidence) []string {
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = r.Service
	}
	return out
}

func followUpBody(f *FollowUp, w followUpWords, lang string, loc *time.Location, htmlMode bool) string {
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
	at := clockAt(loc)
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

	var roots, details []string
	for _, r := range f.Roots {
		roots = append(roots, b(esc(r.Service)))
		details = append(details, esc(rootCause(r, w, at)))
	}
	cause := join(roots) + " (" + strings.Join(details, "; ") + ")."
	switch {
	case len(f.Alerting) == 0 && len(f.Likely) > 0:
		t.WriteString(w.alert + " " + cause + "\n")
	case len(f.Alerting) > 0:
		names := make([]string, len(f.Alerting))
		for i, s := range f.Alerting {
			names[i] = b(esc(s))
		}
		t.WriteString(strings.Join(names, ", ") + ": " + w.causeIs + " " + cause + "\n")
	default:
		t.WriteString(w.cause + " " + cause + "\n")
	}
	for _, l := range f.Likely {
		t.WriteString(fmt.Sprintf(w.likely, b(esc(l.Service)), esc(l.Of), esc(observed(l.Observed, w, lang))) + "\n")
	}
	if len(f.Explains) > 0 {
		names := make([]string, len(f.Explains))
		for i, s := range f.Explains {
			names[i] = b(esc(s))
		}
		t.WriteString(fmt.Sprintf(w.explains, join(names), at(f.ExplainsSince)) + "\n")
	}
	if len(f.Affected) > 0 {
		names := make([]string, len(f.Affected))
		for i, s := range f.Affected {
			names[i] = esc(s)
		}
		t.WriteString(w.alsoAffected + " " + strings.Join(names, ", ") + ".\n")
	}

	all := append([]RootEvidence{}, f.Roots...)
	for _, l := range f.Likely {
		if l.Evidence.Service != "" {
			all = append(all, l.Evidence)
		}
	}
	for _, r := range all {
		if parts := changeParts(r.Changes, w, at, esc); len(parts) > 0 {
			t.WriteString(fmt.Sprintf(w.changed, b(esc(r.Service)), minutes) + " " + strings.Join(parts, "; ") + ".\n")
		}
		if len(r.Operations) > 0 {
			op := r.Operations[0]
			t.WriteString(fmt.Sprintf(w.mostErrors, b(esc(op.Operation)), strconv.FormatFloat(op.Failed, 'f', 0, 64), minutes) + "\n")
		}
		if line := trimLogLine(r.LogLine); line != "" {
			prefix := w.log
			if len(all) > 1 {
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
			if len(all) > 1 {
				text += " (" + r.Service + ")"
			}
			if htmlMode {
				t.WriteString(`<a href="` + html.EscapeString(u) + `">` + html.EscapeString(text) + "</a>\n")
			} else {
				t.WriteString(text + ": " + u + "\n")
			}
		}
	}
	t.WriteString(recallBlock(f.Previous, lang, loc, esc, b))
	return t.String()
}

func clockAt(loc *time.Location) func(time.Time) string {
	return func(t time.Time) string {
		lt := t.In(loc)
		_, off := lt.Zone()
		return lt.Format("15:04") + " " + OffsetLabel(off)
	}
}

func rootCause(r RootEvidence, w followUpWords, at func(time.Time) string) string {
	d := r.Service
	if r.Title != "" {
		d += ": " + r.Title
	}
	if !r.Since.IsZero() {
		d += " " + w.since + " " + at(r.Since)
	}
	return d
}

func observed(obs []Observation, w followUpWords, lang string) string {
	parts := make([]string, 0, len(obs))
	for _, o := range obs {
		p := rules.Title(lang, o.Alert)
		if p == "" {
			p = o.Alert
		}
		if o.Pending {
			p += " (" + w.pending + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

// EvidenceSummary is the evidence of an incident as the alerts API returns
// it; strings are rendered in the description's language and time zone.
type EvidenceSummary struct {
	CollectedAt       time.Time `json:"collectedAt"`
	Cause             string    `json:"cause,omitempty"`
	Likely            bool      `json:"likely"`
	Changes           []string  `json:"changes,omitempty"`
	Operation         string    `json:"operation,omitempty"`
	OperationFailures int       `json:"operationFailures,omitempty"`
	LogLine           string    `json:"logLine,omitempty"`
	TracesURL         string    `json:"tracesURL,omitempty"`
	// Explains names services of earlier incidents this one causes.
	Explains []string `json:"explains,omitempty"`
}

// Summarize describes the first root, or the first likely cause when there is
// one; a part the likely cause lacks comes from the root.
func Summarize(f *FollowUp, lang, timeZone string, collected time.Time) EvidenceSummary {
	w, ok := followUpTexts[lang]
	if !ok {
		w = followUpTexts[rules.LanguageEnglish]
	}
	loc, err := description.LoadTimeZone(timeZone)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	at := clockAt(loc)
	out := EvidenceSummary{CollectedAt: collected.UTC().Truncate(time.Second), Explains: f.Explains}
	var ev []RootEvidence
	if len(f.Likely) > 0 {
		l := f.Likely[0]
		out.Cause, out.Likely = l.Service+": "+observed(l.Observed, w, lang), true
		ev = append(ev, l.Evidence)
	}
	if len(f.Roots) > 0 {
		if out.Cause == "" {
			out.Cause = rootCause(f.Roots[0], w, at)
		}
		ev = append(ev, f.Roots[0])
	}
	plain := func(s string) string { return s }
	for _, r := range ev {
		if out.Changes == nil {
			out.Changes = changeParts(r.Changes, w, at, plain)
		}
		if out.Operation == "" && len(r.Operations) > 0 {
			out.Operation, out.OperationFailures = r.Operations[0].Operation, int(r.Operations[0].Failed+0.5)
		}
		if out.LogLine == "" {
			out.LogLine = trimLogLine(r.LogLine)
		}
		if out.TracesURL == "" {
			out.TracesURL = safeLink(r.TraceURL)
		}
	}
	return out
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
	for _, sc := range c.Scalings {
		p := fmt.Sprintf(w.scaledTo, esc(sc.Workload), sc.To)
		if sc.From >= 0 {
			p = fmt.Sprintf(w.scaledFrom, esc(sc.Workload), sc.From, sc.To)
		}
		if !sc.At.IsZero() {
			p += " " + fmt.Sprintf(w.at, at(sc.At))
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
	if c.NoPods {
		parts = append(parts, w.noPods)
	}
	for _, e := range c.Warnings {
		parts = append(parts, fmt.Sprintf(w.warning, esc(e.Reason), esc(trimLine(e.Message, 160))))
	}
	return parts
}

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
