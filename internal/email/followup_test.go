// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package email

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

type htmlSender struct {
	fakeSender
	html     []string
	password []string
}

func (h *htmlSender) SendHTML(_ context.Context, _ ServerSettings, pw string, to []string, subject, _, html string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, to)
	h.subjects = append(h.subjects, subject)
	h.html = append(h.html, html)
	h.password = append(h.password, pw)
	return nil
}

func TestSendFollowUp(t *testing.T) {
	svc, _, _, _ := newService(t)
	hs := &htmlSender{}
	svc.Sender = hs
	ctx := context.Background()
	rcv := &v1alpha1.EmailReceiver{To: []string{"oncall@example.com"}, From: "kamerton@example.com", Smarthost: "smtp.example.com:465",
		AuthUsername: "kamerton", AuthPasswordSecret: &v1alpha1.SecretKeyRef{Name: DefaultSecretName, Key: DefaultSecretKey}}
	if err := svc.SendFollowUp(ctx, rcv, "s", "t", "<b>h</b>"); !errors.Is(err, ErrNoServer) {
		t.Errorf("without a server: %v", err)
	}
	if _, err := svc.State.Update(ctx, func(st *State) error {
		st.Server = &ServerSettings{Smarthost: "smtp.example.com:465", From: "kamerton@example.com", Username: "kamerton", HasPassword: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = svc.Passwords.Set(ctx, fakePassword)

	for name, r := range map[string]*v1alpha1.EmailReceiver{
		"other server":   {To: rcv.To, Smarthost: "smtp.other.com:465", AuthUsername: "kamerton", AuthPasswordSecret: rcv.AuthPasswordSecret},
		"other login":    {To: rcv.To, Smarthost: rcv.Smarthost, AuthUsername: "someone", AuthPasswordSecret: rcv.AuthPasswordSecret},
		"other secret":   {To: rcv.To, Smarthost: rcv.Smarthost, AuthUsername: "kamerton", AuthPasswordSecret: &v1alpha1.SecretKeyRef{Name: "x", Key: "y"}},
		"no secret":      {To: rcv.To, Smarthost: rcv.Smarthost, AuthUsername: "kamerton"},
		"empty receiver": nil,
	} {
		if err := svc.SendFollowUp(ctx, r, "s", "t", "h"); !errors.Is(err, ErrNotKamertonServer) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := svc.SendFollowUp(ctx, rcv, "Probable cause: shop / payment", "text", "<b>html</b>"); err != nil {
		t.Fatal(err)
	}
	if len(hs.sent) != 1 || hs.sent[0][0] != "oncall@example.com" || hs.html[0] != "<b>html</b>" || hs.password[0] != fakePassword {
		t.Errorf("sent %+v html %+v", hs.sent, hs.html)
	}
}

func TestBuildHTMLMessage(t *testing.T) {
	msg := string(buildHTMLMessage("k@example.com", []string{"a@example.com"}, "Вероятная причина", "plain «text»", "<b>html</b> "+strings.Repeat("x", 100), time.Unix(0, 0)))
	for _, want := range []string{
		"Content-Type: multipart/alternative; boundary=\"" + htmlBoundary + "\"",
		"Content-Type: text/plain; charset=utf-8", "Content-Type: text/html; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable", "--" + htmlBoundary + "--",
		"Subject: =?utf-8?q?",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	for _, line := range strings.Split(msg, "\r\n") {
		if len(line) > 78 && !strings.HasPrefix(line, "Subject:") && !strings.HasPrefix(line, "Content-Type:") {
			t.Errorf("line longer than 78: %q", line)
		}
	}
}
