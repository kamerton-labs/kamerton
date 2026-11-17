// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

// webhookReceiver is the receiver that notifies Kamerton about firing and
// resolved groups, so it can follow up with evidence. The base notification
// still goes from Alertmanager straight to Telegram and email.
const webhookReceiver = "kamerton-webhook"

// WebhookOptions wire the follow-up webhook into generated Alertmanager
// routing.
type WebhookOptions struct {
	// URL of Kamerton's webhook endpoint as Alertmanager reaches it, for
	// example http://kamerton.kamerton.svc:8080/api/v1/hooks/alertmanager.
	URL string
	// Secret holds the bearer token Alertmanager sends. It lives in
	// Namespace, Kamerton's namespace.
	Secret v1alpha1.SecretKeyRef
	// Namespace is Kamerton's namespace. An AlertmanagerConfig can reference
	// Secrets only in its own namespace, and the Prometheus Operator drops a
	// whole AlertmanagerConfig whose Secret it cannot read, so the webhook is
	// added only to descriptions in this namespace (empty: any).
	Namespace string
}

// webhookReceiver returns the webhook receiver of the description, or nil
// when the webhook is not wired (no options, follow-up turned off, or the
// description lives outside Kamerton's namespace).
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
