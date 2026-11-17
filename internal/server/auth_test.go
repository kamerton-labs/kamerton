// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func newAuthServer(t *testing.T, auth *Auth) *httptest.Server {
	t.Helper()
	ui := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}}
	srv := httptest.NewServer(New(Options{UI: ui, UIBuilt: true, Auth: auth}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func withHeader(k, v string) reqOpt    { return func(r *http.Request) { r.Header.Set(k, v) } }

var xrw = withHeader("X-Requested-With", "kamerton")

func call(t *testing.T, method, url, body string, opts ...reqOpt) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	return nil
}

func login(t *testing.T, srv *httptest.Server, user, pass string, opts ...reqOpt) (*http.Response, string) {
	t.Helper()
	b, _ := json.Marshal(loginRequest{Username: user, Password: pass})
	return call(t, "POST", srv.URL+"/api/auth/login", string(b), append([]reqOpt{xrw}, opts...)...)
}

func TestAuthDisabledKeepsAPIOpen(t *testing.T) {
	srv := newAuthServer(t, nil)
	if resp, body := call(t, "GET", srv.URL+"/api/v1/systems", ""); resp.StatusCode != 200 {
		t.Fatalf("systems without login: %d %s", resp.StatusCode, body)
	}
	// The CSRF header is still required: without login, a cross-site form
	// POST would otherwise reach every state-changing endpoint.
	if resp, _ := call(t, "POST", srv.URL+"/api/v1/systems", "x: 1"); resp.StatusCode != 403 {
		t.Fatalf("mutation without X-Requested-With: %d", resp.StatusCode)
	}
	if resp, body := call(t, "POST", srv.URL+"/api/v1/description/validate", "x: 1", xrw); resp.StatusCode != 200 {
		t.Fatalf("validate without login: %d %s", resp.StatusCode, body)
	}
	// Paths that are not clean are checked as the mux routes them.
	if resp, _ := call(t, "POST", srv.URL+"//api/v1/systems", "x: 1"); resp.StatusCode == 201 {
		t.Fatalf("unclean path bypassed the CSRF check: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "GET", srv.URL+"/", ""); resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("security headers: %v", resp.Header)
	}
	_, body := call(t, "GET", srv.URL+"/api/auth/session", "")
	if !strings.Contains(body, `"enabled":false`) {
		t.Errorf("session: %s", body)
	}
	if _, body := call(t, "GET", srv.URL+"/api/v1/settings", ""); !strings.Contains(body, `"auth":{"enabled":false}`) {
		t.Errorf("settings: %s", body)
	}
	if resp, _ := login(t, srv, "a", "b"); resp.StatusCode != 400 {
		t.Errorf("login when disabled: %d", resp.StatusCode)
	}
}

func TestAuthEnabled(t *testing.T) {
	const pass = "s3cret-pass"
	srv := newAuthServer(t, NewAuth("admin", pass, ""))

	resp, body := call(t, "GET", srv.URL+"/api/v1/systems", "")
	if resp.StatusCode != 401 || !strings.Contains(body, `"code":"unauthorized"`) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("systems without cookie: %d %s", resp.StatusCode, body)
	}
	for _, p := range []string{"/healthz", "/api/v1/health", "/", "/login", "/runbooks/api/HighErrorRate"} {
		if resp, body := call(t, "GET", srv.URL+p, ""); resp.StatusCode != 200 {
			t.Errorf("%s must be open: %d %s", p, resp.StatusCode, body)
		}
	}
	if resp, _ := call(t, "GET", srv.URL+"//api/v1/systems", ""); resp.StatusCode != 401 {
		t.Errorf("unclean path without session: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "GET", srv.URL+"/api/v1/nope", ""); resp.StatusCode != 401 {
		t.Errorf("unknown endpoint without session: %d", resp.StatusCode)
	}
	if _, body := call(t, "GET", srv.URL+"/api/auth/session", ""); !strings.Contains(body, `"authenticated":false,"enabled":true`) {
		t.Errorf("session before login: %s", body)
	}

	if resp, body := login(t, srv, "admin", "wrong"); resp.StatusCode != 401 || sessionCookie(resp) != nil || !strings.Contains(body, "invalid_credentials") {
		t.Fatalf("wrong password: %d %s", resp.StatusCode, body)
	}
	if resp, _ := login(t, srv, "root", pass); resp.StatusCode != 401 {
		t.Fatalf("wrong user: %d", resp.StatusCode)
	}

	b, _ := json.Marshal(loginRequest{Username: "admin", Password: pass})
	if resp, _ := call(t, "POST", srv.URL+"/api/auth/login", string(b)); resp.StatusCode != 403 {
		t.Fatalf("login without X-Requested-With: %d", resp.StatusCode)
	}

	resp, body = login(t, srv, "admin", pass)
	c := sessionCookie(resp)
	if resp.StatusCode != 200 || c == nil {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, pass) || strings.Contains(c.Value, pass) {
		t.Fatal("password leaked in the response")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.MaxAge != int(SessionTTL.Seconds()) {
		t.Errorf("cookie attributes: %+v", c)
	}
	if resp, body := call(t, "GET", srv.URL+"/api/v1/systems", "", withCookie(c)); resp.StatusCode != 200 {
		t.Fatalf("systems with cookie: %d %s", resp.StatusCode, body)
	}
	if _, body := call(t, "GET", srv.URL+"/api/auth/session", "", withCookie(c)); !strings.Contains(body, `"authenticated":true,"enabled":true,"user":"admin"`) {
		t.Errorf("session after login: %s", body)
	}
	if _, body := call(t, "GET", srv.URL+"/api/v1/settings", "", withCookie(c)); !strings.Contains(body, `"auth":{"enabled":true}`) {
		t.Errorf("settings: %s", body)
	}

	if resp, _ := call(t, "POST", srv.URL+"/api/v1/description/validate", "x: 1", withCookie(c)); resp.StatusCode != 403 {
		t.Errorf("mutation without X-Requested-With: %d", resp.StatusCode)
	}
	if resp, _ := call(t, "POST", srv.URL+"/api/v1/description/validate", "x: 1", withCookie(c), xrw); resp.StatusCode != 200 {
		t.Errorf("mutation with X-Requested-With: %d", resp.StatusCode)
	}

	payload, sig, _ := strings.Cut(c.Value, ".")
	for name, v := range map[string]string{
		"bad signature": payload + "." + strings.Repeat("A", len(sig)),
		"garbage":       "not-a-token",
		"other key":     NewAuth("admin", "other-password", "").issue(time.Now().Add(time.Hour)),
	} {
		if resp, _ := call(t, "GET", srv.URL+"/api/v1/systems", "", withCookie(&http.Cookie{Name: SessionCookie, Value: v})); resp.StatusCode != 401 {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}

	resp, _ = call(t, "POST", srv.URL+"/api/auth/logout", "", withCookie(c), xrw)
	if lc := sessionCookie(resp); resp.StatusCode != 200 || lc == nil || lc.MaxAge >= 0 || lc.Value != "" {
		t.Errorf("logout: %d %+v", resp.StatusCode, lc)
	}
}

func TestSessionToken(t *testing.T) {
	a := NewAuth("admin", "pw", "explicit-secret")
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }

	tok := a.issue(now.Add(SessionTTL))
	if u, ok := a.verify(tok); !ok || u != "admin" {
		t.Fatalf("fresh token: %q %v", u, ok)
	}
	expired := a.issue(now.Add(-time.Second))
	if _, ok := a.verify(expired); ok {
		t.Error("expired token accepted")
	}
	now = now.Add(SessionTTL + time.Second)
	if _, ok := a.verify(tok); ok {
		t.Error("token accepted after 12h")
	}
	b := NewAuth("admin", "changed", "explicit-secret")
	b.now = a.now
	if _, ok := b.verify(a.issue(now.Add(time.Hour))); !ok {
		t.Error("token from a replica with the same secret rejected")
	}
	d1, d2, d3 := NewAuth("admin", "pw", ""), NewAuth("admin", "pw", ""), NewAuth("admin", "pw2", "")
	tok = d1.issue(time.Now().Add(time.Hour))
	if _, ok := d2.verify(tok); !ok {
		t.Error("derived key differs between instances")
	}
	if _, ok := d3.verify(tok); ok {
		t.Error("token valid after password change")
	}
	other := NewAuth("intruder", "pw", "explicit-secret")
	other.now = a.now
	if _, ok := a.verify(other.issue(now.Add(time.Hour))); ok {
		t.Error("token of another user accepted")
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := NewAuth("admin", "pw", "k")
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }
	srv := newAuthServer(t, a)

	for i := range loginMaxFailures {
		if resp, _ := login(t, srv, "admin", "bad"); resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i+1, resp.StatusCode)
		}
	}
	resp, body := login(t, srv, "admin", "pw")
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "60" || !strings.Contains(body, `"retryAfter":60`) {
		t.Fatalf("after %d failures even the right password must get 429: %d %q %s", loginMaxFailures, resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}
	now = now.Add(30 * time.Second)
	if resp, _ := login(t, srv, "admin", "pw"); resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "30" {
		t.Fatalf("still blocked: %d %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	now = now.Add(31 * time.Second)
	if resp, _ := login(t, srv, "admin", "pw"); resp.StatusCode != 200 {
		t.Fatalf("after the window: %d", resp.StatusCode)
	}
	// A success resets the counter.
	for range loginMaxFailures - 1 {
		login(t, srv, "admin", "bad")
	}
	if resp, _ := login(t, srv, "admin", "pw"); resp.StatusCode != 200 {
		t.Fatalf("below the limit: %d", resp.StatusCode)
	}
}

func TestSecureCookieBehindHTTPSProxy(t *testing.T) {
	srv := newAuthServer(t, NewAuth("admin", "pw", "k"))
	resp, _ := login(t, srv, "admin", "pw", withHeader("X-Forwarded-Proto", "https"))
	if c := sessionCookie(resp); c == nil || !c.Secure {
		t.Fatalf("cookie must be Secure behind an HTTPS proxy: %+v", c)
	}
}
