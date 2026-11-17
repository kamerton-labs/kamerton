// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

func TestSendFollowUp(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()
	ref := v1alpha1.SecretKeyRef{Name: DefaultSecretName, Key: DefaultSecretKey}
	if err := h.svc.SendFollowUp(ctx, ref, -100, "<b>x</b>"); !errors.Is(err, ErrNoToken) {
		t.Errorf("without a token: %v", err)
	}
	_ = h.tokens.Set(ctx, testToken)
	// A receiver with another Secret is not Kamerton's bot: never read.
	if err := h.svc.SendFollowUp(ctx, v1alpha1.SecretKeyRef{Name: "team-bot", Key: "bot-token"}, -100, "x"); !errors.Is(err, ErrNotKamertonBot) {
		t.Errorf("foreign Secret: %v", err)
	}
	if err := h.svc.SendFollowUp(ctx, ref, -100, "🔎 <b>Probable cause</b>"); err != nil {
		t.Fatal(err)
	}
	calls := h.tg.callsOf("sendMessage")
	if len(calls) != 1 || calls[0].Params["chat_id"].(float64) != -100 || calls[0].Params["parse_mode"] != "HTML" || calls[0].Params["text"] != "🔎 <b>Probable cause</b>" {
		t.Errorf("sendMessage calls: %+v", calls)
	}
}
