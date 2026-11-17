// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package telegram

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

func TestSendFollowUp(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()
	ref := v1alpha1.SecretKeyRef{Name: DefaultSecretName, Key: DefaultSecretKey}
	if err := h.svc.SendFollowUp(ctx, ref, -100, "<b>x</b>", nil); !errors.Is(err, ErrNoToken) {
		t.Errorf("without a token: %v", err)
	}
	_ = h.tokens.Set(ctx, testToken)
	// A receiver with another Secret is not Kamerton's bot: never read.
	if err := h.svc.SendFollowUp(ctx, v1alpha1.SecretKeyRef{Name: "team-bot", Key: "bot-token"}, -100, "x", nil); !errors.Is(err, ErrNotKamertonBot) {
		t.Errorf("foreign Secret: %v", err)
	}
	if err := h.svc.SendFollowUp(ctx, ref, -100, "🔎 <b>Probable cause</b>", nil); err != nil {
		t.Fatal(err)
	}
	calls := h.tg.callsOf("sendMessage")
	if len(calls) != 1 || calls[0].Params["chat_id"].(float64) != -100 || calls[0].Params["parse_mode"] != "HTML" || calls[0].Params["text"] != "🔎 <b>Probable cause</b>" {
		t.Errorf("sendMessage calls: %+v", calls)
	}
}

func TestSendFollowUpWithButtons(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()
	_ = h.tokens.Set(ctx, testToken)
	ref := v1alpha1.SecretKeyRef{Name: DefaultSecretName, Key: DefaultSecretKey}
	kb := Keyboard{{{Text: "Useful", CallbackData: "kn:u-:abcdefghijklmnop"}, {Text: "Noise", CallbackData: "kn:n-:abcdefghijklmnop"}}}
	if err := h.svc.SendFollowUp(ctx, ref, -100, "x", kb); err != nil {
		t.Fatal(err)
	}
	calls := h.tg.callsOf("sendMessage")
	markup, _ := calls[0].Params["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	if len(rows) != 1 || len(rows[0].([]any)) != 2 {
		t.Fatalf("reply_markup = %v", calls[0].Params["reply_markup"])
	}
	if b := rows[0].([]any)[1].(map[string]any); b["text"] != "Noise" || b["callback_data"] != "kn:n-:abcdefghijklmnop" {
		t.Errorf("button = %v", b)
	}
}

type stubCallbacks struct{ chats []int64 }

func (s *stubCallbacks) HandleCallback(_ context.Context, chatID int64, data string) CallbackResult {
	s.chats = append(s.chats, chatID)
	if data == "edit" {
		return CallbackResult{Text: "ok", Keyboard: Keyboard{{{Text: "✓ Noise", CallbackData: "x"}}}}
	}
	return CallbackResult{Text: "no"}
}

func TestCallbackQueries(t *testing.T) {
	h := newHarness(t, baseDescription)
	h.svc.init()
	ctx := context.Background()
	if !slices.Contains(AllowedUpdates, "callback_query") {
		t.Errorf("allowed_updates %v", AllowedUpdates)
	}
	msg := &Message{MessageID: 3, Chat: Chat{ID: -100, Type: "group"}}
	ups := []Update{{UpdateID: 1, CallbackQuery: &CallbackQuery{ID: "a", Message: msg, Data: "edit"}}}
	// Without a handler the press is still answered, so the spinner stops.
	if err := h.svc.process(ctx, testToken, ups, testBotID); err != nil {
		t.Fatal(err)
	}
	if calls := h.tg.callsOf("answerCallbackQuery"); len(calls) != 1 || calls[0].Params["callback_query_id"] != "a" || calls[0].Params["text"] != nil {
		t.Fatalf("answers %+v", calls)
	}
	stub := &stubCallbacks{}
	h.svc.Callbacks = stub
	ups = append(ups, Update{UpdateID: 2, CallbackQuery: &CallbackQuery{ID: "b", Message: msg, Data: "other"}})
	if err := h.svc.process(ctx, testToken, ups, testBotID); err != nil {
		t.Fatal(err)
	}
	if len(stub.chats) != 2 || stub.chats[0] != -100 {
		t.Errorf("handler saw %v", stub.chats)
	}
	edits := h.tg.callsOf("editMessageReplyMarkup")
	if len(edits) != 1 || edits[0].Params["chat_id"].(float64) != -100 || edits[0].Params["message_id"].(float64) != 3 {
		t.Errorf("edits %+v", edits)
	}
	st, _ := h.state.Get(ctx)
	if st.Offset != 3 || len(st.Chats) != 0 {
		t.Errorf("a press neither discovers chats nor stalls the offset: %+v", st)
	}
}

func TestSendTracked(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()
	_ = h.tokens.Set(ctx, testToken)
	if _, err := h.svc.SendTracked(ctx, v1alpha1.SecretKeyRef{Name: "team-bot", Key: "bot-token"}, -100, "x"); !errors.Is(err, ErrNotKamertonBot) {
		t.Errorf("foreign Secret: %v", err)
	}
	h.tg.handle("sendMessage", func(string, map[string]any) (int, string) {
		return 200, `{"ok":true,"result":{"message_id":4242,"chat":{"id":-100,"type":"group"},"date":0}}`
	})
	id, err := h.svc.SendTracked(ctx, v1alpha1.SecretKeyRef{Name: DefaultSecretName, Key: DefaultSecretKey}, -100, "✅ <b>Incident over</b>")
	if err != nil || id != 4242 {
		t.Fatalf("id %d, %v", id, err)
	}
	if c := h.tg.callsOf("sendMessage"); len(c) != 1 || c[0].Params["parse_mode"] != "HTML" || c[0].Params["reply_parameters"] != nil {
		t.Errorf("calls %+v", c)
	}
}

type stubReplies struct {
	got    [][2]int64
	result ReplyResult
}

func (s *stubReplies) HandleReply(_ context.Context, chatID, replyTo int64, _ string) ReplyResult {
	s.got = append(s.got, [2]int64{chatID, replyTo})
	return s.result
}

func TestRepliesInPolling(t *testing.T) {
	h := newHarness(t, baseDescription)
	h.svc.init()
	ctx := context.Background()
	stub := &stubReplies{}
	h.svc.Replies = stub
	chat := Chat{ID: -100, Type: "group", Title: "Ops"}
	ups := []Update{
		{UpdateID: 1, Message: &Message{MessageID: 10, Chat: chat, Text: "not a reply"}},
		{UpdateID: 2, Message: &Message{MessageID: 11, Chat: chat, Text: "a reply", ReplyToMessage: &Message{MessageID: 5, Chat: chat}}},
		{UpdateID: 3, Message: &Message{MessageID: 12, Chat: chat, ReplyToMessage: &Message{MessageID: 5, Chat: chat}}},
	}
	if err := h.svc.process(ctx, testToken, ups, testBotID); err != nil {
		t.Fatal(err)
	}
	if len(stub.got) != 1 || stub.got[0] != [2]int64{-100, 5} {
		t.Errorf("handled %v", stub.got)
	}
	// Not a reply Kamerton waits for: no answer at all.
	if len(h.tg.callsOf("setMessageReaction"))+len(h.tg.callsOf("sendMessage")) != 0 {
		t.Error("an ignored reply was answered")
	}
	st, _ := h.state.Get(ctx)
	if st.Offset != 4 || st.Chats[-100] == nil {
		t.Errorf("replies must not stop chat discovery: %+v", st)
	}
	stub.result = ReplyResult{React: true, Text: "Saved"}
	h.tg.handle("setMessageReaction", func(string, map[string]any) (int, string) { return 200, `{"ok":true,"result":true}` })
	if err := h.svc.process(ctx, testToken, ups[1:2], testBotID); err != nil {
		t.Fatal(err)
	}
	if c := h.tg.callsOf("setMessageReaction"); len(c) != 1 || c[0].Params["message_id"].(float64) != 11 || len(h.tg.callsOf("sendMessage")) != 0 {
		t.Errorf("reaction calls %+v", c)
	}
}
