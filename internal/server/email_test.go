// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/email"
)

// Exists only in tests; never a real password.
const fakeSMTPPassword = "fake-smtp-password-server-tests"

type stubSender struct{ sent int }

func (s *stubSender) Check(_ context.Context, cfg email.ServerSettings, pw string) error {
	if cfg.Username != "" && pw != fakeSMTPPassword {
		return errors.New("535 authentication failed")
	}
	return nil
}

func (s *stubSender) Send(context.Context, email.ServerSettings, string, []string, string, string) error {
	s.sent++
	return nil
}

func TestEmailUnavailableWithoutCluster(t *testing.T) {
	srv := httptest.NewServer(New(Options{EmailUnavailable: "нет кластера"}).Handler())
	defer srv.Close()
	code, body := get(t, srv.URL+"/api/v1/email")
	if code != 200 || !strings.Contains(body, `"available":false`) || !strings.Contains(body, "нет кластера") {
		t.Errorf("status: %d %s", code, body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/email/server", `{"smarthost":"smtp.example.com:465","from":"k@example.com","username":"k","password":"` + fakeSMTPPassword + `"}`},
		{"DELETE", "/api/v1/email/server", ""},
		{"POST", "/api/v1/email/recipients", `{"name":"x","to":["a@example.com"]}`},
		{"PUT", "/api/v1/email/recipients/abcd", `{"name":"x","to":["a@example.com"]}`},
		{"DELETE", "/api/v1/email/recipients/abcd", ""},
		{"POST", "/api/v1/email/recipients/abcd/test", ""},
	} {
		code, body := do(t, c.method, srv.URL+c.path, c.body)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "not_available") || strings.Contains(body, fakeSMTPPassword) {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, body)
		}
	}
	if _, body := get(t, srv.URL+"/api/v1/settings"); !strings.Contains(body, `"email":{"available":false`) {
		t.Errorf("settings: %s", body)
	}
}

// Mutating email endpoints sit behind the same login and CSRF check as the rest of the API.
func TestEmailEndpointsNeedAuth(t *testing.T) {
	a := NewAuth("admin", "secret-password-for-tests", "")
	es := &email.Service{Passwords: &email.MemoryPasswordStore{}, State: &email.MemoryStateStore{}, Descriptions: &descstore.Memory{}, Sender: &stubSender{}}
	srv := httptest.NewServer(New(Options{Email: es, Auth: a}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/email/server", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without CSRF header: %d", resp.StatusCode)
	}
	if code, _ := do(t, "PUT", srv.URL+"/api/v1/email/server", `{}`); code != http.StatusUnauthorized {
		t.Errorf("without session: %d", code)
	}
	if code, _ := get(t, srv.URL+"/api/v1/email"); code != http.StatusUnauthorized {
		t.Errorf("status without session: %d", code)
	}
}

func TestEmailFlowNeverReturnsPassword(t *testing.T) {
	path := writeDesc(t)
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &descstore.File{Paths: []string{path}}
	sender := &stubSender{}
	es := &email.Service{Passwords: &email.MemoryPasswordStore{}, State: &email.MemoryStateStore{}, Descriptions: store, Sender: sender, Logger: log}
	srv := httptest.NewServer(New(Options{Descriptions: store, Email: es, Logger: log}).Handler())
	defer srv.Close()

	var bodies []string
	call := func(method, p, body string, want int) string {
		t.Helper()
		code, resp := do(t, method, srv.URL+p, body)
		bodies = append(bodies, resp)
		if code != want {
			t.Errorf("%s %s: %d (want %d) %s", method, p, code, want, resp)
		}
		return resp
	}

	if b := call("GET", "/api/v1/email", "", 200); !strings.Contains(b, `"configured":false`) || !strings.Contains(b, `"secretName":"kamerton-smtp"`) {
		t.Errorf("initial status: %s", b)
	}
	call("POST", "/api/v1/email/recipients", `{"name":"Дежурные","to":["a@example.com"]}`, 409)
	call("PUT", "/api/v1/email/server", `not json `+fakeSMTPPassword, 400)
	call("PUT", "/api/v1/email/server", `{"smarthost":"smtp.example.com","from":"k@example.com"}`, 400)
	call("PUT", "/api/v1/email/server", `{"smarthost":"smtp.example.com:465","from":"nope"}`, 400)
	call("PUT", "/api/v1/email/server", `{"smarthost":"smtp.example.com:465","from":"k@example.com","username":"k","password":"wrong"}`, 422)
	if b := call("PUT", "/api/v1/email/server", `{"smarthost":"smtp.example.com:465","from":"k@example.com","username":"k","password":"`+fakeSMTPPassword+`"}`, 200); !strings.Contains(b, `"hasPassword":true`) {
		t.Errorf("set server: %s", b)
	}
	call("POST", "/api/v1/email/recipients", `{"name":"Дежурные","to":["not an address"]}`, 400)
	b := call("POST", "/api/v1/email/recipients", `{"name":"Дежурные","to":["a@example.com","b@example.com"]}`, 200)
	var added struct {
		Recipient email.Recipient `json:"recipient"`
	}
	_ = json.Unmarshal([]byte(b), &added)
	id := added.Recipient.ID
	if id == "" {
		t.Fatalf("no id: %s", b)
	}
	call("PUT", "/api/v1/email/recipients/"+id, `{"name":"Дежурные","to":["c@example.com"]}`, 200)
	call("PUT", "/api/v1/email/recipients/../x", `{"name":"x","to":["c@example.com"]}`, 404)
	call("DELETE", "/api/v1/email/recipients/ffff", "", 404)
	if b := call("POST", "/api/v1/email/recipients/"+id+"/test", "", 200); !strings.Contains(b, `"ok":true`) || sender.sent != 1 {
		t.Errorf("test email: %s", b)
	}
	if b := call("GET", "/api/v1/email", "", 200); !strings.Contains(b, "c@example.com") || !strings.Contains(b, `"receiver":"email-`+id+`"`) {
		t.Errorf("status: %s", b)
	}
	call("DELETE", "/api/v1/email/recipients/"+id, "", 200)
	call("DELETE", "/api/v1/email/server", "", 200)
	if b := call("GET", "/api/v1/email", "", 200); !strings.Contains(b, `"configured":false`) {
		t.Errorf("status after delete: %s", b)
	}

	for i, body := range bodies {
		if strings.Contains(body, fakeSMTPPassword) {
			t.Errorf("response %d contains the password: %s", i, body)
		}
	}
	if strings.Contains(logs.String(), fakeSMTPPassword) {
		t.Error("logs contain the password")
	}
	if file, _ := os.ReadFile(path); strings.Contains(string(file), fakeSMTPPassword) {
		t.Error("description file contains the password")
	}
}
