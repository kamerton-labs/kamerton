// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// Annotations holding the team's notification text. The templates prefer them
// to the library's plain-words annotations.
const (
	AnnotationMessageTitle  = "kamerton_message_title"
	AnnotationMessageText   = "kamerton_message_text"
	AnnotationMessageAction = "kamerton_message_action"
	// sincePlaceholder stays in the annotation: the start time is known only
	// to Alertmanager, whose template replaces it.
	sincePlaceholder = "{since}"
)

func operationRef(exact, label string, perOperation bool) string {
	switch {
	case exact != "":
		return "{{ " + strconv.Quote(exact) + " }}"
	case perOperation:
		return `{{ index $labels ` + strconv.Quote(label) + ` }}`
	}
	return ""
}

// withMessage relies on description.CheckMessageText: the only braces in the text are placeholders.
func (g *gen) withMessage(r rulefmt.Rule, svc *v1alpha1.Service, m *v1alpha1.NotificationMessage, operation string) rulefmt.Rule {
	if m == nil {
		return r
	}
	rep := strings.NewReplacer(
		"{value}", r.Annotations[rules.AnnotationValue],
		"{threshold}", r.Annotations[rules.AnnotationThreshold],
		"{service}", svc.Name,
		"{operation}", operation,
		"{system}", g.sd.Name,
		sincePlaceholder, sincePlaceholder,
	)
	if r.Annotations == nil {
		r.Annotations = map[string]string{}
	}
	for key, text := range map[string]string{AnnotationMessageTitle: m.Title, AnnotationMessageText: m.Text, AnnotationMessageAction: m.Action} {
		if t := strings.TrimSpace(text); t != "" {
			r.Annotations[key] = rep.Replace(t)
		}
	}
	return r
}

func lastMessage(ms ...*v1alpha1.NotificationMessage) *v1alpha1.NotificationMessage {
	var out *v1alpha1.NotificationMessage
	for _, m := range ms {
		if m != nil {
			out = m
		}
	}
	return out
}

type defaultMessage struct{ text, action string }

// Kamerton's own text for the "no data" alerts: the value alone cannot say that
// silence of the other alerts means nothing. {after} is the window.
var (
	systemNoDataMessage = map[string]defaultMessage{
		rules.LanguageEnglish: {
			text: "Kamerton cannot see this system: none of its services has sent span metrics for {after}. " +
				"Until data returns no other alert of this system can fire, so silence does not mean that all is well.",
			action: "Check the OpenTelemetry Collector (logs, exporter errors), its remote write to Prometheus " +
				"(rejected writes, out of order sample), Prometheus itself and the clocks of the nodes.",
		},
		rules.LanguageRussian: {
			text: "Kamerton не видит эту систему: ни один её сервис не присылает span-метрики дольше {after}. " +
				"Пока данные не вернутся, другие оповещения этой системы сработать не могут, и тишина не значит, что всё в порядке.",
			action: "Проверьте OpenTelemetry Collector (логи, ошибки экспортёра), его remote write в Prometheus " +
				"(отклонённые записи, out of order sample), сам Prometheus и часы на узлах.",
		},
	}
	serviceNoDataMessage = map[string]defaultMessage{
		rules.LanguageEnglish: {
			text: "Kamerton cannot see this service: it sent span metrics in the last day but none for {after}, " +
				"while other services still do. Its other alerts cannot fire until data returns.",
			action: "Check that its pods run, the OpenTelemetry exporter of the service (endpoint, errors in its log) and the Collector it sends to.",
		},
		rules.LanguageRussian: {
			text: "Kamerton не видит этот сервис: за последние сутки он присылал span-метрики, но уже {after} не присылает, " +
				"хотя другие сервисы присылают. Другие его оповещения не сработают, пока данные не вернутся.",
			action: "Проверьте, что его поды работают, экспортёр OpenTelemetry в сервисе (адрес, ошибки в логе) и Collector, в который он отправляет данные.",
		},
	}
)

// withDefaultMessage sets Kamerton's text and first steps; withMessage applied
// after it replaces each part the team wrote.
func (g *gen) withDefaultMessage(r rulefmt.Rule, texts map[string]defaultMessage, after time.Duration) rulefmt.Rule {
	m, ok := texts[g.language()]
	if !ok {
		m = texts[rules.LanguageEnglish]
	}
	if r.Annotations == nil {
		r.Annotations = map[string]string{}
	}
	d := rules.FormatDuration(after)
	r.Annotations[AnnotationMessageText] = strings.ReplaceAll(m.text, "{after}", d)
	r.Annotations[AnnotationMessageAction] = strings.ReplaceAll(m.action, "{after}", d)
	return r
}
