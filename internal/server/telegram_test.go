// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

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
	"testing"
	"time"

	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/telegram"
)

const fakeToken = "123456789:AAFakeTokenForServerTests_abcdefghijkl"

// do sends a request the way the UI does (with the CSRF header).
func do(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(csrfHeader, "kamerton")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func fakeTelegramAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.URL.Path, "/bot"+fakeToken+"/") {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/bot"+fakeToken+"/") {
		case "getMe":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":123456789,"is_bot":true,"first_name":"Alerts","username":"shop_alerts_bot"}}`)
		case "sendMessage":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":-100,"type":"group"},"date":0}}`)
		case "getChat":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":-100,"type":"group","title":"Ops"}}`)
		case "getChatMember":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"status":"member","user":{"id":123456789,"is_bot":true,"first_name":"Alerts"}}}`)
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeDesc(t *testing.T) string {
	t.Helper()
	example, err := os.ReadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "system.yaml")
	if err := os.WriteFile(path, example, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTelegramUnavailableWithoutCluster(t *testing.T) {
	srv := httptest.NewServer(New(Options{TelegramUnavailable: "нет кластера"}).Handler())
	defer srv.Close()
	code, body := get(t, srv.URL+"/api/v1/telegram")
	if code != 200 || !strings.Contains(body, `"available":false`) || !strings.Contains(body, "нет кластера") {
		t.Errorf("status: %d %s", code, body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/telegram/token", `{"token":"` + fakeToken + `"}`},
		{"DELETE", "/api/v1/telegram/token", ""},
		{"POST", "/api/v1/telegram/chats", `{"chatID":-100}`},
		{"POST", "/api/v1/telegram/chats/-100/test", ""},
		{"DELETE", "/api/v1/telegram/chats/-100", ""},
		{"PUT", "/api/v1/telegram/routing", `{"chats":[]}`},
	} {
		code, body := do(t, c.method, srv.URL+c.path, c.body)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "not_available") {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, body)
		}
		if strings.Contains(body, fakeToken) {
			t.Errorf("%s %s echoes the token", c.method, c.path)
		}
	}
	code, body = get(t, srv.URL+"/api/v1/settings")
	if code != 200 || !strings.Contains(body, `"linksEnabled":false`) || !strings.Contains(body, `"mode":"none"`) {
		t.Errorf("settings: %d %s", code, body)
	}
}

func TestTelegramFlowNeverReturnsToken(t *testing.T) {
	tgAPI := fakeTelegramAPI(t)
	path := writeDesc(t)
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &descstore.File{Paths: []string{path}}
	tg := &telegram.Service{
		API: &telegram.Client{BaseURL: tgAPI.URL}, Tokens: &telegram.MemoryTokenStore{}, State: &telegram.MemoryStateStore{},
		Descriptions: store, Logger: log,
	}
	srv := httptest.NewServer(New(Options{Descriptions: store, Telegram: tg, Logger: log, Generator: generator.Options{RunbookBaseURL: "http://localhost:8080"}}).Handler())
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

	if b := call("GET", "/api/v1/telegram", "", 200); !strings.Contains(b, `"configured":false`) {
		t.Errorf("initial status: %s", b)
	}
	call("PUT", "/api/v1/telegram/token", `{"token":"bad"}`, 400)
	call("PUT", "/api/v1/telegram/token", `{"token":"111111111:AAUnknownTokenForTheFakeApi_abcdefg"}`, 400)
	call("PUT", "/api/v1/telegram/token", `not json `+fakeToken, 400)
	if b := call("PUT", "/api/v1/telegram/token", `{"token":"`+fakeToken+`"}`, 200); !strings.Contains(b, "shop_alerts_bot") {
		t.Errorf("set token: %s", b)
	}
	if b := call("GET", "/api/v1/telegram", "", 200); !strings.Contains(b, `"configured":true`) || !strings.Contains(b, "shop_alerts_bot") {
		t.Errorf("status after token: %s", b)
	}
	call("POST", "/api/v1/telegram/chats", `{"chatID":-100}`, 200)
	call("POST", "/api/v1/telegram/chats", `{"chatID":"x"}`, 400)
	if b := call("POST", "/api/v1/telegram/chats/-100/test", "", 200); !strings.Contains(b, `"ok":true`) {
		t.Errorf("test message: %s", b)
	}
	call("PUT", "/api/v1/telegram/routing", `{"chats":[{"chatID":-100,"severities":["critical"],"services":["checkout"]}]}`, 200)
	b := call("GET", "/api/v1/description", "", 200)
	if !strings.Contains(b, "telegram-n100") || !strings.Contains(b, "routes:") {
		t.Errorf("description after routing: %s", b)
	}
	call("PUT", "/api/v1/telegram/routing", `{"chats":[{"chatID":-100,"severities":["critical"],"services":["no-such-service"]}]}`, 422)
	call("DELETE", "/api/v1/telegram/chats/-100", "", 200)
	call("DELETE", "/api/v1/telegram/token", "", 200)
	if b := call("GET", "/api/v1/telegram", "", 200); !strings.Contains(b, `"configured":false`) {
		t.Errorf("status after delete: %s", b)
	}

	_, secret, _ := strings.Cut(fakeToken, ":")
	for i, body := range bodies {
		if strings.Contains(body, secret) {
			t.Errorf("response %d contains the token: %s", i, body)
		}
	}
	if strings.Contains(logs.String(), secret) {
		t.Errorf("logs contain the token")
	}
	file, _ := os.ReadFile(path)
	if strings.Contains(string(file), secret) {
		t.Error("description file contains the token")
	}
}

func TestPutDescriptionFileMode(t *testing.T) {
	path := writeDesc(t)
	srv := httptest.NewServer(New(Options{Descriptions: &descstore.File{Paths: []string{path}}}).Handler())
	defer srv.Close()

	example, _ := os.ReadFile(path)
	changed := strings.Replace(string(example), "environment: demo", "environment: staging", 1)
	code, body := do(t, "PUT", srv.URL+"/api/v1/description", changed)
	if code != 200 || !strings.Contains(body, `"saved":true`) {
		t.Fatalf("put: %d %s", code, body)
	}
	if got, _ := os.ReadFile(path); string(got) != changed {
		t.Error("file was not written verbatim")
	}
	if _, body := get(t, srv.URL+"/api/v1/description"); !strings.Contains(body, "environment: staging") || !strings.Contains(body, `"writable":true`) {
		t.Errorf("get after put: %s", body)
	}
	code, body = do(t, "PUT", srv.URL+"/api/v1/description", "apiVersion: kamerton.dev/v1alpha1\nkind: SystemDescription\nmetadata: {name: x}\nspec: {services: []}\n")
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"errors"`) {
		t.Errorf("invalid put: %d %s", code, body)
	}
	if got, _ := os.ReadFile(path); string(got) != changed {
		t.Error("an invalid description must not be written")
	}
}

func TestPutDescriptionWithoutStorage(t *testing.T) {
	srv := newTestServer(t)
	example, _ := os.ReadFile("../../testdata/otel-demo/system.yaml")
	code, body := do(t, "PUT", srv.URL+"/api/v1/description", string(example))
	if code != http.StatusConflict || !strings.Contains(body, "not_writable") {
		t.Errorf("put without storage: %d %s", code, body)
	}
}

type promStub struct{}

func (promStub) Query(context.Context, string, time.Time) (model.Value, error) {
	return model.Vector{{Metric: model.Metric{"service_name": "checkout"}, Value: 3}, {Metric: model.Metric{"service_name": "unknown-svc"}, Value: 1}}, nil
}

func (promStub) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (model.Matrix, error) {
	return nil, errors.New("unused")
}

func clusterClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func TestClusterModeDescriptionAndDiscovery(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sd.Namespace = "kamerton"
	c := clusterClient(t, sd)
	store := &descstore.Cluster{Client: c, Namespace: "kamerton"}
	srv := httptest.NewServer(New(Options{Descriptions: store, Prometheus: promStub{}}).Handler())
	defer srv.Close()

	_, body := get(t, srv.URL+"/api/v1/discovery/services")
	var disc struct {
		Services []struct {
			Name      string `json:"name"`
			Described bool   `json:"described"`
		} `json:"services"`
	}
	_ = json.Unmarshal([]byte(body), &disc)
	got := map[string]bool{}
	for _, s := range disc.Services {
		got[s.Name] = s.Described
	}
	if !got["checkout"] || got["unknown-svc"] {
		t.Errorf("described flags in cluster mode: %s", body)
	}

	_, body = get(t, srv.URL+"/api/v1/description")
	if !strings.Contains(body, `"source":"cluster:kamerton/otel-demo"`) || !strings.Contains(body, `"mode":"cluster"`) {
		t.Errorf("description: %s", body)
	}

	raw, _ := description.ToYAML(sd)
	changed := strings.Replace(string(raw), "environment: demo", "environment: prod", 1)
	if code, body := do(t, "PUT", srv.URL+"/api/v1/description", changed); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	var got2 v1alpha1.SystemDescription
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "otel-demo"}, &got2); err != nil {
		t.Fatal(err)
	}
	if got2.Spec.Environment != "prod" {
		t.Errorf("resource not updated: %q", got2.Spec.Environment)
	}

	newDesc := "apiVersion: kamerton.dev/v1alpha1\nkind: SystemDescription\nmetadata: {name: other}\nspec:\n  services:\n    - {name: api, type: http}\n"
	if code, body := do(t, "PUT", srv.URL+"/api/v1/description", newDesc); code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "other"}, &got2); err != nil {
		t.Errorf("not created: %v", err)
	}
}

func TestRunbookEndpoint(t *testing.T) {
	path := writeDesc(t)
	srv := httptest.NewServer(New(Options{Descriptions: &descstore.File{Paths: []string{path}}, Generator: generator.Options{RunbookBaseURL: "http://localhost:8080"}}).Handler())
	defer srv.Close()
	code, body := get(t, srv.URL+"/api/v1/runbooks/checkout")
	if code != 200 || !strings.Contains(body, "KamertonErrorRateHigh") || !strings.Contains(body, `"title":"Too many failed requests"`) {
		t.Errorf("runbook: %d %.400s", code, body)
	}
	if code, _ := get(t, srv.URL+"/api/v1/runbooks/nope"); code != 404 {
		t.Errorf("unknown service: %d", code)
	}
	_, settings := get(t, srv.URL+"/api/v1/settings")
	if !strings.Contains(settings, `"externalURL":"http://localhost:8080"`) || !strings.Contains(settings, `"linksEnabled":true`) {
		t.Errorf("settings: %s", settings)
	}
}

func TestExistingSecretIsRecognized(t *testing.T) {
	tgAPI := fakeTelegramAPI(t)
	c := clusterClient(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kamerton", Name: telegram.DefaultSecretName},
		Data:       map[string][]byte{telegram.DefaultSecretKey: []byte(fakeToken)},
	})
	tg := &telegram.Service{
		API:          &telegram.Client{BaseURL: tgAPI.URL},
		Tokens:       &telegram.SecretTokenStore{Client: c, Namespace: "kamerton", Name: telegram.DefaultSecretName, Key: telegram.DefaultSecretKey},
		State:        &telegram.ConfigMapStateStore{Client: c, Namespace: "kamerton", Name: "kamerton-telegram-state"},
		Descriptions: &descstore.Memory{}, Namespace: "kamerton",
	}
	srv := httptest.NewServer(New(Options{Telegram: tg}).Handler())
	defer srv.Close()
	_, body := get(t, srv.URL+"/api/v1/telegram")
	if !strings.Contains(body, `"configured":true`) || !strings.Contains(body, "shop_alerts_bot") {
		t.Errorf("existing Secret not recognized: %s", body)
	}
	_, secret, _ := strings.Cut(fakeToken, ":")
	if strings.Contains(body, secret) {
		t.Error("status contains the token")
	}
}
