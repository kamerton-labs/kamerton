// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/telegram"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// A syntactically valid token that exists only in these tests.
const testToken = "123456789:AAFakeTokenForTestsOnly_abcdefghijklmn"

type botCall struct {
	method string
	params map[string]any
}

type fakeBot struct {
	mu      sync.Mutex
	calls   []botCall
	updates [][]telegram.Update
}

func (b *fakeBot) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var params map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &params)
		b.mu.Lock()
		b.calls = append(b.calls, botCall{method, params})
		var result any = true
		switch method {
		case "getMe":
			result = map[string]any{"id": 123456789, "is_bot": true, "first_name": "Kamerton"}
		case "getChat":
			result = map[string]any{"id": params["chat_id"], "type": "group", "title": "Ops"}
		case "getChatMember":
			result = map[string]any{"status": "member", "user": map[string]any{"id": 123456789, "is_bot": true, "first_name": "Kamerton"}}
		case "getUpdates":
			ups := []telegram.Update{}
			if len(b.updates) > 0 {
				ups, b.updates = b.updates[0], b.updates[1:]
			}
			result = ups
		}
		b.mu.Unlock()
		if method == "getUpdates" {
			time.Sleep(10 * time.Millisecond)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (b *fakeBot) callsOf(method string) []botCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []botCall
	for _, c := range b.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

const receiverYAML = `  notifications:
    language: LANG
    receivers:
      - name: ops
        telegram:
          chatID: -100
          botTokenSecret: {name: kamerton-telegram, key: bot-token}
    routing:
      critical: [ops]
`

func press(id string, chat int64, data string) telegram.Update {
	return telegram.Update{CallbackQuery: &telegram.CallbackQuery{ID: id, Data: data,
		Message: &telegram.Message{MessageID: 7, Chat: telegram.Chat{ID: chat, Type: "group"}}}}
}

func setup(t *testing.T, lang string) (*Feedback, *telegram.Service, *fakeBot, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shop.yaml")
	if err := os.WriteFile(path, []byte(shopYAML+strings.Replace(receiverYAML, "LANG", lang, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &descstore.File{Paths: []string{path}}
	bot := &fakeBot{}
	srv := bot.serve(t)
	tokens := &telegram.MemoryTokenStore{}
	_ = tokens.Set(context.Background(), testToken)
	tg := &telegram.Service{
		API: &telegram.Client{BaseURL: srv.URL}, Tokens: tokens, State: &telegram.MemoryStateStore{},
		Descriptions: store, PollTimeout: time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	f := &Feedback{Descriptions: store, Receiver: tg.IsReceiver}
	tg.Callbacks = f
	snap, err := descstore.Default(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	kb := f.Buttons(context.Background(), snap.SD, []triage.Alert{{Name: "KamertonErrorRateHigh", Service: "checkout"}})
	_, _, ref, ok := parseCallback(kb[0][1].CallbackData)
	if !ok {
		t.Fatalf("keyboard %+v", kb)
	}
	return f, tg, bot, ref
}

func run(t *testing.T, tg *telegram.Service, bot *fakeBot, answers int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = tg.Start(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(bot.callsOf("answerCallbackQuery")) < answers {
		if time.Now().After(deadline) {
			t.Fatalf("answers = %d, want %d", len(bot.callsOf("answerCallbackQuery")), answers)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestTelegramButtonPresses(t *testing.T) {
	f, tg, bot, ref := setup(t, "en")
	bot.updates = [][]telegram.Update{{
		press("q1", -100, "kn:n-:"+ref),
		press("q2", -100, "kn:nn:"+ref),
		press("q3", -555, "kn:n-:"+ref),
		press("q4", -100, "kn:zz:"+ref),
		{UpdateID: 5, CallbackQuery: &telegram.CallbackQuery{ID: "q5", Data: "kn:n-:" + ref}},
	}}
	run(t, tg, bot, 5)

	answers := map[string]string{}
	for _, c := range bot.callsOf("answerCallbackQuery") {
		text, _ := c.params["text"].(string)
		answers[c.params["callback_query_id"].(string)] = text
	}
	want := map[string]string{
		"q1": "Thanks, marked as noise",
		"q2": "Already marked",
		"q3": "This chat does not get the alerts of this system",
		"q4": "Unknown button",
		"q5": "",
	}
	for id, w := range want {
		if answers[id] != w {
			t.Errorf("answer to %s = %q, want %q", id, answers[id], w)
		}
	}
	edits := bot.callsOf("editMessageReplyMarkup")
	if len(edits) != 1 {
		t.Fatalf("edits = %+v", edits)
	}
	raw, _ := json.Marshal(edits[0].params)
	if !strings.Contains(string(raw), `"✓ Noise"`) || !strings.Contains(string(raw), `"kn:un:`+ref+`"`) || edits[0].params["message_id"].(float64) != 7 {
		t.Errorf("edit = %s", raw)
	}
	got, _ := f.Counts(context.Background(), "shop")
	if got[errorRateKey] != (Counts{Noise: 1}) {
		t.Errorf("counts after presses: %+v", got)
	}
	for _, c := range bot.calls {
		raw, _ := json.Marshal(c.params)
		if strings.Contains(string(raw), "AAFakeTokenForTestsOnly") {
			t.Errorf("%s carries the token", c.method)
		}
	}

	bot.updates = [][]telegram.Update{{press("q6", -100, "kn:un:"+ref)}}
	run(t, tg, bot, 6)
	if got, _ := f.Counts(context.Background(), "shop"); got[errorRateKey] != (Counts{Useful: 1}) {
		t.Errorf("a changed verdict moves the vote: %+v", got)
	}
}

func TestTelegramAnswersInRussian(t *testing.T) {
	f, _, _, ref := setup(t, "ru")
	res := f.HandleCallback(context.Background(), -100, "kn:u-:"+ref)
	if res.Text != "Спасибо, отмечено: по делу" || res.Keyboard[0][0].Text != "✓ По делу" || res.Keyboard[0][1].Text != "Шум" {
		t.Errorf("result %+v", res)
	}
	if res := f.HandleCallback(context.Background(), -555, "kn:n-:"+ref); res.Text != "Этот чат не получает оповещения этой системы" || res.Keyboard != nil {
		t.Errorf("foreign chat: %+v", res)
	}
	if res := f.HandleCallback(context.Background(), -100, "kn:n-:aaaaaaaaaaaaaaaa"); res.Text != "These buttons have expired" {
		t.Errorf("unknown reference: %+v", res)
	}
}
