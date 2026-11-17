// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/kamerton-labs/kamerton/internal/rules"
)

// Notification message templates.
//
// They are Alertmanager templates (Go text/template syntax). Alertmanager
// executes a Telegram message with parse mode HTML and an email html body
// with html/template, so label and annotation values are HTML-escaped by
// Alertmanager itself; the email text body and subject are executed with
// text/template. The templates only use what Alertmanager v0.25+ provides:
// .Status, .CommonLabels, .Alerts.Firing, .Alerts.Resolved, alert fields,
// time.Time methods and Alertmanager's built-in reReplaceAll; no custom functions. The output uses only tags allowed
// by Telegram's HTML parse mode (<b>, <i>, <a href>).
//
// Times are shown in spec.notifications.timeZone (see zoneClock): Kamerton
// computes the UTC offsets of the zone and the template only adds a constant
// to the UTC time, so nothing depends on the time-zone database or the
// version of the Alertmanager that sends the message.
//
// Links point only to Kamerton's external URL (the alert in the UI and the
// runbook). Without an external URL the message has no links: in-cluster
// addresses such as the Prometheus generator URL are useless on a phone.

// maxListedAlerts bounds the alerts listed in one grouped message, so the
// message stays readable and below Telegram's 4096-character limit.
const maxListedAlerts = 8

type messageWords struct {
	critical, warning, resolved, problem  string
	now, threshold, since, until, runbook string
	open, shown, more, alsoResolved       string
	subjectFiring, subjectResolved        string
}

var words = map[string]messageWords{
	rules.LanguageRussian: {
		critical: "Критично", warning: "Предупреждение", resolved: "Решено", problem: "Проблема",
		now: "сейчас", threshold: "порог", since: "с", until: "до", runbook: "Что делать",
		open: "Открыть в Kamerton", shown: "показаны первые", more: "из", alsoResolved: "Уже решено",
		subjectFiring: "Проблема", subjectResolved: "Решено",
	},
	rules.LanguageEnglish: {
		critical: "Critical", warning: "Warning", resolved: "Resolved", problem: "Problem",
		now: "now", threshold: "threshold", since: "since", until: "until", runbook: "Runbook",
		open: "Open in Kamerton", shown: "showing first", more: "of", alsoResolved: "Already resolved",
		subjectFiring: "Problem", subjectResolved: "Resolved",
	},
}

// messageTemplates are the rendered templates for one description.
type messageTemplates struct {
	TelegramHTML string
	EmailHTML    string
	EmailText    string
	EmailSubject string
}

// buildMessages renders the templates for a language, an external URL
// (empty: no links) and the clock of the time zone.
func buildMessages(lang, externalURL string, clock zoneClock) messageTemplates {
	w, ok := words[lang]
	if !ok {
		w = words[rules.LanguageEnglish]
	}
	base := safeBaseURL(externalURL)
	tg := messageBody(w, base, true, clock)
	textBase := html.UnescapeString(base)
	return messageTemplates{
		TelegramHTML: tg,
		EmailHTML:    `<div style="font-family:sans-serif;white-space:pre-line">` + tg + `</div>`,
		EmailText:    messageBody(w, textBase, false, clock),
		EmailSubject: `{{ if eq .Status "firing" }}` + w.subjectFiring + `{{ else }}` + w.subjectResolved + `{{ end }}: ` +
			`{{ .CommonLabels.kamerton_system }}{{ with .CommonLabels.service }} / {{ . }}{{ end }}` +
			`{{ if .CommonAnnotations.kamerton_message_title }} - {{ reReplaceAll " ?\\{since\\}" "" .CommonAnnotations.kamerton_message_title }}` +
			`{{ else }}{{ with .CommonAnnotations.kamerton_title }} - {{ . }}{{ end }}{{ end }}` +
			`{{ if gt (len .Alerts) 1 }} ({{ len .Alerts }}){{ end }}`,
	}
}

// safeBaseURL returns an http(s) URL without a trailing slash, escaped for use
// inside an HTML attribute of the template, or "" when the URL is unusable.
func safeBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" || strings.Contains(raw, "{{") || strings.Contains(raw, "}}") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return html.EscapeString(raw)
}

func messageBody(w messageWords, base string, htmlMode bool, clock zoneClock) string {
	b := func(s string) string {
		if htmlMode {
			return "<b>" + s + "</b>"
		}
		return s
	}
	i := func(s string) string {
		if htmlMode {
			return "<i>" + s + "</i>"
		}
		return s
	}
	link := func(href, text string) string {
		if htmlMode {
			return `<a href="` + href + `">` + text + `</a>`
		}
		return text + ": " + href
	}
	amp := "&"
	if htmlMode {
		amp = "&amp;" // Telegram requires & outside entities to be escaped, also in attributes
	}

	var t strings.Builder
	// Header: state, severity, system (environment), number of alerts.
	t.WriteString(`{{ if eq .Status "firing" }}{{ if eq .CommonLabels.severity "critical" }}🔴 ` + b(w.critical) +
		`{{ else if eq .CommonLabels.severity "warning" }}🟡 ` + b(w.warning) + `{{ else }}🟠 ` + b(w.problem) + `{{ end }}` +
		`{{ else }}✅ ` + b(w.resolved) + `{{ end }}` +
		` · {{ .CommonLabels.kamerton_system }}{{ with .CommonLabels.environment }} ({{ . }}){{ end }}` +
		`{{ if gt (len .Alerts) 1 }} · {{ len .Alerts }}{{ end }}` + "\n")

	alertLinks := ""
	if base != "" {
		alertLinks = `{{ with $a.Annotations.runbook_url }}` + link(`{{ . }}`, w.runbook) + ` · {{ end }}` +
			link(base+`/alerts?alertname={{ $a.Labels.alertname }}`+amp+`service={{ $a.Labels.service }}`, w.open) + "\n"
	}
	// A text written by the team (kamerton_message_*) replaces the library's
	// words; {since} in it is filled here, the only place the start time is
	// known. $since is the start time in the zone of the description.
	since := func(field string) string {
		return `{{ reReplaceAll "\\{since\\}" $since ` + field + ` }}`
	}
	title := `{{ if $a.Annotations.kamerton_message_title }}` + since(`$a.Annotations.kamerton_message_title`) +
		`{{ else if $a.Annotations.kamerton_title }}{{ $a.Annotations.kamerton_title }}{{ else }}{{ $a.Annotations.summary }}{{ end }}`
	// The team's text and first steps appear only when the message is about
	// one alert; a grouped message stays a compact list (title and value).
	details := `{{ if eq (len $.Alerts) 1 }}` +
		`{{ with $a.Annotations.kamerton_message_text }}` + since(`.`) + "\n{{ end }}" +
		`{{ with $a.Annotations.kamerton_message_action }}👉 ` + since(`.`) + "\n{{ end }}" +
		`{{ end }}`

	// Firing alerts: service, plain-words title, value vs threshold, since.
	t.WriteString(`{{ range $i, $a := .Alerts.Firing }}{{ if lt $i ` + strconv.Itoa(maxListedAlerts) + ` }}` + clock.assign("$since", "$a.StartsAt") + "\n" +
		`{{ if eq $a.Labels.severity "critical" }}🔴{{ else }}🟡{{ end }} ` + b(`{{ $a.Labels.service }}`) + `: ` + title + "\n" +
		`{{ with $a.Annotations.kamerton_value }}` + w.now + ` ` + b(`{{ . }}`) +
		`{{ with $a.Annotations.kamerton_threshold }}, ` + w.threshold + ` {{ . }}{{ end }} · {{ end }}` +
		w.since + ` {{ $since }}` + "\n" +
		details +
		alertLinks +
		`{{ end }}{{ end }}`)
	t.WriteString(`{{ if gt (len .Alerts.Firing) ` + strconv.Itoa(maxListedAlerts) + ` }}` + "\n" +
		i(`… `+w.shown+` `+strconv.Itoa(maxListedAlerts)+` `+w.more+` {{ len .Alerts.Firing }}`) + "\n{{ end }}")

	// Resolved alerts: a compact list, one line each.
	resolvedHeader := `{{ if .Alerts.Firing }}` + "\n" + b(w.alsoResolved) + ":\n{{ end }}"
	t.WriteString(`{{ if .Alerts.Resolved }}` + resolvedHeader +
		`{{ range $i, $a := .Alerts.Resolved }}{{ if lt $i ` + strconv.Itoa(maxListedAlerts) + ` }}` +
		clock.assign("$since", "$a.StartsAt") + clock.assign("$until", "$a.EndsAt") +
		"\n" + `✅ ` + b(`{{ $a.Labels.service }}`) + `: ` + title + "\n" +
		w.since + ` {{ $since }} ` + w.until + ` {{ $until }}` + "\n" +
		`{{ end }}{{ end }}` +
		`{{ if gt (len .Alerts.Resolved) ` + strconv.Itoa(maxListedAlerts) + ` }}` + i(`… `+w.shown+` `+strconv.Itoa(maxListedAlerts)+` `+w.more+` {{ len .Alerts.Resolved }}`) + "\n{{ end }}" +
		`{{ end }}`)
	return t.String()
}
