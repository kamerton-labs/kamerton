// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/email"
	"github.com/kamerton-labs/kamerton/internal/telegram"
)

// Mail server configured in Kamerton in these tests.
const (
	kmHost = "smtp.kamerton.example:587"
	kmUser = "alerts@kamerton.example"
	// attackerHost is an SMTP server of someone who wants a Secret value.
	attackerHost = "smtp.attacker.example:25"
)

func ref(name, key string) *v1alpha1.SecretKeyRef {
	return &v1alpha1.SecretKeyRef{Name: name, Key: key}
}

func tgReceiver(name string, chat int64, secret *v1alpha1.SecretKeyRef) v1alpha1.Receiver {
	return v1alpha1.Receiver{Name: name, Telegram: &v1alpha1.TelegramReceiver{ChatID: chat, BotTokenSecret: *secret}}
}

func mailReceiver(name, host, user string, secret *v1alpha1.SecretKeyRef) v1alpha1.Receiver {
	return v1alpha1.Receiver{Name: name, Email: &v1alpha1.EmailReceiver{
		To: []string{"oncall@example.com"}, From: "kamerton@example.com", Smarthost: host, AuthUsername: user, AuthPasswordSecret: secret,
	}}
}

// secretDesc is a small valid description with the given receivers, all
// routed to warnings.
func secretDesc(name, ns string, rs ...v1alpha1.Receiver) *v1alpha1.SystemDescription {
	sd := &v1alpha1.SystemDescription{}
	sd.Name, sd.Namespace = name, ns
	sd.Spec.Services = []v1alpha1.Service{{Name: "api", Type: v1alpha1.ServiceTypeHTTP}}
	if len(rs) > 0 {
		var names []string
		for _, r := range rs {
			names = append(names, r.Name)
		}
		sd.Spec.Notifications = &v1alpha1.Notifications{Receivers: rs, Routing: &v1alpha1.Routing{Warning: names}}
	}
	return sd
}

func toYAML(t *testing.T, sd *v1alpha1.SystemDescription) string {
	t.Helper()
	b, err := description.ToYAML(sd)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Receivers written by a cluster admin with kubectl.
var (
	handTelegram = tgReceiver("ops-telegram", -1001, ref("ops-bot", "token"))
	handMail     = mailReceiver("ops-mail", "mail.corp.example:25", "ops", ref("corp-smtp", "pass"))
	foreignTG    = tgReceiver("team-telegram", -1002, ref(telegram.DefaultSecretName, telegram.DefaultSecretKey))
)

type secretEnv struct {
	api   string
	c     client.Client
	email *email.Service
}

// secretCluster is a cluster-mode server with Telegram and email setup and
// two hand-written descriptions: legacy (in Kamerton's namespace) and
// foreign (in team-a).
func secretCluster(t *testing.T) *secretEnv {
	t.Helper()
	c := clusterClient(t,
		secretDesc("legacy", "kamerton", handTelegram, handMail),
		secretDesc("foreign", "team-a", foreignTG),
	)
	store := &descstore.Cluster{Client: c, Namespace: "kamerton"}
	tg := &telegram.Service{
		API: &telegram.Client{BaseURL: fakeTelegramAPI(t).URL}, Tokens: &telegram.MemoryTokenStore{}, State: &telegram.MemoryStateStore{},
		Descriptions: store, Namespace: "kamerton",
	}
	es := &email.Service{Passwords: &email.MemoryPasswordStore{}, State: &email.MemoryStateStore{}, Descriptions: store, Sender: &stubSender{}, Namespace: "kamerton"}
	if _, err := es.SetServer(context.Background(), email.ServerInput{Smarthost: kmHost, From: "kamerton@kamerton.example", Username: kmUser, Password: fakeSMTPPassword}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Options{Descriptions: store, Telegram: tg, Email: es, Namespace: "kamerton"}).Handler())
	t.Cleanup(srv.Close)
	return &secretEnv{api: srv.URL + "/api/v1", c: c, email: es}
}

type secretCase struct {
	name string
	sd   *v1alpha1.SystemDescription
	want int
	// inMessage must appear in the rejection message.
	inMessage []string
}

func runSecretCases(t *testing.T, method, url string, cases []secretCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := do(t, method, url, toYAML(t, tc.sd))
			if code != tc.want {
				t.Fatalf("%s %s: %d (want %d) %s", method, url, code, tc.want, body)
			}
			if code == http.StatusBadRequest {
				var resp struct {
					Error  string
					Code   string
					Errors []description.Issue
				}
				decode(t, body, &resp)
				if len(resp.Errors) == 0 && resp.Code == "secret_not_allowed" {
					t.Errorf("no issues in %s", body)
				}
				for _, s := range tc.inMessage {
					if !strings.Contains(resp.Error, s) {
						t.Errorf("message lacks %q: %s", s, resp.Error)
					}
				}
			}
		})
	}
}

func TestSecretRefsOnCreate(t *testing.T) {
	env := secretCluster(t)
	managedTG := ref(telegram.DefaultSecretName, telegram.DefaultSecretKey)
	managedMail := ref(email.DefaultSecretName, email.DefaultSecretKey)
	runSecretCases(t, "POST", env.api+"/systems", []secretCase{
		{name: "managed telegram", sd: secretDesc("c1", "", tgReceiver("tg", -5, managedTG)), want: http.StatusCreated},
		{name: "managed telegram, Kamerton's namespace named", sd: secretDesc("c2", "kamerton", tgReceiver("tg", -5, managedTG)), want: http.StatusCreated},
		{name: "managed email", sd: secretDesc("c3", "", mailReceiver("email-abc", kmHost, kmUser, managedMail)), want: http.StatusCreated},
		{name: "email without password", sd: secretDesc("c4", "", mailReceiver("plain", attackerHost, "", nil)), want: http.StatusCreated},
		{name: "email with username but no password", sd: secretDesc("c5", "", mailReceiver("plain", attackerHost, "me", nil)), want: http.StatusCreated},
		{
			name: "smarthost swap", sd: secretDesc("x1", "", mailReceiver("steal", attackerHost, kmUser, managedMail)), want: http.StatusBadRequest,
			inMessage: []string{`"steal"`, kmHost, attackerHost},
		},
		{name: "managed SMTP secret, other username", sd: secretDesc("x2", "", mailReceiver("steal", kmHost, "someone", managedMail)), want: http.StatusBadRequest},
		{
			name: "bot token as SMTP password", sd: secretDesc("x3", "", mailReceiver("steal", attackerHost, "x", managedTG)), want: http.StatusBadRequest,
			inMessage: []string{`"steal"`, "kamerton-telegram"},
		},
		{name: "bot token as SMTP password to Kamerton's server", sd: secretDesc("x4", "", mailReceiver("steal", kmHost, kmUser, managedTG)), want: http.StatusBadRequest},
		{name: "hand-written SMTP secret", sd: secretDesc("x5", "", mailReceiver("steal", "mail.corp.example:25", "ops", ref("corp-smtp", "pass"))), want: http.StatusBadRequest},
		{name: "SMTP secret as bot token", sd: secretDesc("x6", "", tgReceiver("tg", -5, managedMail)), want: http.StatusBadRequest, inMessage: []string{`"tg"`}},
		{name: "other telegram secret", sd: secretDesc("x7", "", tgReceiver("tg", -5, ref("ops-bot", "token"))), want: http.StatusBadRequest},
		{name: "managed telegram, other key", sd: secretDesc("x8", "", tgReceiver("tg", -5, ref(telegram.DefaultSecretName, "other"))), want: http.StatusBadRequest},
		{name: "another namespace", sd: secretDesc("x9", "team-a"), want: http.StatusBadRequest, inMessage: []string{"kamerton", "team-a"}},
	})
	// Nothing rejected was written.
	for _, name := range []string{"x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8"} {
		var sd v1alpha1.SystemDescription
		if err := env.c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: name}, &sd); err == nil {
			t.Errorf("%s was created", name)
		}
	}
	var sd v1alpha1.SystemDescription
	if err := env.c.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "x9"}, &sd); err == nil {
		t.Error("x9 was created in team-a")
	}
}

func TestSecretRefsOnUpdate(t *testing.T) {
	env := secretCluster(t)
	managedTG := ref(telegram.DefaultSecretName, telegram.DefaultSecretKey)
	managedMail := ref(email.DefaultSecretName, email.DefaultSecretKey)
	edited := func(rs ...v1alpha1.Receiver) *v1alpha1.SystemDescription {
		sd := secretDesc("legacy", "kamerton", rs...)
		sd.Spec.Environment = "prod"
		return sd
	}
	movedMail := handMail
	movedMail.Email = handMail.Email.DeepCopy()
	movedMail.Email.Smarthost = attackerHost
	otherUser := handMail
	otherUser.Email = handMail.Email.DeepCopy()
	otherUser.Email.AuthUsername = "root"
	renamed := handMail
	renamed.Name = "ops-mail-2"
	newChat := handTelegram
	newChat.Telegram = handTelegram.Telegram.DeepCopy()
	newChat.Telegram.ChatID = -2002
	otherKey := handTelegram
	otherKey.Telegram = handTelegram.Telegram.DeepCopy()
	otherKey.Telegram.BotTokenSecret.Key = "other"

	runSecretCases(t, "PUT", env.api+"/systems/legacy", []secretCase{
		{name: "hand-written refs unchanged", sd: edited(handTelegram, handMail), want: http.StatusOK},
		{name: "hand-written email to another smarthost", sd: edited(handTelegram, movedMail), want: http.StatusBadRequest, inMessage: []string{`"ops-mail"`, attackerHost}},
		{name: "hand-written email with another username", sd: edited(handTelegram, otherUser), want: http.StatusBadRequest},
		{name: "hand-written receiver removed", sd: edited(handTelegram), want: http.StatusOK},
	})
	// The stored version now has no ops-mail: adding it again is a new reference.
	runSecretCases(t, "PUT", env.api+"/systems/legacy", []secretCase{
		{name: "removed ref cannot come back", sd: edited(handTelegram, handMail), want: http.StatusBadRequest},
	})
	// Restore it as a cluster admin would (kubectl), then go on.
	restoreLegacy(t, env.c, handTelegram, handMail)
	runSecretCases(t, "PUT", env.api+"/systems/legacy", []secretCase{
		{name: "same hand-written email under another name", sd: edited(handTelegram, handMail, renamed), want: http.StatusOK},
		{name: "hand-written telegram, new chat", sd: edited(newChat, handMail), want: http.StatusOK},
		{name: "hand-written telegram, other key", sd: edited(otherKey, handMail), want: http.StatusBadRequest},
		{name: "managed telegram added", sd: edited(handTelegram, handMail, tgReceiver("tg", -7, managedTG)), want: http.StatusOK},
		{name: "managed email added", sd: edited(handTelegram, handMail, mailReceiver("email-1", kmHost, kmUser, managedMail)), want: http.StatusOK},
		{name: "smarthost swap added", sd: edited(handTelegram, handMail, mailReceiver("steal", attackerHost, kmUser, managedMail)), want: http.StatusBadRequest},
		{name: "bot token as SMTP password added", sd: edited(handTelegram, handMail, mailReceiver("steal", attackerHost, "x", managedTG)), want: http.StatusBadRequest},
		{name: "hand-written SMTP secret to attacker", sd: edited(handTelegram, handMail, mailReceiver("steal", attackerHost, "ops", ref("corp-smtp", "pass"))), want: http.StatusBadRequest},
	})

	// Outside Kamerton's namespace Kamerton's Secrets are not available, but
	// what the admin wrote may stay.
	foreign := func(rs ...v1alpha1.Receiver) *v1alpha1.SystemDescription {
		sd := secretDesc("foreign", "team-a", rs...)
		sd.Spec.Environment = "stage"
		return sd
	}
	runSecretCases(t, "PUT", env.api+"/systems/foreign", []secretCase{
		{name: "foreign: unchanged ref", sd: foreign(foreignTG), want: http.StatusOK},
		{name: "foreign: managed email", sd: foreign(foreignTG, mailReceiver("email-1", kmHost, kmUser, managedMail)), want: http.StatusBadRequest, inMessage: []string{"team-a"}},
		{name: "foreign: other telegram secret", sd: foreign(tgReceiver("tg", -9, ref("ops-bot", "token"))), want: http.StatusBadRequest},
	})
	// A namespace in the document does not move a saved description.
	moved := foreign(foreignTG, mailReceiver("email-1", kmHost, kmUser, managedMail))
	moved.Namespace = "kamerton"
	runSecretCases(t, "PUT", env.api+"/systems/foreign", []secretCase{{name: "foreign: namespace in the document", sd: moved, want: http.StatusBadRequest}})

	// The single-description endpoint: replace and create.
	runSecretCases(t, "PUT", env.api+"/description", []secretCase{
		{name: "legacy endpoint: unchanged", sd: edited(handTelegram, handMail), want: http.StatusOK},
		{name: "legacy endpoint: smarthost swap", sd: edited(handTelegram, handMail, mailReceiver("steal", attackerHost, kmUser, managedMail)), want: http.StatusBadRequest},
		{name: "legacy endpoint: new system with hand-written ref", sd: secretDesc("fresh", "", handMail), want: http.StatusBadRequest},
		{name: "legacy endpoint: new system with managed email", sd: secretDesc("fresh", "", mailReceiver("email-1", kmHost, kmUser, managedMail)), want: http.StatusOK},
	})

	var legacy v1alpha1.SystemDescription
	if err := env.c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "legacy"}, &legacy); err != nil {
		t.Fatal(err)
	}
	for _, r := range legacy.Spec.Notifications.Receivers {
		if r.Email != nil && r.Email.Smarthost == attackerHost {
			t.Errorf("attacker receiver stored: %+v", r)
		}
	}
}

// restoreLegacy writes the legacy receivers directly, as kubectl would.
func restoreLegacy(t *testing.T, c client.Client, rs ...v1alpha1.Receiver) {
	t.Helper()
	var sd v1alpha1.SystemDescription
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "legacy"}, &sd); err != nil {
		t.Fatal(err)
	}
	sd.Spec.Notifications = secretDesc("legacy", "kamerton", rs...).Spec.Notifications
	if err := c.Update(context.Background(), &sd); err != nil {
		t.Fatal(err)
	}
}

// The editor learns about a forbidden reference from validation, before saving.
func TestSecretRefsInValidation(t *testing.T) {
	env := secretCluster(t)
	attack := secretDesc("legacy", "kamerton", handTelegram, handMail, mailReceiver("steal", attackerHost, kmUser, ref(email.DefaultSecretName, email.DefaultSecretKey)))
	want := "/spec/notifications/receivers/2/email/authPasswordSecret"
	for _, p := range []string{"/systems/legacy/validate", "/description/validate", "/systems/legacy/rules/preview", "/rules/preview"} {
		code, body := do(t, "POST", env.api+p, toYAML(t, attack))
		var res description.Result
		decode(t, body, &res)
		if code != 200 || res.Valid || len(res.Errors) != 1 || res.Errors[0].Path != want || !strings.Contains(res.Errors[0].Message, `"steal"`) {
			t.Errorf("%s: %d %s", p, code, body)
		}
		code, body = do(t, "POST", env.api+p, toYAML(t, secretDesc("legacy", "kamerton", handTelegram, handMail)))
		decode(t, body, &res)
		if code != 200 || !res.Valid {
			t.Errorf("%s unchanged: %d %s", p, code, body)
		}
	}
}

// Telegram routing and the email setup write receivers through the store;
// the UI then saves what they wrote.
func TestManagedServicesStillSave(t *testing.T) {
	env := secretCluster(t)
	if code, body := do(t, "PUT", env.api+"/telegram/token", `{"token":"`+fakeToken+`"}`); code != 200 {
		t.Fatalf("token: %d %s", code, body)
	}
	if code, body := do(t, "POST", env.api+"/telegram/chats", `{"chatID":-100}`); code != 200 {
		t.Fatalf("chat: %d %s", code, body)
	}
	if code, body := do(t, "PUT", env.api+"/systems/legacy/telegram/routing", `{"chats":[{"chatID":-100,"severities":["critical"],"services":[]}]}`); code != 200 {
		t.Fatalf("routing: %d %s", code, body)
	}

	// A recipient: the editor writes its receiver as the UI does.
	rc, err := env.email.AddRecipient(context.Background(), email.RecipientInput{Name: "Дежурные", To: []string{"oncall@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := env.email.State.Get(context.Background())
	recv := v1alpha1.Receiver{Name: email.ReceiverPrefix + rc.ID, Email: env.email.ReceiverFor(st.Server, st.Recipients[rc.ID])}
	saveWith := func(extra ...v1alpha1.Receiver) (int, string) {
		var cur v1alpha1.SystemDescription
		if err := env.c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "legacy"}, &cur); err != nil {
			t.Fatal(err)
		}
		cur.Spec.Notifications.Receivers = append(cur.Spec.Notifications.Receivers, extra...)
		for _, r := range extra {
			cur.Spec.Notifications.Routing.Warning = append(cur.Spec.Notifications.Routing.Warning, r.Name)
		}
		return do(t, "PUT", env.api+"/systems/legacy", toYAML(t, &cur))
	}
	if code, body := saveWith(recv); code != 200 {
		t.Fatalf("save with the email receiver: %d %s", code, body)
	}

	// The mail server moves: the email service rewrites receivers itself.
	if _, err := env.email.SetServer(context.Background(), email.ServerInput{Smarthost: "smtp2.kamerton.example:465", From: "kamerton@kamerton.example", Username: kmUser}); err != nil {
		t.Fatal(err)
	}
	var cur v1alpha1.SystemDescription
	_ = env.c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "legacy"}, &cur)
	found := 0
	for _, r := range cur.Spec.Notifications.Receivers {
		switch {
		case r.Name == recv.Name && r.Email.Smarthost == "smtp2.kamerton.example:465":
			found++
		case r.Telegram != nil && r.Telegram.ChatID == -100:
			found++
		}
	}
	if found != 2 {
		t.Fatalf("receivers written by the services: %+v", cur.Spec.Notifications.Receivers)
	}
	// Saving what the services wrote is allowed.
	if code, body := saveWith(); code != 200 {
		t.Fatalf("save after the services: %d %s", code, body)
	}
	// Telegram routing still refuses descriptions outside Kamerton's namespace.
	if code, _ := do(t, "PUT", env.api+"/systems/foreign/telegram/routing", `{"chats":[{"chatID":-100,"severities":["critical"],"services":[]}]}`); code < 400 {
		t.Errorf("routing in team-a: %d", code)
	}
}

// In file mode nothing manages Telegram or email: only references already
// in the stored file may stay. The example description is saved as the
// end-to-end flows do.
func TestSecretRefsFileMode(t *testing.T) {
	example, err := os.ReadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "otel-demo.yaml"), example, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := descstore.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Options{Descriptions: store}).Handler())
	defer srv.Close()
	api := srv.URL + "/api/v1"

	text := string(example)
	if code, body := do(t, "PUT", api+"/systems/otel-demo", strings.Replace(text, "environment: demo", "environment: prod", 1)); code != 200 {
		t.Fatalf("save the example: %d %s", code, body)
	}
	if code, body := do(t, "PUT", api+"/description", text); code != 200 {
		t.Fatalf("legacy save of the example: %d %s", code, body)
	}
	swapped := strings.Replace(text, "smarthost: smtp.example.com:587", "smarthost: "+attackerHost, 1)
	if code, body := do(t, "PUT", api+"/systems/otel-demo", swapped); code != http.StatusBadRequest || !strings.Contains(body, "team-email") {
		t.Errorf("smarthost swap: %d %s", code, body)
	}
	copied := strings.Replace(text, "name: otel-demo", "name: otel-copy", 1)
	if code, body := do(t, "POST", api+"/systems", copied); code != http.StatusBadRequest {
		t.Errorf("new system with Secret references: %d %s", code, body)
	}
	if code, body := do(t, "POST", api+"/systems", toYAML(t, secretDesc("plain", "", mailReceiver("m", attackerHost, "", nil)))); code != http.StatusCreated {
		t.Errorf("new system without Secret references: %d %s", code, body)
	}
	saved, _ := os.ReadFile(filepath.Join(dir, "otel-demo.yaml"))
	if strings.Contains(string(saved), attackerHost) {
		t.Error("the rejected smarthost was written")
	}
	var res description.Result
	_, body := do(t, "POST", api+"/systems/otel-demo/validate", swapped)
	if err := json.Unmarshal([]byte(body), &res); err != nil || res.Valid {
		t.Errorf("validate swap: %s", body)
	}
}
