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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/generator"
)

const shopYAML = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
  annotations:
    kamerton.dev/layout: '{"api":[10,20]}'
spec:
  # comment kept verbatim in file mode
  services:
    - name: api
      type: http
      critical: true
      dependsOn: [db]
    - name: db
      type: database
`

// promRange answers instant queries with ALERTS and range queries with a
// series that matches for 30 minutes.
type promRange struct{}

func (promRange) Query(_ context.Context, q string, _ time.Time) (model.Value, error) {
	if strings.HasPrefix(q, "ALERTS") {
		return model.Vector{{Metric: model.Metric{
			"alertname": "KamertonErrorRateHigh", "alertstate": "firing", "service": "api", "severity": "critical", "kamerton_system": "shop",
		}, Value: 1}}, nil
	}
	return model.Vector{{Metric: model.Metric{"service_name": "api"}, Value: 3}, {Metric: model.Metric{"service_name": "payments"}, Value: 1}}, nil
}

func (promRange) QueryRange(_ context.Context, _ string, start, end time.Time, step time.Duration) (model.Matrix, error) {
	var vals []model.SamplePair
	from := end.Add(-time.Hour)
	for t := from; t.Before(from.Add(30 * time.Minute)); t = t.Add(step) {
		vals = append(vals, model.SamplePair{Timestamp: model.TimeFromUnixNano(t.UnixNano()), Value: 0.2})
	}
	return model.Matrix{{Metric: model.Metric{"service_name": "api"}, Values: vals}}, nil
}

type amStub struct{}

func (amStub) ActiveAlerts(context.Context, map[string]string) ([]alertmanager.Alert, error) {
	return []alertmanager.Alert{{
		Labels:      map[string]string{"alertname": "KamertonErrorRateHigh", "service": "api", "severity": "critical", "kamerton_system": "shop", "kamerton_managed": "true"},
		Annotations: map[string]string{"summary": "High error rate: api", "kamerton_title": "Много ошибочных запросов", "kamerton_value": "20%", "kamerton_threshold": "5%"},
	}}, nil
}

func decode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

type systemList struct {
	Mode    string `json:"mode"`
	Systems []struct {
		ID       string `json:"id"`
		Services int    `json:"services"`
		Rules    int    `json:"rules"`
		State    string `json:"state"`
		Valid    bool   `json:"valid"`
		Alerts   *struct {
			Critical int `json:"critical"`
		} `json:"alerts"`
	} `json:"systems"`
}

func TestSystemsFileMode(t *testing.T) {
	dir := t.TempDir()
	example, err := os.ReadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "otel-demo.yaml"), example, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("apiVersion: kamerton.dev/v1alpha1\nkind: SystemDescription\nmetadata: {name: broken}\nspec: {services: []}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := descstore.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Options{Descriptions: store, Prometheus: promRange{}, Alertmanager: amStub{}, Generator: generator.Options{RunbookBaseURL: "http://localhost:8080"}}).Handler())
	defer srv.Close()
	api := srv.URL + "/api/v1"

	var list systemList
	_, body := get(t, api+"/systems")
	decode(t, body, &list)
	if list.Mode != "file" || len(list.Systems) != 2 || list.Systems[0].ID != "broken" || list.Systems[0].Valid || list.Systems[0].State != "invalid" {
		t.Fatalf("list: %s", body)
	}
	if s := list.Systems[1]; s.ID != "otel-demo" || s.Services != 16 || s.Rules == 0 || s.State != "ok" || s.Alerts == nil {
		t.Errorf("otel-demo summary: %+v", s)
	}

	if code, body := do(t, "POST", api+"/systems", shopYAML); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, _ := do(t, "POST", api+"/systems", shopYAML); code != http.StatusConflict {
		t.Errorf("duplicate create: %d", code)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "shop.yaml")); string(got) != shopYAML {
		t.Error("new system is not written verbatim to <dir>/shop.yaml")
	}
	_, body = get(t, api+"/systems")
	decode(t, body, &list)
	if len(list.Systems) != 3 {
		t.Fatalf("after create: %s", body)
	}
	for _, s := range list.Systems {
		if s.ID == "shop" && (s.State != "firing" || s.Alerts == nil || s.Alerts.Critical != 1) {
			t.Errorf("shop must show its firing alert: %+v", s)
		}
	}

	code, body := get(t, api+"/systems/shop")
	if code != 200 || !strings.Contains(body, "comment kept verbatim") || !strings.Contains(body, `"valid":true`) {
		t.Errorf("get: %d %s", code, body)
	}
	moved := strings.Replace(shopYAML, `[10,20]`, `[300,40]`, 1)
	if code, body := do(t, "PUT", api+"/systems/shop", moved); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "shop.yaml")); string(got) != moved {
		t.Error("save is not verbatim")
	}
	if code, body := do(t, "PUT", api+"/systems/shop", strings.Replace(shopYAML, "name: shop", "name: other", 1)); code != http.StatusConflict || !strings.Contains(body, "rename") {
		t.Errorf("rename: %d %s", code, body)
	}
	if code, _ := do(t, "PUT", api+"/systems/nope", shopYAML); code != http.StatusNotFound {
		t.Errorf("unknown system: %d", code)
	}
	if code, body := do(t, "PUT", api+"/systems/shop", strings.Replace(shopYAML, "dependsOn: [db]", "dependsOn: [nope]", 1)); code != http.StatusUnprocessableEntity || !strings.Contains(body, "/spec/services/0/dependsOn/0") {
		t.Errorf("invalid save: %d %s", code, body)
	}

	code, body = get(t, api+"/systems/broken")
	if code != 200 || !strings.Contains(body, `"valid":false`) || !strings.Contains(body, "/spec/services") {
		t.Errorf("broken get: %d %s", code, body)
	}
	fixed := "apiVersion: kamerton.dev/v1alpha1\nkind: SystemDescription\nmetadata: {name: broken}\nspec: {services: [{name: a, type: http}]}\n"
	if code, body := do(t, "PUT", api+"/systems/broken", fixed); code != 200 {
		t.Errorf("fix broken: %d %s", code, body)
	}

	_, body = do(t, "POST", api+"/systems/shop/validate", strings.Replace(shopYAML, "name: shop", "name: x", 1))
	if !strings.Contains(body, `"valid":false`) || !strings.Contains(body, "/metadata/name") {
		t.Errorf("validate rename: %s", body)
	}

	_, body = do(t, "POST", api+"/systems/shop/rules/preview", "")
	if !strings.Contains(body, "KamertonErrorRateHigh") {
		t.Errorf("preview stored: %.300s", body)
	}

	draftYAML := strings.Replace(shopYAML, "critical: true", "critical: true\n      errorRate: {threshold: 0.1, for: 10m}", 1)
	req, _ := json.Marshal(map[string]any{"yaml": draftYAML, "service": "api", "alert": "KamertonErrorRateHigh", "range": "1d"})
	code, body = do(t, "POST", api+"/systems/shop/backtest", string(req))
	var bt struct {
		Firings int `json:"firings"`
		Rule    struct {
			Expr, For, Threshold string
		} `json:"rule"`
	}
	decode(t, body, &bt)
	if code != 200 || bt.Firings != 1 || bt.Rule.For != "10m" || !strings.Contains(bt.Rule.Expr, "> 0.1") || bt.Rule.Threshold != "10%" {
		t.Errorf("rule backtest: %d %s", code, body)
	}
	for _, alert := range []map[string]any{
		{"alert": "KamertonTelemetryMissing", "range": "1d"},
		{"service": "api", "alert": "KamertonNoReadyPods", "range": "1d"},
	} {
		req, _ := json.Marshal(alert)
		if code, body := do(t, "POST", api+"/systems/shop/backtest", string(req)); code != 200 || !strings.Contains(body, `"rule":{`) {
			t.Errorf("backtest of %v: %d %.300s", alert, code, body)
		}
	}
	req, _ = json.Marshal(map[string]any{"service": "api", "alert": "KamertonQueueLagHigh"})
	if code, _ := do(t, "POST", api+"/systems/shop/backtest", string(req)); code != http.StatusNotFound {
		t.Errorf("backtest of a rule that does not exist: %d", code)
	}

	_, body = get(t, api+"/systems/shop/rules/state")
	if !strings.Contains(body, `"reachable":true`) || !strings.Contains(body, `"state":"firing"`) {
		t.Errorf("rule states: %s", body)
	}

	_, body = get(t, api+"/systems/shop/discovery")
	var disc struct {
		Services []struct {
			Name      string   `json:"name"`
			Described bool     `json:"described"`
			InSystems []string `json:"inSystems"`
		} `json:"services"`
	}
	decode(t, body, &disc)
	for _, s := range disc.Services {
		if s.Name == "api" && !s.Described {
			t.Errorf("api is described in shop: %s", body)
		}
		if s.Name == "payments" && (s.Described || len(s.InSystems) != 0) {
			t.Errorf("payments is new: %s", body)
		}
	}

	if code, body := get(t, api+"/systems/otel-demo/runbooks/checkout"); code != 200 || !strings.Contains(body, `"systemID":"otel-demo"`) {
		t.Errorf("system runbook: %d %.200s", code, body)
	}
	if code, body := get(t, api+"/runbooks/api"); code != 200 || !strings.Contains(body, `"system":"shop"`) {
		t.Errorf("legacy runbook lookup: %d %.200s", code, body)
	}

	_, body = get(t, api+"/systems/shop/alerts")
	if !strings.Contains(body, `"title":"Много ошибочных запросов"`) || !strings.Contains(body, `"system":"shop"`) || !strings.Contains(body, `"threshold":"5%"`) {
		t.Errorf("alerts: %s", body)
	}

	if _, body := get(t, api+"/description"); !strings.Contains(body, `"isTemplate":false`) {
		t.Errorf("legacy description: %.200s", body)
	}

	if code, body := do(t, "DELETE", api+"/systems/shop", ""); code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(dir, "shop.yaml")); !os.IsNotExist(err) {
		t.Error("file not removed")
	}
	if code, _ := get(t, api+"/systems/shop"); code != http.StatusNotFound {
		t.Errorf("deleted system still readable: %d", code)
	}
}

func TestSystemsSeveralFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	_ = os.WriteFile(a, []byte(shopYAML), 0o600)
	b := filepath.Join(dir, "b.yaml")
	_ = os.WriteFile(b, []byte(strings.Replace(shopYAML, "name: shop", "name: billing", 1)), 0o600)
	store, err := descstore.NewFile(a + "," + b)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Options{Descriptions: store}).Handler())
	defer srv.Close()
	var list systemList
	_, body := get(t, srv.URL+"/api/v1/systems")
	decode(t, body, &list)
	if len(list.Systems) != 2 || list.Systems[0].ID != "billing" || list.Systems[1].ID != "shop" || list.Systems[1].State != "unknown" {
		t.Fatalf("list: %s", body)
	}
	if code, _ := do(t, "POST", srv.URL+"/api/v1/systems", strings.Replace(shopYAML, "name: shop", "name: third", 1)); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "third.yaml")); err != nil {
		t.Error(err)
	}
	_, body = get(t, srv.URL+"/api/v1/systems")
	decode(t, body, &list)
	if len(list.Systems) != 3 {
		t.Errorf("created system not listed: %s", body)
	}
}

func TestCustomRuleThroughAPI(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "shop.yaml"), []byte(shopYAML), 0o600)
	store, _ := descstore.NewFile(dir)
	srv := httptest.NewServer(New(Options{Descriptions: store, Prometheus: promRange{}}).Handler())
	defer srv.Close()
	api := srv.URL + "/api/v1/systems/shop"

	withRule := strings.Replace(shopYAML, "      dependsOn: [db]\n", `      dependsOn: [db]
      customRules:
        - name: orders-stuck
          title: Заказы не обрабатываются
          condition: {metric: orders_pending, operator: gt, threshold: 100}
          unit: шт
`, 1)
	if code, body := do(t, "PUT", api, withRule); code != 200 {
		t.Fatalf("save custom rule: %d %s", code, body)
	}
	_, body := do(t, "POST", api+"/rules/preview", "")
	if !strings.Contains(body, "KamertonCustomOrdersStuck") || !strings.Contains(body, "kamerton_title: Заказы не обрабатываются") || !strings.Contains(body, "kamerton_rule: orders-stuck") {
		t.Errorf("preview: %.1500s", body)
	}
	_, body = get(t, api)
	if !strings.Contains(body, "orders_pending") {
		t.Error("custom rule not stored")
	}
	req, _ := json.Marshal(map[string]any{"service": "api", "alert": "KamertonCustomOrdersStuck", "range": "1d"})
	if code, body := do(t, "POST", api+"/backtest", string(req)); code != 200 || !strings.Contains(body, `"expr":"max (orders_pending) > 100"`) {
		t.Errorf("backtest custom rule: %d %s", code, body)
	}

	bad := strings.Replace(withRule, "condition: {metric: orders_pending, operator: gt, threshold: 100}", "expr: 'sum(rate(orders_total[5m]) >'", 1)
	code, body := do(t, "PUT", api, bad)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "/spec/services/0/customRules/0/expr") || !strings.Contains(body, "parse error") {
		t.Errorf("bad expr: %d %s", code, body)
	}
	_, body = do(t, "POST", api+"/validate", bad)
	if !strings.Contains(body, "/spec/services/0/customRules/0/expr") {
		t.Errorf("validate bad expr: %s", body)
	}
}

func TestSystemsClusterMode(t *testing.T) {
	otel, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	otel.Namespace = "kamerton"
	otel.Annotations = map[string]string{"team": "sre"}
	dup := &v1alpha1.SystemDescription{}
	dup.Name, dup.Namespace = "otel-demo", "other"
	dup.Spec.Services = []v1alpha1.Service{{Name: "a", Type: v1alpha1.ServiceTypeHTTP}}
	c := clusterClient(t, otel, dup)
	store := &descstore.Cluster{Client: c, Namespace: "kamerton"}
	srv := httptest.NewServer(New(Options{Descriptions: store}).Handler())
	defer srv.Close()
	api := srv.URL + "/api/v1"

	var list systemList
	_, body := get(t, api+"/systems")
	decode(t, body, &list)
	if list.Mode != "cluster" || len(list.Systems) != 2 || list.Systems[0].ID != "otel-demo~kamerton" || list.Systems[1].ID != "otel-demo~other" {
		t.Fatalf("list: %s", body)
	}

	if code, body := do(t, "POST", api+"/systems", shopYAML); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var shop v1alpha1.SystemDescription
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "shop"}, &shop); err != nil {
		t.Fatal(err)
	}
	if shop.Annotations["kamerton.dev/layout"] != `{"api":[10,20]}` {
		t.Errorf("layout annotation not stored: %v", shop.Annotations)
	}

	// Save the otel-demo in kamerton with a layout: spec and layout change, the
	// "team" annotation stays.
	_, body = get(t, api+"/systems/otel-demo~kamerton")
	var got struct{ YAML string }
	decode(t, body, &got)
	if !strings.Contains(got.YAML, "team: sre") {
		t.Errorf("annotations missing from the file form: %s", got.YAML)
	}
	changed := strings.Replace(got.YAML, "environment: demo", "environment: prod", 1)
	changed = strings.Replace(changed, "team: sre", "team: sre\n    kamerton.dev/layout: '{\"frontend\":[1,2]}'", 1)
	if code, body := do(t, "PUT", api+"/systems/otel-demo~kamerton", changed); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	var otel2 v1alpha1.SystemDescription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "otel-demo"}, &otel2)
	if otel2.Spec.Environment != "prod" || otel2.Annotations["kamerton.dev/layout"] != `{"frontend":[1,2]}` || otel2.Annotations["team"] != "sre" {
		t.Errorf("after save: env=%s annotations=%v", otel2.Spec.Environment, otel2.Annotations)
	}
	var other v1alpha1.SystemDescription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "other", Name: "otel-demo"}, &other)
	if len(other.Spec.Services) != 1 {
		t.Error("the description with the same name in another namespace was changed")
	}

	noLayout := strings.Replace(changed, "    kamerton.dev/layout: '{\"frontend\":[1,2]}'\n", "", 1)
	if code, body := do(t, "PUT", api+"/systems/otel-demo~kamerton", noLayout); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "otel-demo"}, &otel2)
	if _, ok := otel2.Annotations["kamerton.dev/layout"]; ok || otel2.Annotations["team"] != "sre" {
		t.Errorf("layout not removed: %v", otel2.Annotations)
	}

	if code, body := do(t, "DELETE", api+"/systems/shop", ""); code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: "shop"}, &shop); err == nil {
		t.Error("not deleted")
	}
	if code, _ := do(t, "DELETE", api+"/systems/shop", ""); code != http.StatusNotFound {
		t.Errorf("second delete: %d", code)
	}
}

func TestSystemsListNamesChannelsWithoutSecret(t *testing.T) {
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
	// Only the bot token is absent.
	missing := func(_ context.Context, _ string, ref v1alpha1.SecretKeyRef) bool {
		return ref.Name == "kamerton-telegram"
	}
	srv := httptest.NewServer(New(Options{Descriptions: store, SecretMissing: missing}).Handler())
	defer srv.Close()

	var list struct {
		Systems []struct {
			ChannelsOff []channelOff `json:"channelsOff"`
		} `json:"systems"`
	}
	_, body := get(t, srv.URL+"/api/v1/systems")
	decode(t, body, &list)
	if len(list.Systems) != 1 {
		t.Fatalf("list: %s", body)
	}
	want := []channelOff{{Receiver: "oncall-telegram", Channel: "telegram"}}
	if got := list.Systems[0].ChannelsOff; !reflect.DeepEqual(got, want) {
		t.Errorf("channelsOff = %+v, want %+v", got, want)
	}
}

func TestSystemFromGitIsReadOnly(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sd.Namespace = "kamerton"
	sd.Annotations = map[string]string{
		descstore.AnnotationGitRepo: "https://example.com/team/obs.git#main", descstore.AnnotationGitPath: "systems/otel-demo.yaml",
		descstore.AnnotationGitCommit: "0123456789abcdef",
	}
	sch := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	store := &descstore.Cluster{Client: fake.NewClientBuilder().WithScheme(sch).WithObjects(sd).Build(), Namespace: "kamerton"}
	srv := httptest.NewServer(New(Options{Descriptions: store}).Handler())
	defer srv.Close()
	api := srv.URL + "/api/v1"

	var list struct {
		Systems []struct {
			Git *descstore.GitSource `json:"git"`
		} `json:"systems"`
	}
	_, body := get(t, api+"/systems")
	decode(t, body, &list)
	if len(list.Systems) != 1 || list.Systems[0].Git == nil || list.Systems[0].Git.Path != "systems/otel-demo.yaml" {
		t.Fatalf("list: %s", body)
	}
	var one struct {
		Writable bool                 `json:"writable"`
		YAML     string               `json:"yaml"`
		Git      *descstore.GitSource `json:"git"`
	}
	_, body = get(t, api+"/systems/otel-demo")
	decode(t, body, &one)
	if one.Writable || one.Git == nil || one.Git.Commit != "0123456789abcdef" {
		t.Fatalf("system: writable=%v git=%+v", one.Writable, one.Git)
	}
	if code, body := do(t, "PUT", api+"/systems/otel-demo", one.YAML); code != http.StatusConflict || !strings.Contains(body, "from_git") {
		t.Errorf("save: %d %s", code, body)
	}
	if code, body := do(t, "DELETE", api+"/systems/otel-demo", ""); code != http.StatusConflict || !strings.Contains(body, "from_git") {
		t.Errorf("delete: %d %s", code, body)
	}
}
