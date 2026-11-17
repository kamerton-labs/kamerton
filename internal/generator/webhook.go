// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

// webhookReceiver only triggers follow-ups; the notification goes straight from Alertmanager.
const webhookReceiver = "kamerton-webhook"

type WebhookOptions struct {
	// URL of Kamerton's webhook endpoint as Alertmanager reaches it, for
	// example http://kamerton.kamerton.svc:8080/api/v1/hooks/alertmanager.
	URL    string
	Secret v1alpha1.SecretKeyRef
	// Namespace is Kamerton's namespace (empty: any). The webhook is added only to
	// descriptions in it: an AlertmanagerConfig can reference Secrets only in its own
	// namespace, and the operator drops one whose Secret it cannot read.
	Namespace string
}

func (g *gen) webhookReceiver() map[string]any {
	w := g.opts.Webhook
	if w == nil || w.URL == "" || w.Secret.Name == "" || !g.spec.Notifications.SendsEvidence() {
		return nil
	}
	if w.Namespace != "" && g.sd.Namespace != w.Namespace {
		return nil
	}
	return map[string]any{
		"name": webhookReceiver,
		"webhookConfigs": []any{map[string]any{
			"url":          w.URL,
			"sendResolved": true,
			"httpConfig": map[string]any{
				"authorization": map[string]any{
					"type":        "Bearer",
					"credentials": secretRef(w.Secret),
				},
			},
		}},
	}
}
