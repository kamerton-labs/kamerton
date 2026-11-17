// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/generator"
)

// A syntactically valid token that exists only in these tests.
const (
	testToken = "123456789:AAFakeTokenForTestsOnly_abcdefghijklmn"
	testBotID = 123456789
	otherTok  = "987654321:BBOtherFakeTokenForTests_abcdefghijklm"
)

type fakeTelegram struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	calls    []fakeCall
	handlers map[string]func(token string, params map[string]any) (int, string)
	updates  [][]Update // returned by successive getUpdates calls
}

type fakeCall struct {
	Method string
	Token  string
	Params map[string]any
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{t: t, handlers: map[string]func(string, map[string]any) (int, string){}}
	f.handlers["getMe"] = func(token string, _ map[string]any) (int, string) {
		if token != testToken && token != otherTok {
			return 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`
		}
		id, _ := BotIDFromToken(token)
		b, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"id": id, "is_bot": true, "first_name": "Kamerton Alerts", "username": "kamerton_test_bot"}})
		return 200, string(b)
	}
	f.handlers["getUpdates"] = func(string, map[string]any) (int, string) {
		f.mu.Lock()
		var ups []Update
		if len(f.updates) > 0 {
			ups, f.updates = f.updates[0], f.updates[1:]
		}
		f.mu.Unlock()
		if ups == nil {
			time.Sleep(20 * time.Millisecond)
			ups = []Update{}
		}
		b, _ := json.Marshal(map[string]any{"ok": true, "result": ups})
		return 200, string(b)
	}
	f.handlers["sendMessage"] = func(string, map[string]any) (int, string) {
		return 200, `{"ok":true,"result":{"message_id":1,"chat":{"id":1,"type":"group"},"date":0}}`
	}
	f.handlers["getChatMember"] = func(string, map[string]any) (int, string) {
		return 200, `{"ok":true,"result":{"status":"member","user":{"id":123456789,"is_bot":true,"first_name":"x"}}}`
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "bot") {
			http.NotFound(w, r)
			return
		}
		token, method := strings.TrimPrefix(parts[0], "bot"), parts[1]
		var params map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &params)
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{Method: method, Token: token, Params: params})
		h := f.handlers[method]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
			return
		}
		code, resp := h(token, params)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) handle(method string, h func(token string, params map[string]any) (int, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = h
}

func (f *fakeTelegram) callsOf(method string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

const baseDescription = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
spec:
  services:
    - name: checkout
      type: grpc
      critical: true
    - name: payment
      type: grpc
`

type harness struct {
	svc    *Service
	tg     *fakeTelegram
	tokens *MemoryTokenStore
	state  *MemoryStateStore
	file   string
	logs   *bytes.Buffer
}

func newHarness(t *testing.T, desc string) *harness {
	t.Helper()
	tg := newFakeTelegram(t)
	path := filepath.Join(t.TempDir(), "system.yaml")
	if err := os.WriteFile(path, []byte(desc), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	h := &harness{tg: tg, tokens: &MemoryTokenStore{}, state: &MemoryStateStore{}, file: path, logs: logs}
	h.svc = &Service{
		API:          &Client{BaseURL: tg.srv.URL},
		Tokens:       h.tokens,
		State:        h.state,
		Descriptions: &descstore.File{Paths: []string{path}},
		Identity:     "test-pod",
		PollTimeout:  time.Second,
		Logger:       slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return h
}

func (h *harness) description(t *testing.T) *v1alpha1.SystemDescription {
	t.Helper()
	snap, err := descstore.Default(context.Background(), h.svc.Descriptions)
	if err != nil {
		t.Fatal(err)
	}
	return snap.SD
}

func assertNoToken(t *testing.T, what, s string) {
	t.Helper()
	_, secret, _ := strings.Cut(testToken, ":")
	if strings.Contains(s, testToken) || strings.Contains(s, secret) {
		t.Errorf("%s contains the bot token:\n%s", what, s)
	}
}

func TestSetTokenValidatesWithGetMe(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()

	bot, err := h.svc.SetToken(ctx, "  "+testToken+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if bot.Username != "kamerton_test_bot" || bot.ID != testBotID {
		t.Errorf("bot = %+v", bot)
	}
	if got, _ := h.tokens.Get(ctx); got != testToken {
		t.Error("token was not stored (trimmed)")
	}
	st, _ := h.state.Get(ctx)
	if st.Bot == nil || st.Bot.Username != "kamerton_test_bot" {
		t.Errorf("bot not recorded in state: %+v", st.Bot)
	}
	raw, _ := json.Marshal(st)
	assertNoToken(t, "state", string(raw))
	assertNoToken(t, "logs", h.logs.String())
}

func TestSetTokenRejectsBadTokens(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()

	if _, err := h.svc.SetToken(ctx, "not a token"); !errors.Is(err, ErrMalformedToken) {
		t.Errorf("malformed: err = %v", err)
	}
	if n := len(h.tg.callsOf("getMe")); n != 0 {
		t.Errorf("a malformed token must not be sent to Telegram (%d calls)", n)
	}
	wrong := "111111111:AAWrongTokenNotKnownToTheFake_abcdefgh"
	_, err := h.svc.SetToken(ctx, wrong)
	if !IsUnauthorized(err) {
		t.Fatalf("unknown token: err = %v", err)
	}
	if strings.Contains(err.Error(), wrong) || strings.Contains(describeErr(err), wrong) {
		t.Error("error message contains the token")
	}
	if got, _ := h.tokens.Get(ctx); got != "" {
		t.Error("a rejected token must not be stored")
	}
}

func TestReplacingTheBotResetsChats(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()
	if _, err := h.svc.SetToken(ctx, testToken); err != nil {
		t.Fatal(err)
	}
	_, _ = h.state.Update(ctx, func(st *State) error {
		st.Chats[-1] = &KnownChat{ID: -1, Title: "old", Available: true}
		st.Offset = 42
		return nil
	})
	if _, err := h.svc.SetToken(ctx, otherTok); err != nil {
		t.Fatal(err)
	}
	st, _ := h.state.Get(ctx)
	if len(st.Chats) != 0 || st.Offset != 0 || st.Bot.ID != 987654321 {
		t.Errorf("state not reset for a new bot: %+v", st)
	}
}

func TestClientErrorsNeverContainTheToken(t *testing.T) {
	// A server that is already closed: the transport error includes the URL.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Client{BaseURL: url}
	_, err := c.GetMe(context.Background(), testToken)
	if err == nil {
		t.Fatal("expected an error")
	}
	assertNoToken(t, "transport error", err.Error())

	// A Telegram error description that echoes the token is scrubbed too.
	tg := newFakeTelegram(t)
	tg.handle("getMe", func(string, map[string]any) (int, string) {
		return 400, `{"ok":false,"error_code":400,"description":"bad request for ` + testToken + `"}`
	})
	_, err = (&Client{BaseURL: tg.srv.URL}).GetMe(context.Background(), testToken)
	assertNoToken(t, "api error", err.Error())
}

func groupChat(id int64, title string) Chat { return Chat{ID: id, Type: "group", Title: title} }

func member(status string) ChatMember {
	return ChatMember{Status: status, User: User{ID: testBotID, IsBot: true}}
}

func TestApplyUpdateAddedRemovedMigrated(t *testing.T) {
	st := &State{}
	now := time.Now()

	ApplyUpdate(st, Update{UpdateID: 1, MyChatMember: &ChatMemberUpdated{Chat: groupChat(-100, "Ops"), OldChatMember: member("left"), NewChatMember: member("member")}}, testBotID, now)
	if c := st.Chats[-100]; c == nil || !c.Available || c.Title != "Ops" || c.Type != "group" {
		t.Fatalf("added: %+v", c)
	}
	ApplyUpdate(st, Update{UpdateID: 2, MyChatMember: &ChatMemberUpdated{Chat: groupChat(-100, "Ops"), NewChatMember: member("administrator")}}, testBotID, now)
	if !st.Chats[-100].Available {
		t.Fatal("administrator must be available")
	}
	ApplyUpdate(st, Update{UpdateID: 3, MyChatMember: &ChatMemberUpdated{Chat: groupChat(-200, "Dev"), NewChatMember: member("member")}}, testBotID, now)
	ApplyUpdate(st, Update{UpdateID: 4, MyChatMember: &ChatMemberUpdated{Chat: groupChat(-200, "Dev"), NewChatMember: member("left")}}, testBotID, now)
	if c := st.Chats[-200]; c.Available || c.Problem == "" {
		t.Fatalf("removed chat must be unavailable with a reason: %+v", c)
	}
	ApplyUpdate(st, Update{UpdateID: 5, MyChatMember: &ChatMemberUpdated{Chat: groupChat(-200, "Dev"), NewChatMember: member("kicked")}}, testBotID, now)
	if st.Chats[-200].Available || st.Chats[-200].Status != "kicked" {
		t.Fatalf("kicked: %+v", st.Chats[-200])
	}
	// Restricted but still a member counts as present.
	yes := true
	ApplyUpdate(st, Update{UpdateID: 6, MyChatMember: &ChatMemberUpdated{Chat: Chat{ID: -300, Type: "supergroup", Title: "R"}, NewChatMember: ChatMember{Status: "restricted", IsMember: &yes, User: User{ID: testBotID}}}}, testBotID, now)
	if !st.Chats[-300].Available {
		t.Fatal("restricted member must be available")
	}
	// Migration: the old group reports migrate_to_chat_id, the supergroup migrate_from_chat_id.
	// Both are idempotent.
	migs := ApplyUpdate(st, Update{UpdateID: 7, Message: &Message{Chat: groupChat(-100, "Ops"), MigrateToChatID: -1001000}}, testBotID, now)
	migs = append(migs, ApplyUpdate(st, Update{UpdateID: 8, Message: &Message{Chat: Chat{ID: -1001000, Type: "supergroup", Title: "Ops"}, MigrateFromChatID: -100}}, testBotID, now)...)
	if _, still := st.Chats[-100]; still {
		t.Error("the old group ID must be gone")
	}
	c := st.Chats[-1001000]
	if c == nil || c.Type != "supergroup" || c.MigratedFrom != -100 || c.Title != "Ops" || !c.Available {
		t.Fatalf("migrated: %+v", c)
	}
	if len(migs) != 2 || migs[0] != (Migration{From: -100, To: -1001000}) {
		t.Errorf("migrations = %+v", migs)
	}
	// A late message from the old group must not resurrect it.
	ApplyUpdate(st, Update{UpdateID: 9, Message: &Message{Chat: groupChat(-100, "Ops")}}, testBotID, now)
	if _, back := st.Chats[-100]; back {
		t.Error("the old group came back")
	}
	ApplyUpdate(st, Update{UpdateID: 10, Message: &Message{Chat: Chat{ID: -1001000, Type: "supergroup", Title: "Ops 24/7"}, NewChatTitle: "Ops 24/7"}}, testBotID, now)
	if st.Chats[-1001000].Title != "Ops 24/7" {
		t.Error("title not updated")
	}
	ApplyUpdate(st, Update{UpdateID: 11, Message: &Message{Chat: Chat{ID: 555, Type: "private", FirstName: "Анна"}}}, testBotID, now)
	if c := st.Chats[555]; c == nil || c.Title != "Анна" || !c.Available {
		t.Errorf("private chat: %+v", c)
	}
}

func TestPollingDiscoversChatsAndFollowsMigration(t *testing.T) {
	desc := baseDescription + `  notifications:
    receivers:
      - name: oncall
        telegram:
          chatID: -100
          botTokenSecret: {name: kamerton-telegram, key: bot-token}
    routing:
      critical: [oncall]
`
	h := newHarness(t, desc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.tokens.Set(ctx, testToken); err != nil {
		t.Fatal(err)
	}
	h.tg.handle("getChat", func(_ string, p map[string]any) (int, string) {
		return 200, `{"ok":true,"result":{"id":-100,"type":"group","title":"Ops"}}`
	})
	h.tg.mu.Lock()
	h.tg.updates = [][]Update{
		{
			{UpdateID: 10, MyChatMember: &ChatMemberUpdated{Chat: groupChat(-200, "Dev"), NewChatMember: member("member")}},
			{UpdateID: 11, Message: &Message{Chat: groupChat(-100, "Ops"), MigrateToChatID: -1001000}},
		},
		{
			{UpdateID: 12, MyChatMember: &ChatMemberUpdated{Chat: groupChat(-200, "Dev"), NewChatMember: member("left")}},
		},
	}
	h.tg.mu.Unlock()

	done := make(chan struct{})
	go func() { _ = h.svc.Start(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := h.state.Get(ctx)
		if st.Offset == 13 && st.Chats[-200] != nil && !st.Chats[-200].Available {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("polling did not process updates: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	st, _ := h.state.Get(context.Background())
	if c := st.Chats[-1001000]; c == nil || c.MigratedFrom != -100 || c.Title != "Ops" {
		t.Errorf("migrated chat: %+v", c)
	}
	sd := h.description(t)
	if got := sd.Spec.Notifications.Receivers[0].Telegram.ChatID; got != -1001000 {
		t.Errorf("description receiver still points at %d after migration", got)
	}
	if sd.Spec.Notifications.Receivers[0].Name != "oncall" {
		t.Error("receiver name must be kept")
	}
	// allowed_updates is requested explicitly (getUpdates remembers the last setting).
	calls := h.tg.callsOf("getUpdates")
	if len(calls) == 0 {
		t.Fatal("no getUpdates calls")
	}
	allowed, _ := calls[0].Params["allowed_updates"].([]any)
	if len(allowed) == 0 || allowed[0] != "my_chat_member" {
		t.Errorf("allowed_updates = %v", calls[0].Params["allowed_updates"])
	}
	if off, _ := calls[1].Params["offset"].(float64); off != 12 {
		t.Errorf("second getUpdates offset = %v, want 12", calls[1].Params["offset"])
	}
	assertNoToken(t, "logs", h.logs.String())
}

func TestPollingReportsConflict(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = h.tokens.Set(ctx, testToken)
	h.tg.handle("getUpdates", func(string, map[string]any) (int, string) {
		return 409, `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"}`
	})
	go func() { _ = h.svc.Start(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, _ := h.state.Get(ctx)
		if st.Poll.Conflict {
			if !strings.Contains(st.Poll.LastError, "only one update consumer") {
				t.Errorf("conflict message: %q", st.Poll.LastError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("conflict not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !h.svc.NeedLeaderElection() {
		t.Error("the poller must run only on the leader")
	}
}

func TestAddChatByIDFollowsMigration(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()
	_ = h.tokens.Set(ctx, testToken)
	h.tg.handle("getChat", func(_ string, p map[string]any) (int, string) {
		if p["chat_id"].(float64) == -100 {
			return 400, `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1009}}`
		}
		return 200, `{"ok":true,"result":{"id":-1009,"type":"supergroup","title":"Ops"}}`
	})
	kc, err := h.svc.AddChat(ctx, -100)
	if err != nil {
		t.Fatal(err)
	}
	if kc.ID != -1009 || kc.Type != "supergroup" || !kc.Available {
		t.Errorf("chat = %+v", kc)
	}
}

func TestSendTest(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()
	_ = h.tokens.Set(ctx, testToken)

	res, err := h.svc.SendTest(ctx, -100)
	if err != nil || !res.OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	calls := h.tg.callsOf("sendMessage")
	if len(calls) != 1 || calls[0].Params["chat_id"].(float64) != -100 || calls[0].Params["parse_mode"] != "HTML" {
		t.Fatalf("sendMessage calls: %+v", calls)
	}
	if text := calls[0].Params["text"].(string); !strings.Contains(text, "<b>shop</b>") || !strings.Contains(text, "test message") {
		t.Errorf("text = %q", text)
	}

	h.tg.handle("sendMessage", func(string, map[string]any) (int, string) {
		return 403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked from the group chat"}`
	})
	_, _ = h.state.Update(ctx, func(st *State) error {
		st.Chats[-100] = &KnownChat{ID: -100, Available: true}
		return nil
	})
	res, err = h.svc.SendTest(ctx, -100)
	if err != nil || res.OK || !strings.Contains(res.Error, "bot was kicked") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	st, _ := h.state.Get(ctx)
	if st.Chats[-100].Available {
		t.Error("a chat the bot was kicked from must become unavailable")
	}

	_ = h.tokens.Delete(ctx)
	if _, err := h.svc.SendTest(ctx, -100); !errors.Is(err, ErrNoToken) {
		t.Errorf("without token: %v", err)
	}
}

func TestSendTestLanguage(t *testing.T) {
	h := newHarness(t, strings.Replace(baseDescription, "spec:\n", "spec:\n  notifications:\n    language: ru\n", 1))
	ctx := context.Background()
	_ = h.tokens.Set(ctx, testToken)
	if res, err := h.svc.SendTest(ctx, -100); err != nil || !res.OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	calls := h.tg.callsOf("sendMessage")
	if len(calls) != 1 || !strings.Contains(calls[0].Params["text"].(string), "тестовое сообщение") {
		t.Errorf("sendMessage calls: %+v", calls)
	}
}

func TestSaveRoutingWritesDescriptionAndCleansUp(t *testing.T) {
	h := newHarness(t, baseDescription)
	ctx := context.Background()
	if _, err := h.svc.SetToken(ctx, testToken); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.SaveRouting(ctx, []RoutingInput{
		{ChatID: -1001, Severities: []string{"critical", "warning"}},
		{ChatID: -1002, Severities: []string{"critical"}, Services: []string{"payment"}},
		{ChatID: -1003, Severities: nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	sd := h.description(t)
	n := sd.Spec.Notifications
	if len(n.Receivers) != 2 {
		t.Fatalf("receivers: %+v", n.Receivers)
	}
	if n.Receivers[0].Name != "telegram-n1001" || n.Receivers[0].Telegram.BotTokenSecret.Name != DefaultSecretName {
		t.Errorf("receiver: %+v", n.Receivers[0])
	}
	if strings.Join(n.Routing.Critical, ",") != "telegram-n1001" || strings.Join(n.Routing.Warning, ",") != "telegram-n1001" {
		t.Errorf("routing lists: %+v", n.Routing)
	}
	if len(n.Routing.Routes) != 1 || n.Routing.Routes[0].Receiver != "telegram-n1002" || n.Routing.Routes[0].Services[0] != "payment" {
		t.Errorf("routes: %+v", n.Routing.Routes)
	}
	raw, _ := os.ReadFile(h.file)
	assertNoToken(t, "description file", string(raw))

	b, err := generator.Generate(sd, generator.Options{})
	if err != nil {
		t.Fatal(err)
	}
	amc, _ := json.Marshal(b.AlertmanagerConfig)
	for _, want := range []string{`"receiver":"telegram-n1002"`, `"value":"payment"`, `"matchType":"=~"`, `"chatID":-1001`} {
		if !strings.Contains(string(amc), want) {
			t.Errorf("AlertmanagerConfig lacks %s:\n%s", want, amc)
		}
	}
	assertNoToken(t, "AlertmanagerConfig", string(amc))

	// The token is not in the status.
	st, err := h.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(st)
	assertNoToken(t, "status", string(js))
	if !st.Token.Configured || st.Token.Bot == nil || st.Token.Bot.Username != "kamerton_test_bot" {
		t.Errorf("token status: %+v", st.Token)
	}
	var routed int
	for _, c := range st.Chats {
		if c.ID == -1002 && strings.Join(c.Routing.Services, ",") == "payment" && strings.Join(c.Routing.Severities, ",") == "critical" {
			routed++
		}
		if c.ID == -1001 && strings.Join(c.Routing.Severities, ",") == "critical,warning" && len(c.Routing.Services) == 0 {
			routed++
		}
	}
	if routed != 2 {
		t.Errorf("chat routing in status: %+v", st.Chats)
	}

	if err := h.svc.DeleteChat(ctx, -1002); err != nil {
		t.Fatal(err)
	}
	sd = h.description(t)
	if len(sd.Spec.Notifications.Receivers) != 1 || sd.Spec.Notifications.Routing.Routes != nil {
		t.Errorf("after DeleteChat: %+v", sd.Spec.Notifications)
	}

	_, _ = h.state.Update(ctx, func(st *State) error {
		st.Chats[-1001] = &KnownChat{ID: -1001, Available: true}
		return nil
	})
	if err := h.svc.DeleteToken(ctx); err != nil {
		t.Fatal(err)
	}
	sd = h.description(t)
	if len(sd.Spec.Notifications.Receivers) != 0 || len(sd.Spec.Notifications.Routing.Critical) != 0 {
		t.Errorf("after DeleteToken: %+v", sd.Spec.Notifications)
	}
	if tok, _ := h.tokens.Get(ctx); tok != "" {
		t.Error("token still stored")
	}
	if s, _ := h.state.Get(ctx); len(s.Chats) != 0 || s.Bot != nil {
		t.Errorf("state not cleared: %+v", s)
	}
	assertNoToken(t, "logs", h.logs.String())
}

func TestSaveRoutingKeepsHandWrittenReceiverAndOtherSecrets(t *testing.T) {
	desc := baseDescription + `  notifications:
    receivers:
      - name: oncall
        telegram:
          chatID: -100
          botTokenSecret: {name: kamerton-telegram, key: bot-token}
      - name: other-bot
        telegram:
          chatID: -100
          botTokenSecret: {name: another-secret, key: token}
    routing:
      critical: [oncall, other-bot]
`
	h := newHarness(t, desc)
	ctx := context.Background()
	_ = h.tokens.Set(ctx, testToken)
	if _, err := h.svc.SaveRouting(ctx, []RoutingInput{{ChatID: -100, Severities: []string{"warning"}}}); err != nil {
		t.Fatal(err)
	}
	n := h.description(t).Spec.Notifications
	if strings.Join(n.Routing.Critical, ",") != "other-bot" || strings.Join(n.Routing.Warning, ",") != "oncall" {
		t.Errorf("routing: %+v", n.Routing)
	}
	if len(n.Receivers) != 2 {
		t.Errorf("receivers using another Secret must be untouched: %+v", n.Receivers)
	}
	if _, err := h.svc.SaveRouting(ctx, []RoutingInput{{ChatID: -100, Severities: []string{"bogus"}}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bogus severity: %v", err)
	}
	if _, err := h.svc.SaveRouting(ctx, []RoutingInput{{ChatID: -100, Severities: []string{"critical"}, Services: []string{"nope"}}}); err == nil {
		t.Error("unknown service must be rejected by validation")
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	b := fake.NewClientBuilder().WithScheme(s)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return b.Build()
}

func TestSecretTokenStore(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)
	ts := &SecretTokenStore{Client: c, Namespace: "kamerton", Name: DefaultSecretName, Key: DefaultSecretKey}

	if tok, err := ts.Get(ctx); err != nil || tok != "" {
		t.Fatalf("empty store: %q %v", tok, err)
	}
	if err := ts.Set(ctx, testToken); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "kamerton", Name: DefaultSecretName}, &sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data[DefaultSecretKey]) != testToken || sec.Labels["app.kubernetes.io/managed-by"] != "kamerton" {
		t.Errorf("secret: %+v", sec.ObjectMeta)
	}
	if tok, _ := ts.Get(ctx); tok != testToken {
		t.Error("Get after Set")
	}
	if err := ts.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "kamerton", Name: DefaultSecretName}, &sec); !apierrors.IsNotFound(err) {
		t.Errorf("Secret with only the token must be deleted: %v", err)
	}
}

func TestSecretTokenStoreKeepsExistingSecretAndOtherKeys(t *testing.T) {
	ctx := context.Background()
	// A Secret created by hand with kubectl, as on existing clusters.
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kamerton", Name: DefaultSecretName},
		Data:       map[string][]byte{DefaultSecretKey: []byte(otherTok + "\n"), "note": []byte("keep me")},
	}
	c := newFakeClient(t, existing)
	ts := &SecretTokenStore{Client: c, Namespace: "kamerton", Name: DefaultSecretName, Key: DefaultSecretKey}
	if tok, _ := ts.Get(ctx); tok != otherTok {
		t.Errorf("existing token not read (whitespace trimmed): %q", tok)
	}
	if err := ts.Set(ctx, testToken); err != nil {
		t.Fatal(err)
	}
	if err := ts.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "kamerton", Name: DefaultSecretName}, &sec); err != nil {
		t.Fatalf("Secret with other keys must survive: %v", err)
	}
	if _, ok := sec.Data[DefaultSecretKey]; ok || string(sec.Data["note"]) != "keep me" {
		t.Errorf("data after delete: %v", keysOf(sec.Data))
	}
}

func TestConfigMapStateStoreSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)
	a := &ConfigMapStateStore{Client: c, Namespace: "kamerton", Name: "kamerton-telegram-state"}
	if _, err := a.Update(ctx, func(st *State) error {
		st.Offset = 7
		st.Chats[-100] = &KnownChat{ID: -100, Title: "Ops", Available: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b := &ConfigMapStateStore{Client: c, Namespace: "kamerton", Name: "kamerton-telegram-state"}
	st, err := b.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Offset != 7 || st.Chats[-100] == nil || st.Chats[-100].Title != "Ops" {
		t.Errorf("state after restart: %+v", st)
	}
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
