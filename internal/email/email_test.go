// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package email

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
)

// Exists only in tests; never a real password.
const fakePassword = "fake-smtp-password-for-tests"

type fakeSender struct {
	mu       sync.Mutex
	checks   []ServerSettings
	sent     [][]string
	subjects []string
	sendErr  error
}

func (f *fakeSender) Check(_ context.Context, cfg ServerSettings, pw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks = append(f.checks, cfg)
	if cfg.Username != "" && pw != fakePassword {
		return errors.New("535 5.7.8 authentication failed for " + pw)
	}
	return nil
}

func (f *fakeSender) Send(_ context.Context, _ ServerSettings, _ string, to []string, subject, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, to)
	f.subjects = append(f.subjects, subject)
	return f.sendErr
}

func newService(t *testing.T) (*Service, *fakeSender, string, *bytes.Buffer) {
	t.Helper()
	example, err := os.ReadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "system.yaml")
	if err := os.WriteFile(path, example, 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	fs := &fakeSender{}
	return &Service{
		Passwords: &MemoryPasswordStore{}, State: &MemoryStateStore{}, Descriptions: &descstore.File{Paths: []string{path}},
		Sender: fs, Namespace: "kamerton", Logger: slog.New(slog.NewTextHandler(logs, nil)),
	}, fs, path, logs
}

func TestValidation(t *testing.T) {
	for _, ok := range []string{"smtp.yandex.ru:465", "localhost:25", "[::1]:587"} {
		if err := ValidSmarthost(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "smtp.yandex.ru", ":25", "smtp:0", "smtp:99999", "smtp x:25", "smtp:abc"} {
		if err := ValidSmarthost(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if a, err := NormalizeAddress(" Дежурный <oncall@example.com> "); err != nil || a != "oncall@example.com" {
		t.Errorf("normalize: %q %v", a, err)
	}
	for _, bad := range []string{"", "oncall", "a@b", "a@b.c\r\nBcc: x@y.z", "@example.com"} {
		if _, err := NormalizeAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := validRecipient(RecipientInput{Name: " ", To: []string{"a@b.co"}}); err == nil {
		t.Error("empty name accepted")
	}
	r, err := validRecipient(RecipientInput{Name: "Дежурные", To: []string{"a@b.co", "A@b.co", "c@d.co"}})
	if err != nil || len(r.To) != 2 {
		t.Errorf("dedupe: %v %v", r.To, err)
	}
}

func TestScrubAndMessage(t *testing.T) {
	if got := scrub(errors.New("535 bad "+fakePassword), fakePassword).Error(); strings.Contains(got, fakePassword) {
		t.Errorf("scrub: %s", got)
	}
	msg := string(buildMessage("k@example.com", []string{"a@example.com"}, "Kamerton: тест", "строка 1\nстрока 2", time.Unix(0, 0)))
	if !strings.Contains(msg, "Subject: =?utf-8?q?") || !strings.Contains(msg, "строка 1\r\nстрока 2") || !strings.Contains(msg, "charset=utf-8") {
		t.Errorf("message: %s", msg)
	}
}

func TestFlowKeepsDescriptionInSync(t *testing.T) {
	ctx := context.Background()
	s, fs, path, logs := newService(t)

	if _, err := s.AddRecipient(ctx, RecipientInput{Name: "Дежурные", To: []string{"a@example.com"}}); !errors.Is(err, ErrNoServer) {
		t.Errorf("recipient before server: %v", err)
	}
	if _, err := s.SetServer(ctx, ServerInput{Smarthost: "smtp.example.com:465", From: "kamerton@example.com", Username: "kamerton", Password: "wrong"}); !errors.Is(err, ErrCheckFailed) || strings.Contains(err.Error(), "wrong") {
		t.Errorf("bad password: %v", err)
	}
	if _, err := s.SetServer(ctx, ServerInput{Smarthost: "smtp.example.com:465", From: "kamerton@example.com", Username: "kamerton"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("missing password: %v", err)
	}
	st, err := s.SetServer(ctx, ServerInput{Smarthost: "smtp.example.com:465", From: "kamerton@example.com", Username: "kamerton", Password: fakePassword})
	if err != nil || !st.Configured || !st.HasPassword || st.Problem != "" {
		t.Fatalf("set server: %+v %v", st, err)
	}
	if pw, _ := s.Passwords.Get(ctx); pw != fakePassword {
		t.Error("password not stored")
	}

	rc, err := s.AddRecipient(ctx, RecipientInput{Name: "Дежурные", To: []string{"a@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	// What the system editor does: add a receiver and route critical alerts to it.
	full, _ := s.State.Get(ctx)
	recv := ReceiverPrefix + rc.ID
	if _, err := s.Descriptions.Update(ctx, "otel-demo", func(sd *v1alpha1.SystemDescription) error {
		n := sd.Spec.Notifications
		n.Receivers = append(n.Receivers, v1alpha1.Receiver{Name: recv, Email: s.ReceiverFor(full.Server, full.Recipients[rc.ID])})
		n.Routing.Routes = append(n.Routing.Routes, v1alpha1.Route{Receiver: recv, Severities: []v1alpha1.Severity{"critical"}, Services: []string{"checkout"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(ctx)
	if err != nil || len(status.Recipients) != 1 || len(status.Recipients[0].UsedIn) != 1 {
		t.Fatalf("status: %+v %v", status, err)
	}
	if u := status.Recipients[0].UsedIn[0]; u.System != "otel-demo" || strings.Join(u.Severities, ",") != "critical" || strings.Join(u.Services, ",") != "checkout" {
		t.Errorf("usedIn: %+v", u)
	}

	// Changing addresses and the server rewrites the receiver.
	if _, err := s.UpdateRecipient(ctx, rc.ID, RecipientInput{Name: "Дежурные", To: []string{"b@example.com", "c@example.com"}}); err != nil {
		t.Fatal(err)
	}
	// Same login, no password: the stored password is kept.
	if _, err := s.SetServer(ctx, ServerInput{Smarthost: "smtp.other.com:587", From: "alerts@example.com", Username: "kamerton"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	file := string(b)
	for _, want := range []string{"b@example.com", "c@example.com", "smtp.other.com:587", "alerts@example.com", "name: kamerton-smtp", recv} {
		if !strings.Contains(file, want) {
			t.Errorf("description lacks %q", want)
		}
	}
	if strings.Contains(file, "a@example.com") {
		t.Error("old address remains")
	}
	// Hand-written receiver (team-email) is left alone.
	if !strings.Contains(file, "oncall@example.com") {
		t.Error("hand-written receiver changed")
	}

	if res, err := s.SendTest(ctx, rc.ID); err != nil || !res.OK || len(fs.sent) != 1 || fs.subjects[0] != "Kamerton: test email" {
		t.Errorf("test: %+v %v %q", res, err, fs.subjects)
	}
	fs.sendErr = errors.New("550 mailbox unavailable")
	if res, _ := s.SendTest(ctx, rc.ID); res.OK || !strings.Contains(res.Error, "550") {
		t.Errorf("failed test: %+v", res)
	}

	if err := s.DeleteRecipient(ctx, rc.ID); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), recv) {
		t.Error("receiver not removed")
	}
	if err := s.DeleteServer(ctx); err != nil {
		t.Fatal(err)
	}
	if pw, _ := s.Passwords.Get(ctx); pw != "" {
		t.Error("password not deleted")
	}
	if strings.Contains(string(b), fakePassword) || strings.Contains(logs.String(), fakePassword) {
		t.Error("password leaked to the description or logs")
	}
}

// A tiny SMTP server without TLS or AUTH, enough for the real sender.
func TestSMTPSenderPlain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		w("220 test")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					w("250 ok")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"):
				w("250-test\r\n250 8BITMIME")
			case strings.HasPrefix(cmd, "DATA"):
				inData = true
				w("354 go")
			case strings.HasPrefix(cmd, "QUIT"):
				w("221 bye")
				got <- data.String()
				return
			default:
				w("250 ok")
			}
		}
	}()
	cfg := ServerSettings{Smarthost: ln.Addr().String(), From: "k@example.com"}
	if err := (&SMTPSender{Timeout: 5 * time.Second}).Send(context.Background(), cfg, "", []string{"a@example.com"}, "Тест", "привет"); err != nil {
		t.Fatal(err)
	}
	if msg := <-got; !strings.Contains(msg, "привет") || !strings.Contains(msg, "To: a@example.com") {
		t.Errorf("message: %s", msg)
	}
}
