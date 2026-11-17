// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package stack

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func flags() Flags {
	return Flags{
		URLs: map[string]string{
			KindPrometheus: "http://kamerton-kps-prometheus:9090", KindAlertmanager: "http://kamerton-kps-alertmanager:9093",
			KindGrafana: "http://grafana.monitoring", KindLoki: "", KindTempo: "http://kamerton-tempo:3200", KindOTelCollector: "",
		},
		External: map[string]bool{KindGrafana: true},
	}
}

func ptr[T any](v T) *T { return &v }

func program(t *testing.T, r *Resolved, kind string) Program {
	t.Helper()
	for _, p := range r.Programs {
		if p.Kind == kind {
			return p
		}
	}
	t.Fatalf("no %s in %+v", kind, r.Programs)
	return Program{}
}

// exerciseStore runs the same scenario against every store implementation.
func exerciseStore(t *testing.T, store StateStore) {
	ctx := context.Background()
	s := &Sets{Store: store, Flags: flags()}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != BuiltinID || list[0].Name != BuiltinName || !list[0].Builtin {
		t.Fatalf("fresh store must have only the built-in set: %+v", list)
	}
	b := &list[0]
	if p := program(t, b, KindPrometheus); p.URL != "http://kamerton-kps-prometheus:9090" || p.Origin != OriginBundled || p.Overridden {
		t.Errorf("prometheus from flags: %+v", p)
	}
	if p := program(t, b, KindGrafana); p.Origin != OriginOwn {
		t.Errorf("external grafana must be own: %+v", p)
	}
	if p := program(t, b, KindLoki); p.Origin != OriginNone || p.URL != "" {
		t.Errorf("loki not connected: %+v", p)
	}

	// Override one address of the built-in set, then reset it.
	b, err = s.Update(ctx, BuiltinID, Input{URLs: map[string]string{KindPrometheus: " http://prom.team-a:9090/ "}})
	if err != nil {
		t.Fatal(err)
	}
	if p := program(t, b, KindPrometheus); p.URL != "http://prom.team-a:9090" || p.Origin != OriginOwn || !p.Overridden || p.FlagURL != "http://kamerton-kps-prometheus:9090" {
		t.Errorf("override: %+v", p)
	}
	b, err = s.ResetAddress(ctx, BuiltinID, KindPrometheus)
	if err != nil {
		t.Fatal(err)
	}
	if p := program(t, b, KindPrometheus); p.URL != "http://kamerton-kps-prometheus:9090" || p.Overridden || p.Origin != OriginBundled {
		t.Errorf("reset: %+v", p)
	}
	// Disconnecting a flag program is an override to "".
	b, _ = s.Update(ctx, BuiltinID, Input{URLs: map[string]string{KindTempo: ""}})
	if p := program(t, b, KindTempo); p.URL != "" || !p.Overridden || p.Origin != OriginNone {
		t.Errorf("disconnect: %+v", p)
	}

	// A second set, ID derived from a Russian name.
	team, err := s.Create(ctx, Input{Name: ptr("Команда А"), URLs: map[string]string{KindPrometheus: "http://prometheus-operated.team-a:9090", KindAlertmanager: "http://kamerton-kps-alertmanager:9093"},
		RuleLabels: &map[string]string{"release": "team-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if team.ID != "komanda-a" || team.Name != "Команда А" || team.Builtin {
		t.Errorf("created: %+v", team)
	}
	if p := program(t, team, KindAlertmanager); p.Origin != OriginBundled {
		t.Errorf("a second set using the bundled Alertmanager shows it as bundled: %+v", p)
	}
	if p := program(t, team, KindGrafana); p.Origin != OriginNone {
		t.Errorf("unset program in a new set: %+v", p)
	}
	if team.RuleLabels["release"] != "team-a" {
		t.Errorf("labels: %+v", team.RuleLabels)
	}
	// Same name again: refused; the derived ID would be unique anyway.
	if _, err := s.Create(ctx, Input{Name: ptr("команда а")}); !isInput(err) {
		t.Errorf("duplicate name: %v", err)
	}
	// A different name with the same slug gets a unique ID.
	other, err := s.Create(ctx, Input{Name: ptr("Команда А!")})
	if err != nil || other.ID != "komanda-a-2" {
		t.Errorf("unique ID: %+v %v", other, err)
	}
	if err := s.Delete(ctx, "komanda-a-2", nil); err != nil {
		t.Error(err)
	}

	// Rename keeps the ID.
	team, err = s.Update(ctx, "komanda-a", Input{Name: ptr("Team A")})
	if err != nil || team.ID != "komanda-a" || team.Name != "Team A" {
		t.Fatalf("rename: %+v %v", team, err)
	}

	// Resolve: unknown falls back to the built-in set.
	r, unknown, err := s.Resolve(ctx, "nope")
	if err != nil || !unknown || r.ID != BuiltinID {
		t.Errorf("unknown set: %+v %v %v", r.ID, unknown, err)
	}
	r, unknown, _ = s.Resolve(ctx, "")
	if unknown || r.ID != BuiltinID {
		t.Errorf("empty is built-in: %+v %v", r.ID, unknown)
	}
	r, unknown, _ = s.Resolve(ctx, "komanda-a")
	if unknown || r.URL(KindPrometheus) != "http://prometheus-operated.team-a:9090" {
		t.Errorf("resolve team: %+v", r)
	}

	// Delete: the built-in set and a used set cannot be deleted.
	if err := s.Delete(ctx, BuiltinID, nil); !errors.Is(err, ErrBuiltin) {
		t.Errorf("delete built-in: %v", err)
	}
	var inUse *InUseError
	if err := s.Delete(ctx, "komanda-a", []string{"shop"}); !errors.As(err, &inUse) || inUse.Systems[0] != "shop" {
		t.Errorf("delete used: %v", err)
	}
	if err := s.Delete(ctx, "komanda-a", nil); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := s.Get(ctx, "komanda-a"); !errors.Is(err, ErrUnknownSet) {
		t.Errorf("deleted set still there: %v", err)
	}
	list, _ = s.List(ctx)
	if len(list) != 1 {
		t.Errorf("after delete: %+v", list)
	}
}

func isInput(err error) bool {
	var in *InputError
	return errors.As(err, &in)
}

func TestSetsMemoryStore(t *testing.T) { exerciseStore(t, &MemoryStateStore{}) }

func TestSetsFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".kamerton-stacks.json")
	exerciseStore(t, &FileStateStore{Path: path})
	// A new store on the same file sees the state (survives restarts).
	s := &Sets{Store: &FileStateStore{Path: path}, Flags: flags()}
	b, err := s.Get(context.Background(), BuiltinID)
	if err != nil {
		t.Fatal(err)
	}
	if p := program(t, b, KindTempo); p.URL != "" || !p.Overridden {
		t.Errorf("state not persisted: %+v", p)
	}
}

func TestSetsConfigMapStore(t *testing.T) {
	sc := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sc); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sc).Build()
	exerciseStore(t, &ConfigMapStateStore{Client: c, Namespace: "kamerton", Name: DefaultConfigMapName})
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: DefaultConfigMapName}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data[StateConfigMapKey] == "" || cm.Labels["app.kubernetes.io/managed-by"] != "kamerton" {
		t.Errorf("ConfigMap: %+v", cm)
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "",
		"  ":                          "",
		"http://prom:9090/":           "http://prom:9090",
		"https://grafana.example.com": "https://grafana.example.com",
		"http://loki:3100/prefix/":    "http://loki:3100/prefix",
	} {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"prom:9090", "ftp://prom", "file:///etc/passwd", "http://user:secret@prom:9090", "http://prom:9090/?x=1", "javascript:alert(1)", "http://"} {
		if _, err := NormalizeURL(bad); !isInput(err) {
			t.Errorf("NormalizeURL(%q) must be refused, got %v", bad, err)
		}
	}
}

func TestCheckLabels(t *testing.T) {
	if l, err := CheckLabels(map[string]string{"release": "kps", " team ": " a ", "": "x"}, "PrometheusRule"); err != nil || l["release"] != "kps" || l["team"] != "a" || len(l) != 2 {
		t.Errorf("good labels: %v %v", l, err)
	}
	// Repeating Kamerton's own label with its value is fine (found selectors do).
	if _, err := CheckLabels(map[string]string{"kamerton.dev/managed": "true"}, "AlertmanagerConfig"); err != nil {
		t.Errorf("own label with own value: %v", err)
	}
	for _, bad := range []map[string]string{
		{"kamerton.dev/managed": "false"},
		{"kamerton.dev/system": "x"},
		{"bad key!": "x"},
		{"ok": "bad value with spaces"},
	} {
		if _, err := CheckLabels(bad, "PrometheusRule"); !isInput(err) {
			t.Errorf("CheckLabels(%v) must be refused, got %v", bad, err)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Команда А": "komanda-a", "Team B / prod": "team-b-prod", "!!!": "set", "Шопинг 2": "shoping-2"} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
	st := &State{Sets: map[string]*Set{"main-2": {}}}
	if got := uniqueID(st, "main"); got != "main-3" {
		t.Errorf("uniqueID must skip the built-in ID and taken ones: %s", got)
	}
}
