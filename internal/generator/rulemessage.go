// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"strconv"
	"strings"

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
