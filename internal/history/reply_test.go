// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package history

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/telegram"
)

const fakeToken = "123456789:AAFakeTokenForTestsOnly_abcdefghijklmn"

type botCall struct {
	method string
	params map[string]any
}

// fakeBotAPI answers getMe, getUpdates (from a queue), getChat,
// sendMessage and setMessageReaction, and records every call.
type fakeBotAPI struct {
	mu         sync.Mutex
	calls      []botCall
	updates    [][]telegram.Update
	noReaction bool
	srv        *httptest.Server
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	f := &fakeBotAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var params map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &params)
		f.mu.Lock()
		f.calls = append(f.calls, botCall{method, params})
		var result any = true
		switch method {
		case "getMe":
			result = map[string]any{"id": 123456789, "is_bot": true, "first_name": "Kamerton"}
		case "getChat":
			result = map[string]any{"id": params["chat_id"], "type": "group", "title": "Ops"}
		case "getUpdates":
			ups := []telegram.Update{}
			if len(f.updates) > 0 {
				ups, f.updates = f.updates[0], f.updates[1:]
			}
			result = ups
		case "sendMessage":
			result = map[string]any{"message_id": 900 + len(f.calls), "chat": map[string]any{"id": params["chat_id"], "type": "group"}, "date": 0}
		case "setMessageReaction":
			if f.noReaction {
				f.mu.Unlock()
				_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: REACTION_INVALID"}`)
				return
			}
		}
		f.mu.Unlock()
		b, _ := json.Marshal(map[string]any{"ok": true, "result": result})
		if method == "getUpdates" && string(b) == `{"ok":true,"result":[]}` {
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBotAPI) callsOf(method string) []botCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []botCall
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

type stubCallbacks struct {
	mu    sync.Mutex
	chats []int64
}

func (s *stubCallbacks) HandleCallback(_ context.Context, chatID int64, _ string) telegram.CallbackResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chats = append(s.chats, chatID)
	return telegram.CallbackResult{Text: "ok"}
}

func shopDescription(lang string) *v1alpha1.SystemDescription {
	ref := v1alpha1.SecretKeyRef{Name: telegram.DefaultSecretName, Key: telegram.DefaultSecretKey}
	return &v1alpha1.SystemDescription{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "kamerton"},
		Spec: v1alpha1.SystemDescriptionSpec{
			Services: []v1alpha1.Service{{Name: "payment", Type: v1alpha1.ServiceTypeGRPC}},
			Notifications: &v1alpha1.Notifications{
				Language:  lang,
				Receivers: []v1alpha1.Receiver{{Name: "oncall", Telegram: &v1alpha1.TelegramReceiver{ChatID: -1001, BotTokenSecret: ref}}},
				Routing:   &v1alpha1.Routing{Critical: []string{"oncall"}},
			},
		},
	}
}

// replyUpdate is a reply in chat to the bot's message replyTo.
func replyUpdate(id, chat, replyTo int64, text string) telegram.Update {
	c := telegram.Chat{ID: chat, Type: "group", Title: "Ops"}
	return telegram.Update{UpdateID: id, Message: &telegram.Message{
		MessageID: 500 + id, Chat: c, Text: text, ReplyToMessage: &telegram.Message{MessageID: replyTo, Chat: c},
	}}
}

// poll runs the polling loop until the offset reaches want.
func poll(t *testing.T, tg *telegram.Service, state *telegram.MemoryStateStore, want int64) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = tg.Start(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := state.Get(ctx)
		if st.Offset >= want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("polling stopped at offset %d", st.Offset)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestTelegramReplies(t *testing.T) {
	for _, lang := range []string{"", "ru"} {
		t.Run("lang="+lang, func(t *testing.T) {
			ctx := context.Background()
			api := newFakeBotAPI(t)
			descs := &descstore.Memory{}
			descs.Set(shopDescription(lang), nil, "test")
			tokens := &telegram.MemoryTokenStore{}
			_ = tokens.Set(ctx, fakeToken)
			state := &telegram.MemoryStateStore{}
			callbacks := &stubCallbacks{}
			tg := &telegram.Service{
				API: &telegram.Client{BaseURL: api.srv.URL}, Tokens: tokens, State: state, Descriptions: descs,
				PollTimeout: time.Second, Callbacks: callbacks, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			hist := &Service{Descriptions: descs, Receiver: tg.IsReceiver, Now: func() time.Time { return t0 }}
			tg.Replies = hist
			_, _ = hist.store().Update(ctx, func(st *State) error {
				r := closedRecord("0123456789abcdef", "KamertonNoReadyPods", "payment", t0.Add(-time.Hour), 10*time.Minute, nil)
				// -1009 got the message but is no longer a receiver of the system.
				r.Asked = []Asked{{Chat: -1001, Message: 101}, {Chat: -1009, Message: 55}}
				st.Records = append(st.Records, r)
				return nil
			})
			note := func() *Note {
				st, _ := hist.Snapshot(ctx)
				return st.Records[0].Note
			}

			api.mu.Lock()
			api.updates = [][]telegram.Update{{
				replyUpdate(1, -1001, 101, "  rolled back to v1.4.2 "),
				replyUpdate(2, -1009, 55, "from a foreign chat"),
				replyUpdate(3, -1001, 7, "a reply to another message"),
				{UpdateID: 4, Message: &telegram.Message{MessageID: 9, Chat: telegram.Chat{ID: -2000, Type: "group", Title: "New"}, Text: "hello"}},
				{UpdateID: 5, CallbackQuery: &telegram.CallbackQuery{ID: "cb", Message: &telegram.Message{MessageID: 3, Chat: telegram.Chat{ID: -1001}}, Data: "kn:u-:abcdefghijklmnop"}},
			}}
			api.mu.Unlock()
			poll(t, tg, state, 6)

			if n := note(); n == nil || n.Fix != "rolled back to v1.4.2" || n.Source != SourceTelegram {
				t.Fatalf("note %+v", n)
			}
			reactions := api.callsOf("setMessageReaction")
			if len(reactions) != 1 || reactions[0].params["chat_id"].(float64) != -1001 || reactions[0].params["message_id"].(float64) != 501 {
				t.Errorf("reactions %+v", reactions)
			}
			if r, _ := reactions[0].params["reaction"].([]any); len(r) != 1 || r[0].(map[string]any)["emoji"] != "👍" || r[0].(map[string]any)["type"] != "emoji" {
				t.Errorf("reaction %+v", reactions[0].params["reaction"])
			}
			if sent := api.callsOf("sendMessage"); len(sent) != 0 {
				t.Errorf("ignored replies must not be answered: %+v", sent)
			}
			st, _ := state.Get(ctx)
			if st.Chats[-2000] == nil {
				t.Error("chat discovery must still see plain messages")
			}
			if len(callbacks.chats) != 1 || len(api.callsOf("answerCallbackQuery")) != 1 {
				t.Errorf("callbacks %v, answers %d", callbacks.chats, len(api.callsOf("answerCallbackQuery")))
			}
			if allowed, _ := api.callsOf("getUpdates")[0].params["allowed_updates"].([]any); len(allowed) != 4 || allowed[1] != "message" || allowed[3] != "callback_query" {
				t.Errorf("allowed_updates %v", allowed)
			}

			// A later reply replaces the note; the chat does not allow the
			// reaction, so Kamerton answers with a short reply.
			api.mu.Lock()
			api.noReaction = true
			api.updates = [][]telegram.Update{{replyUpdate(6, -1001, 101, "raised the memory limit")}}
			api.mu.Unlock()
			poll(t, tg, state, 7)
			if n := note(); n == nil || n.Fix != "raised the memory limit" {
				t.Fatalf("replaced note %+v", n)
			}
			saved := "Saved: what helped."
			if lang == "ru" {
				saved = "Сохранено: что помогло."
			}
			sent := api.callsOf("sendMessage")
			if len(sent) != 1 || sent[0].params["text"] != saved {
				t.Fatalf("confirmation %+v", sent)
			}
			if rp, _ := sent[0].params["reply_parameters"].(map[string]any); rp["message_id"].(float64) != 506 {
				t.Errorf("reply_parameters %+v", sent[0].params["reply_parameters"])
			}

			// Too long: nothing is saved and the person is told why.
			api.mu.Lock()
			api.updates = [][]telegram.Update{{replyUpdate(7, -1001, 101, strings.Repeat("x", MaxFix+1))}}
			api.mu.Unlock()
			poll(t, tg, state, 8)
			if n := note(); n.Fix != "raised the memory limit" {
				t.Errorf("a too long reply changed the note: %+v", n)
			}
			sent = api.callsOf("sendMessage")
			if len(sent) != 2 || !strings.Contains(sent[1].params["text"].(string), "500") {
				t.Errorf("too long answer %+v", sent)
			}
		})
	}
}
