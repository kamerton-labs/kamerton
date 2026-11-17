// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	srv := httptest.NewServer(New(Options{UI: ui, UIBuilt: true}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// post sends a POST the way the UI does (with the CSRF header).
func post(t *testing.T, url, contentType, body string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(csrfHeader, "kamerton")
	return http.DefaultClient.Do(req)
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAPI(t *testing.T) {
	srv := newTestServer(t)

	if code, body := get(t, srv.URL+"/api/v1/health"); code != 200 || !strings.Contains(body, "ok") {
		t.Errorf("health: %d %s", code, body)
	}
	if code, body := get(t, srv.URL+"/api/v1/schema"); code != 200 || !strings.Contains(body, `"SystemDescription"`) {
		t.Errorf("schema: %d", code)
	}
	code, body := get(t, srv.URL+"/api/v1/discovery/services")
	if code != 200 || !strings.Contains(body, `"configured":false`) || !strings.Contains(body, `"services":[]`) {
		t.Errorf("discovery must report not configured, not fake data: %d %s", code, body)
	}
	if code, _ := get(t, srv.URL+"/api/v1/nope"); code != 404 {
		t.Errorf("unknown endpoint: %d", code)
	}
}

func TestValidateEndpoint(t *testing.T) {
	srv := newTestServer(t)
	example, err := os.ReadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{string(example), true},
		{"apiVersion: kamerton.dev/v1alpha1\nkind: SystemDescription\nmetadata: {name: x}\nspec: {services: []}\n", false},
	} {
		resp, err := post(t, srv.URL+"/api/v1/description/validate", "application/yaml", tc.body)
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			Valid  bool `json:"valid"`
			Errors []struct{ Path, Message string }
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if res.Valid != tc.valid {
			t.Errorf("valid=%v want %v, errors %+v", res.Valid, tc.valid, res.Errors)
		}
	}
}

func TestPreviewRules(t *testing.T) {
	srv := newTestServer(t)
	example, _ := os.ReadFile("../../testdata/otel-demo/system.yaml")
	resp, err := post(t, srv.URL+"/api/v1/rules/preview", "application/yaml", string(example))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var res struct {
		RuleCount int    `json:"ruleCount"`
		Rules     string `json:"rules"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.RuleCount == 0 || !strings.Contains(res.Rules, "groups:") {
		t.Errorf("unexpected preview: %d", res.RuleCount)
	}
}

func TestBacktestNotConfigured(t *testing.T) {
	srv := newTestServer(t)
	resp, err := post(t, srv.URL+"/api/v1/backtest", "application/json", `{"expr":"up == 0","for":"5m"}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestSPAFallback(t *testing.T) {
	srv := newTestServer(t)
	for _, p := range []string{"/", "/setup/stack", "/services"} {
		if code, body := get(t, srv.URL+p); code != 200 || body != "<html>app</html>" {
			t.Errorf("%s: %d %q", p, code, body)
		}
	}
	if code, body := get(t, srv.URL+"/assets/app.js"); code != 200 || body != "console.log(1)" {
		t.Errorf("asset: %d %q", code, body)
	}
}
