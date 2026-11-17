// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

func stackServer(t *testing.T, opts Options) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shop.yaml"), []byte(shopYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	billing := strings.Replace(strings.Replace(shopYAML, "name: shop", "name: billing", 1), "spec:\n", "spec:\n  stack: team-b\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "billing.yaml"), []byte(billing), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := descstore.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts.Descriptions = store
	if opts.Sets == nil {
		opts.Sets = &stack.Sets{Store: &stack.FileStateStore{Path: filepath.Join(dir, ".kamerton-stacks.json")}}
	}
	srv := httptest.NewServer(New(opts).Handler())
	t.Cleanup(srv.Close)
	return srv, dir
}

func TestStacksAPI(t *testing.T) {
	srv, dir := stackServer(t, Options{DiscoveryUnavailable: "только в кластере"})
	api := srv.URL + "/api/v1/stacks"

	var list struct {
		Sets []struct {
			ID       string         `json:"id"`
			Name     string         `json:"name"`
			Builtin  bool           `json:"builtin"`
			UsedBy   []string       `json:"usedBy"`
			Health   []stack.Status `json:"health"`
			Programs []stack.Program
		} `json:"sets"`
		Discovery struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
		} `json:"discovery"`
	}
	_, body := get(t, api)
	decode(t, body, &list)
	if len(list.Sets) != 1 || list.Sets[0].ID != stack.BuiltinID || !list.Sets[0].Builtin || len(list.Sets[0].Health) != 6 || list.Discovery.Available || list.Discovery.Reason != "только в кластере" {
		t.Fatalf("fresh: %s", body)
	}
	// billing asks for a set that does not exist yet: it uses the built-in one.
	if u := list.Sets[0].UsedBy; len(u) != 2 {
		t.Errorf("both systems use the built-in set for now: %v", u)
	}

	if code, body := do(t, "POST", api, `{"name":"Команда Б","urls":{"prometheus":"ftp://x"}}`); code != 400 || !strings.Contains(body, "http://") {
		t.Errorf("bad address: %d %s", code, body)
	}
	if code, body := do(t, "POST", api, `{"id":"team-b","name":"Команда Б","urls":{"prometheus":"http://prom-b:9090","alertmanager":"http://am-b:9093"},"ruleLabels":{"team":"b"}}`); code != 201 || !strings.Contains(body, `"id":"team-b"`) {
		t.Fatalf("create: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(dir, ".kamerton-stacks.json")); err != nil {
		t.Errorf("sets are kept next to the descriptions: %v", err)
	}
	_, body = get(t, api)
	decode(t, body, &list)
	if len(list.Sets) != 2 || list.Sets[1].ID != "team-b" || len(list.Sets[1].UsedBy) != 1 || list.Sets[1].UsedBy[0] != "billing" || len(list.Sets[0].UsedBy) != 1 {
		t.Errorf("billing now uses team-b: %s", body)
	}

	// Change and reset an address of the built-in set.
	if code, body := do(t, "PUT", api+"/main/programs/grafana", `{"url":"http://grafana.monitoring:3000"}`); code != 200 || !strings.Contains(body, `"overridden":true`) || !strings.Contains(body, `"origin":"own"`) {
		t.Errorf("override: %d %s", code, body)
	}
	if code, body := do(t, "DELETE", api+"/main/programs/grafana", ""); code != 200 || strings.Contains(body, `"overridden":true`) {
		t.Errorf("reset: %d %s", code, body)
	}
	if code, _ := do(t, "PUT", api+"/main/programs/nagios", `{"url":"http://x"}`); code != 404 {
		t.Errorf("unknown program: %d", code)
	}
	if code, body := do(t, "PUT", api+"/team-b", `{"name":"Команда Б (prod)"}`); code != 200 || !strings.Contains(body, "Команда Б (prod)") {
		t.Errorf("rename: %d %s", code, body)
	}
	if code, _ := do(t, "PUT", api+"/nope", `{"name":"x"}`); code != 404 {
		t.Errorf("unknown set: %d", code)
	}

	// A used set and the built-in set cannot be deleted.
	if code, body := do(t, "DELETE", api+"/team-b", ""); code != 409 || !strings.Contains(body, `"in_use"`) || !strings.Contains(body, "billing") {
		t.Errorf("delete used: %d %s", code, body)
	}
	if code, body := do(t, "DELETE", api+"/main", ""); code != 409 || !strings.Contains(body, "builtin") {
		t.Errorf("delete built-in: %d %s", code, body)
	}

	// Systems show their set; an unknown set is a warning, not an error.
	_, body = get(t, srv.URL+"/api/v1/systems")
	if !strings.Contains(body, `"stack":"team-b","stackName":"Команда Б (prod)"`) || !strings.Contains(body, `"stacks":2`) {
		t.Errorf("systems: %s", body)
	}
	draft := strings.Replace(shopYAML, "spec:\n", "spec:\n  stack: nope\n", 1)
	_, body = do(t, "POST", srv.URL+"/api/v1/systems/shop/validate", draft)
	if !strings.Contains(body, `"valid":true`) || !strings.Contains(body, `there is no set of programs \"nope\"`) {
		t.Errorf("unknown set in a draft: %s", body)
	}
	if code, body := do(t, "PUT", srv.URL+"/api/v1/systems/shop", draft); code != 200 || !strings.Contains(body, `\"nope\"`) {
		t.Errorf("save with unknown set: %d %s", code, body)
	}
	_, body = get(t, srv.URL+"/api/v1/systems/shop")
	if !strings.Contains(body, `"unknown":"nope"`) || !strings.Contains(body, `"id":"main"`) {
		t.Errorf("system with unknown set: %s", body)
	}
	// Free the set and delete it.
	if code, _ := do(t, "PUT", srv.URL+"/api/v1/systems/billing", strings.Replace(shopYAML, "name: shop", "name: billing", 1)); code != 200 {
		t.Fatal("save billing")
	}
	if code, body := do(t, "DELETE", api+"/team-b", ""); code != 200 {
		t.Errorf("delete: %d %s", code, body)
	}

	// Discovery is not available in file mode, with the reason.
	if _, body := get(t, api+"/discover"); !strings.Contains(body, `"available":false`) || !strings.Contains(body, "только в кластере") {
		t.Errorf("discover: %s", body)
	}
}

func TestStackCheckEndpoint(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/-/ready":
			_, _ = io.WriteString(w, "ready")
		case "/api/v1/status/buildinfo":
			_, _ = io.WriteString(w, `{"status":"success","data":{"version":"3.5.0"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer prom.Close()
	srv, _ := stackServer(t, Options{})
	check := srv.URL + "/api/v1/stacks/check"
	if _, body := do(t, "POST", check, `{"kind":"prometheus","url":"`+prom.URL+`"}`); !strings.Contains(body, `"state":"ok"`) || !strings.Contains(body, `"version":"3.5.0"`) {
		t.Errorf("prometheus: %s", body)
	}
	if _, body := do(t, "POST", check, `{"kind":"alertmanager","url":"`+prom.URL+`"}`); !strings.Contains(body, `"state":"wrong_program"`) || !strings.Contains(body, "it is not Alertmanager") {
		t.Errorf("not alertmanager: %s", body)
	}
	if _, body := do(t, "POST", check, `{"kind":"prometheus","url":"file:///etc/passwd"}`); !strings.Contains(body, `"state":"bad_url"`) {
		t.Errorf("only http/https: %s", body)
	}
	if code, _ := do(t, "POST", check, `{"kind":"nagios","url":"http://x"}`); code != 400 {
		t.Errorf("unknown kind: %d", code)
	}
}

// recorder notes which client (by address) was asked.
type recorder struct {
	mu    sync.Mutex
	asked []string
}

func (r *recorder) note(name string) {
	r.mu.Lock()
	r.asked = append(r.asked, name)
	r.mu.Unlock()
}

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.asked
	r.asked = nil
	return out
}

type recProm struct {
	name string
	rec  *recorder
}

func (p recProm) Query(context.Context, string, time.Time) (model.Value, error) {
	p.rec.note(p.name)
	return model.Vector{}, nil
}

func (p recProm) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (model.Matrix, error) {
	p.rec.note(p.name)
	return model.Matrix{}, nil
}

type recAM struct {
	name   string
	rec    *recorder
	alerts []alertmanager.Alert
}

func (a recAM) ActiveAlerts(_ context.Context, filter map[string]string) ([]alertmanager.Alert, error) {
	a.rec.note(a.name)
	var out []alertmanager.Alert
	for _, al := range a.alerts {
		if s := filter["kamerton_system"]; s != "" && al.Labels["kamerton_system"] != s {
			continue
		}
		out = append(out, al)
	}
	return out, nil
}

func alert(fp, system string) alertmanager.Alert {
	return alertmanager.Alert{Fingerprint: fp, Labels: map[string]string{"alertname": "KamertonErrorRateHigh", "service": "api", "severity": "critical", "kamerton_system": system, "kamerton_managed": "true"}}
}

// Every read of Prometheus and Alertmanager for a system goes to the
// programs of that system's set.
func TestPerSetClients(t *testing.T) {
	rec := &recorder{}
	sets := &stack.Sets{Store: &stack.MemoryStateStore{}, Flags: stack.Flags{URLs: map[string]string{stack.KindPrometheus: "http://prom-main", stack.KindAlertmanager: "http://am-main"}}}
	if _, err := sets.Create(context.Background(), stack.Input{ID: "team-b", Name: ptr("B"), URLs: map[string]string{stack.KindPrometheus: "http://prom-b", stack.KindAlertmanager: "http://am-b"}}); err != nil {
		t.Fatal(err)
	}
	// The same billing alert reaches both Alertmanagers (both Prometheus
	// select every rule): it must be shown once.
	amMain := recAM{name: "am-main", rec: rec, alerts: []alertmanager.Alert{alert("f-shop", "shop"), alert("f-billing", "billing")}}
	srv, _ := stackServer(t, Options{
		Sets:         sets,
		Prometheus:   recProm{name: "prom-main", rec: rec},
		Alertmanager: amMain,
		NewPrometheus: func(u string) (prometheus.Querier, error) {
			return recProm{name: strings.TrimPrefix(u, "http://"), rec: rec}, nil
		},
		NewAlertmanager: func(u string) alertmanager.Reader {
			return recAM{name: strings.TrimPrefix(u, "http://"), rec: rec, alerts: []alertmanager.Alert{alert("f-billing", "billing"), alert("f-billing-2", "billing")}}
		},
	})
	sys := srv.URL + "/api/v1/systems/"
	expect := func(what string, want ...string) {
		t.Helper()
		got := rec.take()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s asked %v, want %v", what, got, want)
		}
	}
	get(t, sys+"billing/rules/state")
	expect("billing rule states", "prom-b")
	get(t, sys+"shop/rules/state")
	expect("shop rule states", "prom-main")
	get(t, sys+"billing/discovery")
	if got := rec.take(); len(got) == 0 || got[0] != "prom-b" {
		t.Errorf("billing discovery asked %v", got)
	}
	do(t, "POST", sys+"billing/backtest", `{"expr":"up","range":"1h"}`)
	expect("billing backtest", "prom-b")
	_, body := get(t, sys+"billing/alerts")
	expect("billing alerts", "am-b")
	if strings.Count(body, `"name":"KamertonErrorRateHigh"`) != 2 {
		t.Errorf("billing alerts: %s", body)
	}
	get(t, sys+"shop/alerts")
	expect("shop alerts", "am-main")

	// All alerts: every Alertmanager in use, merged without duplicates.
	_, body = get(t, srv.URL+"/api/v1/alerts")
	if got := rec.take(); len(got) != 2 {
		t.Errorf("all alerts must ask both Alertmanagers: %v", got)
	}
	if n := strings.Count(body, `"name":"KamertonErrorRateHigh"`); n != 3 {
		t.Errorf("want 3 distinct alerts (shop, billing, billing-2), got %d: %s", n, body)
	}

	// The system list counts each system from its own Alertmanager.
	var list systemList
	_, body = get(t, srv.URL+"/api/v1/systems")
	decode(t, body, &list)
	for _, s := range list.Systems {
		want := map[string]int{"shop": 1, "billing": 2}[s.ID]
		if s.Alerts == nil || s.Alerts.Critical != want {
			t.Errorf("%s: %s", s.ID, body)
		}
	}
	// The legacy stack endpoint reports a chosen set.
	if _, body := get(t, srv.URL+"/api/v1/stack?set=team-b"); !strings.Contains(body, `"set":"team-b"`) || !strings.Contains(body, `"url":"http://prom-b"`) {
		t.Errorf("stack?set=: %s", body)
	}
}

func TestMergeAlerts(t *testing.T) {
	early := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	a1 := alert("f1", "shop")
	a1.StartsAt = early.Add(time.Minute)
	a1dup := alert("f1", "shop")
	a1dup.StartsAt = early
	noFP := alert("", "billing")
	noFPdup := alert("", "billing")
	got := mergeAlerts([]alertmanager.Alert{a1, noFP}, []alertmanager.Alert{a1dup, noFPdup, alert("f2", "shop")})
	if len(got) != 3 {
		t.Fatalf("merged: %+v", got)
	}
	if !got[0].StartsAt.Equal(early) {
		t.Errorf("the earliest start is kept: %v", got[0].StartsAt)
	}
	if got[1].Labels["kamerton_system"] != "billing" || got[2].Fingerprint != "f2" {
		t.Errorf("order of first appearance: %+v", got)
	}
}

func TestSameAddress(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"http://prometheus-operated.monitoring:9090", "http://prometheus-operated.monitoring.svc:9090", true},
		{"http://prometheus-operated.monitoring.svc.cluster.local:9090/", "http://prometheus-operated.monitoring.svc:9090", true},
		{"http://prometheus-operated.monitoring.svc:9090", "http://prometheus-operated.monitoring.svc:9090", true},
		{"http://prometheus-operated.team-a:9090", "http://prometheus-operated.monitoring.svc:9090", false},
		{"http://kamerton-kps-prometheus:9090", "http://kamerton-kps-prometheus.kamerton.svc:9090", true},
		{"http://kamerton-grafana", "http://kamerton-grafana.kamerton.svc:80", true},
		{"http://kamerton-grafana", "http://kamerton-grafana.other.svc:80", false},
	} {
		if got := sameAddress(tc.a, tc.b, "kamerton"); got != tc.same {
			t.Errorf("sameAddress(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

func ptr[T any](v T) *T { return &v }
