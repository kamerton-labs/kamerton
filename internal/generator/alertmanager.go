// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"regexp"
	"strings"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// nullReceiver swallows alerts that match no severity route.
const nullReceiver = "kamerton-null"

// alertmanagerConfig references credentials only through Secret key selectors, never copies
// them. Receivers get alerts straight from Alertmanager, so alerts arrive while Kamerton is down.
func (g *gen) alertmanagerConfig() map[string]any {
	n := g.spec.Notifications
	if n == nil || len(n.Receivers) == 0 {
		return nil
	}
	groupBy := []any{"alertname", rules.LabelService}
	repeat := builtinRepeatInterval
	var critical, warning []string
	if r := n.Routing; r != nil {
		if len(r.GroupBy) > 0 {
			groupBy = toAny(r.GroupBy)
		}
		if r.RepeatInterval != "" {
			repeat = string(r.RepeatInterval)
		}
		critical, warning = r.Critical, r.Warning
	}

	var routes []any
	add := func(severity string, receivers []string) {
		for _, name := range receivers {
			routes = append(routes, map[string]any{
				"receiver": name,
				"matchers": []any{matcher(rules.LabelSeverity, severity)},
				"continue": true,
			})
		}
	}
	add(string(v1alpha1.SeverityCritical), critical)
	add(string(v1alpha1.SeverityWarning), warning)
	if r := n.Routing; r != nil {
		for _, rt := range r.Routes {
			matchers := []any{regexMatcher(rules.LabelService, rt.Services)}
			if len(rt.Severities) > 0 {
				sev := make([]string, len(rt.Severities))
				for i, s := range rt.Severities {
					sev[i] = string(s)
				}
				matchers = append([]any{regexMatcher(rules.LabelSeverity, sev)}, matchers...)
			}
			routes = append(routes, map[string]any{"receiver": rt.Receiver, "matchers": matchers, "continue": true})
		}
	}
	webhook := g.webhookReceiver()
	if webhook != nil {
		// Last and with continue, so it does not change delivery by the routes above.
		routes = append(routes, map[string]any{"receiver": webhookReceiver, "continue": true})
	}
	msg := buildMessages(g.language(), g.opts.RunbookBaseURL, newZoneClock(n.TimeZone, g.now()))

	receivers := []any{map[string]any{"name": nullReceiver}}
	for _, r := range n.Receivers {
		recv := map[string]any{"name": r.Name}
		if t := r.Telegram; t != nil && !g.secretMissing(r.Name, "telegram", t.BotTokenSecret) {
			recv["telegramConfigs"] = []any{map[string]any{
				"chatID":       t.ChatID,
				"botToken":     secretRef(t.BotTokenSecret),
				"sendResolved": true,
				"parseMode":    "HTML",
				"message":      msg.TelegramHTML,
			}}
		}
		if e := r.Email; e != nil && (e.AuthPasswordSecret == nil || !g.secretMissing(r.Name, "email", *e.AuthPasswordSecret)) {
			cfg := map[string]any{
				"to":           strings.Join(e.To, ", "),
				"from":         e.From,
				"smarthost":    e.Smarthost,
				"sendResolved": true,
				"html":         msg.EmailHTML,
				"text":         msg.EmailText,
				"headers":      []any{map[string]any{"key": "Subject", "value": msg.EmailSubject}},
			}
			if e.AuthUsername != "" {
				cfg["authUsername"] = e.AuthUsername
			}
			if e.AuthPasswordSecret != nil {
				cfg["authPassword"] = secretRef(*e.AuthPasswordSecret)
			}
			recv["emailConfigs"] = []any{cfg}
		}
		receivers = append(receivers, recv)
	}
	if webhook != nil {
		receivers = append(receivers, webhook)
	}

	out := map[string]any{
		"route": map[string]any{
			"receiver":       nullReceiver,
			"groupBy":        groupBy,
			"groupWait":      "10s",
			"groupInterval":  "2m",
			"repeatInterval": repeat,
			"matchers": []any{
				matcher(rules.LabelManaged, "true"),
				matcher(LabelSystem, g.sd.Name),
			},
			"routes": routes,
		},
		"receivers": receivers,
	}
	if inhibit := g.inhibitRules(); len(inhibit) > 0 {
		out["inhibitRules"] = inhibit
	}
	return out
}

func (g *gen) secretMissing(receiver, channel string, ref v1alpha1.SecretKeyRef) bool {
	if g.opts.SecretMissing == nil || !g.opts.SecretMissing(ref) {
		return false
	}
	g.missing = append(g.missing, MissingSecret{Receiver: receiver, Channel: channel, Secret: ref.Name, Key: ref.Key})
	return true
}

func matcher(name, value string) map[string]any {
	return map[string]any{"name": name, "value": value, "matchType": "="}
}

func regexMatcher(name string, values []string) map[string]any {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = regexp.QuoteMeta(v)
	}
	return map[string]any{"name": name, "value": strings.Join(quoted, "|"), "matchType": "=~"}
}

func secretRef(s v1alpha1.SecretKeyRef) map[string]any {
	return map[string]any{"name": s.Name, "key": s.Key}
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
