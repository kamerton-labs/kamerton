// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package stack

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func fakeProgram(kind string, ready bool) http.Handler {
	mux := http.NewServeMux()
	status := func(w http.ResponseWriter) {
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}
	switch kind {
	case KindPrometheus:
		mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
			status(w)
			_, _ = io.WriteString(w, "Prometheus Server is Ready.\n")
		})
		mux.HandleFunc("/api/v1/status/buildinfo", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"status":"success","data":{"version":"3.5.0","revision":"x","goVersion":"go1.24"}}`)
		})
	case KindAlertmanager:
		mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) { status(w); _, _ = io.WriteString(w, "OK") })
		mux.HandleFunc("/api/v2/status", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"cluster":{"status":"ready"},"versionInfo":{"version":"0.28.1"},"config":{"original":""},"uptime":"2026-11-17T00:00:00Z"}`)
		})
	case KindGrafana:
		mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
			db := "ok"
			if !ready {
				db = "failing"
			}
			_, _ = io.WriteString(w, `{"commit":"x","database":"`+db+`","version":"12.1.0"}`)
		})
	case KindLoki:
		mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { status(w); _, _ = io.WriteString(w, "ready\n") })
		mux.HandleFunc("/loki/api/v1/status/buildinfo", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"version":"3.7.10","revision":"c792f585","branch":"release-3.7.x","goVersion":""}`)
		})
	case KindTempo:
		mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { status(w); _, _ = io.WriteString(w, "ready\n") })
		mux.HandleFunc("/api/status/buildinfo", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"version":"2.8.2","revision":"x","branch":"HEAD","goVersion":"go1.24.1"}`)
		})
	case KindOTelCollector:
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"status":"Server available","upSince":"2026-11-17T00:00:00Z","uptime":"1h"}`)
		})
	case "web":
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	}
	return mux
}

// routedClient fails to connect to unknown hosts, like a service with nothing
// behind it.
func routedClient(t *testing.T, routes map[string]http.Handler) *http.Client {
	t.Helper()
	addrs := map[string]string{}
	for host, h := range routes {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		addrs[host] = srv.Listener.Addr().String()
	}
	var d net.Dialer
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if a, ok := addrs[addr]; ok {
			return d.DialContext(ctx, network, a)
		}
		return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host", Name: addr, IsNotFound: true}}
	}}}
}

func TestCheck(t *testing.T) {
	routes := map[string]http.Handler{"web.apps:80": fakeProgram("web", true), "locked.apps:9090": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })}
	for _, k := range Kinds {
		routes[k+".ok:80"] = fakeProgram(k, true)
		routes[k+".down:80"] = fakeProgram(k, false)
	}
	c := routedClient(t, routes)
	ctx := context.Background()
	versions := map[string]string{KindPrometheus: "3.5.0", KindAlertmanager: "0.28.1", KindGrafana: "12.1.0", KindLoki: "3.7.10", KindTempo: "2.8.2", KindOTelCollector: ""}
	for _, k := range Kinds {
		r := Check(ctx, c, k, "http://"+k+".ok:80/")
		if r.State != StateOK || !r.Identified || !r.Healthy || r.Version != versions[k] || r.URL != "http://"+k+".ok:80" {
			t.Errorf("%s ok: %+v", k, r)
		}
		r = Check(ctx, c, k, "http://"+k+".down:80")
		if k == KindOTelCollector {
			continue // the collector's health answer is its identity
		}
		if r.State != StateNotReady || !r.Identified || r.Healthy {
			t.Errorf("%s not ready: %+v", k, r)
		}
		for _, other := range Kinds {
			if other == k {
				continue
			}
			if r := Check(ctx, c, other, "http://"+k+".ok:80"); r.Identified {
				t.Errorf("%s at a %s address must not be identified: %+v", other, k, r)
			}
		}
	}
	if r := Check(ctx, c, KindPrometheus, "http://web.apps:80"); r.State != StateWrongProgram || !strings.Contains(r.Message, "it is not Prometheus") {
		t.Errorf("wrong program: %+v", r)
	}
	if r := Check(ctx, c, KindPrometheus, "http://locked.apps:9090"); r.State != StateNoAccess {
		t.Errorf("no access: %+v", r)
	}
	if r := Check(ctx, c, KindPrometheus, "http://nothing.here:9090"); r.State != StateNoAnswer || r.Healthy {
		t.Errorf("no answer: %+v", r)
	}
	for _, bad := range []string{"", "prom:9090", "ftp://x", "http://u:p@x"} {
		if r := Check(ctx, c, KindPrometheus, bad); r.State != StateBadURL {
			t.Errorf("bad URL %q: %+v", bad, r)
		}
	}
}

func TestCheckRedirectsAndVersion(t *testing.T) {
	var foreignHit bool
	redirect := func(to string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, to+r.URL.Path, http.StatusFound)
		})
	}
	routes := map[string]http.Handler{
		// A program behind a path prefix on the same host is followed.
		"prefixed.ok:80": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := strings.CutPrefix(r.URL.Path, "/prom"); ok {
				r.URL.Path = p
				fakeProgram(KindPrometheus, true).ServeHTTP(w, r)
				return
			}
			http.Redirect(w, r, "/prom"+r.URL.Path, http.StatusFound)
		}),
		// Another host is not.
		"bounce.apps:80": redirect("http://prometheus.ok:80"),
		"prometheus.ok:80": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			foreignHit = true
			fakeProgram(KindPrometheus, true).ServeHTTP(w, r)
		}),
		"long.version:80": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/status/buildinfo" {
				_, _ = io.WriteString(w, `{"status":"success","data":{"version":"1.0\n`+strings.Repeat("x", 500)+`"}}`)
				return
			}
			w.WriteHeader(http.StatusOK)
		}),
	}
	c := routedClient(t, routes)
	ctx := context.Background()
	if r := Check(ctx, c, KindPrometheus, "http://prefixed.ok:80"); r.State != StateOK {
		t.Errorf("same-host redirect: %+v", r)
	}
	if r := Check(ctx, c, KindPrometheus, "http://bounce.apps:80"); r.State != StateWrongProgram || foreignHit {
		t.Errorf("redirect to another host must not be followed (hit %v): %+v", foreignHit, r)
	}
	if r := Check(ctx, c, KindPrometheus, "http://long.version:80"); r.State != StateOK || len(r.Version) != maxVersionLen || strings.ContainsRune(r.Version, '\n') {
		t.Errorf("version must be short and printable: %+v", r)
	}
}

func svc(ns, name string, lbls map[string]string, selector map[string]string, ports ...int32) *corev1.Service {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbls}, Spec: corev1.ServiceSpec{Selector: selector}}
	for _, p := range ports {
		s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{Port: p, Protocol: corev1.ProtocolTCP})
	}
	return s
}

var (
	gvkPrometheus   = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "Prometheus"}
	gvkAlertmanager = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "Alertmanager"}
)

func cr(gvk schema.GroupVersionKind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func scheme(t *testing.T, withCRDs bool) *runtime.Scheme {
	t.Helper()
	sc := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sc); err != nil {
		t.Fatal(err)
	}
	if withCRDs {
		sc.AddKnownTypeWithName(gvkPrometheus, &unstructured.Unstructured{})
		sc.AddKnownTypeWithName(gvkPrometheusList, &unstructured.UnstructuredList{})
		sc.AddKnownTypeWithName(gvkAlertmanager, &unstructured.Unstructured{})
		sc.AddKnownTypeWithName(gvkAlertmanagerList, &unstructured.UnstructuredList{})
	}
	return sc
}

func services() []client.Object {
	return []client.Object{
		svc("monitoring", "prometheus-operated", nil, map[string]string{"app.kubernetes.io/name": "prometheus"}, 9090),
		svc("monitoring", "kps-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, map[string]string{"operator.prometheus.io/name": "kps"}, 9090, 8080),
		svc("monitoring", "kps-operator", map[string]string{"app.kubernetes.io/name": "kube-prometheus-stack-operator"}, nil, 443),
		svc("monitoring", "kps-prometheus-node-exporter", map[string]string{"app.kubernetes.io/name": "prometheus-node-exporter"}, nil, 9100),
		svc("monitoring", "alertmanager-operated", nil, nil, 9093, 9094),
		svc("monitoring", "grafana", map[string]string{"app.kubernetes.io/name": "grafana"}, nil, 80),
		svc("logging", "loki", map[string]string{"app.kubernetes.io/name": "loki"}, nil, 3100, 9095),
		svc("logging", "loki-headless", map[string]string{"app.kubernetes.io/name": "loki"}, nil, 3100),
		svc("logging", "loki-memberlist", map[string]string{"app.kubernetes.io/name": "loki"}, nil, 7946),
		svc("tracing", "tempo", map[string]string{"app.kubernetes.io/name": "tempo"}, nil, 3200, 4317),
		svc("otel", "collector", map[string]string{"app.kubernetes.io/name": "opentelemetry-collector"}, nil, 4317, 4318, 13133),
		svc("otel", "agent", map[string]string{"app.kubernetes.io/name": "opentelemetry-collector"}, nil, 4317),
		svc("apps", "my-prometheus-dashboard", nil, nil, 9090),
		svc("apps", "web", nil, nil, 9093),
		svc("apps", "shop", nil, nil, 8080),
		svc("team-a", "prometheus", map[string]string{"app.kubernetes.io/name": "prometheus"}, nil, 9090),
	}
}

func discoveryRoutes() map[string]http.Handler {
	return map[string]http.Handler{
		"prometheus-operated.monitoring.svc:9090":   fakeProgram(KindPrometheus, true),
		"kps-prometheus.monitoring.svc:9090":        fakeProgram(KindPrometheus, true),
		"alertmanager-operated.monitoring.svc:9093": fakeProgram(KindAlertmanager, true),
		"grafana.monitoring.svc:80":                 fakeProgram(KindGrafana, true),
		"loki.logging.svc:3100":                     fakeProgram(KindLoki, true),
		"tempo.tracing.svc:3200":                    fakeProgram(KindTempo, false),
		"collector.otel.svc:13133":                  fakeProgram(KindOTelCollector, true),
		"my-prometheus-dashboard.apps.svc:9090":     fakeProgram("web", true),
		// apps/web and team-a/prometheus: nothing answers.
	}
}

func TestDiscover(t *testing.T) {
	objs := append(services(),
		cr(gvkPrometheus, "monitoring", "kps", map[string]any{"ruleSelector": map[string]any{}, "ruleNamespaceSelector": map[string]any{}}),
		cr(gvkPrometheus, "team-a", "team", map[string]any{"ruleSelector": map[string]any{"matchLabels": map[string]any{"team": "a"}}}),
		cr(gvkPrometheus, "team-b", "all", map[string]any{"ruleSelector": map[string]any{}}),
		cr(gvkAlertmanager, "monitoring", "kps", map[string]any{
			"alertmanagerConfigSelector":          map[string]any{"matchLabels": map[string]any{"kamerton.dev/managed": "true"}},
			"alertmanagerConfigNamespaceSelector": map[string]any{},
		}),
	)
	c := fake.NewClientBuilder().WithScheme(scheme(t, true)).WithObjects(objs...).Build()
	d := &Discoverer{Client: c, HTTP: routedClient(t, discoveryRoutes()), Concurrency: 3}
	res, err := d.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Found{}
	for _, f := range res.Found {
		got[f.Namespace+"/"+f.Service] = f
	}
	want := map[string]struct {
		kind, state, version string
	}{
		"monitoring/prometheus-operated":   {KindPrometheus, StateOK, "3.5.0"},
		"monitoring/kps-prometheus":        {KindPrometheus, StateOK, "3.5.0"},
		"monitoring/alertmanager-operated": {KindAlertmanager, StateOK, "0.28.1"},
		"monitoring/grafana":               {KindGrafana, StateOK, "12.1.0"},
		"logging/loki":                     {KindLoki, StateOK, "3.7.10"},
		"tracing/tempo":                    {KindTempo, StateNotReady, "2.8.2"},
		"otel/collector":                   {KindOTelCollector, StateOK, ""},
		"otel/agent":                       {KindOTelCollector, StateNoAnswer, ""},
		"team-a/prometheus":                {KindPrometheus, StateNoAnswer, ""},
	}
	for key, w := range want {
		f, ok := got[key]
		if !ok {
			t.Errorf("%s not found", key)
			continue
		}
		if f.Kind != w.kind || f.State != w.state || f.Version != w.version {
			t.Errorf("%s: %+v, want %+v", key, f, w)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected candidate %s: %+v", key, got[key])
		}
	}
	if f := got["monitoring/grafana"]; f.URL != "http://grafana.monitoring.svc:80" || !f.Healthy {
		t.Errorf("grafana address: %+v", f)
	}
	if f := got["otel/agent"]; !strings.Contains(f.Message, "13133") {
		t.Errorf("collector without health port must say so: %+v", f)
	}
	if res.Found[0].Kind != KindPrometheus || res.Found[len(res.Found)-1].Kind != KindOTelCollector {
		t.Errorf("found must be ordered by kind: %+v", res.Found)
	}

	if !res.Operator.Available || len(res.Operator.Prometheuses) != 3 || len(res.Operator.Alertmanagers) != 1 {
		t.Fatalf("operator: %+v", res.Operator)
	}
	op := got["monitoring/kps-prometheus"].Operator
	if op == nil || op.Name != "kps" || !op.SelectsAll || op.Namespaces != "from all namespaces" {
		t.Errorf("kps-prometheus → Prometheus kps: %+v", op)
	}
	if op := got["monitoring/alertmanager-operated"].Operator; op == nil || op.Labels["kamerton.dev/managed"] != "true" || op.SelectsAll {
		t.Errorf("alertmanager → its selector labels: %+v", op)
	}
	team := res.Operator.Prometheuses[1]
	if team.Name != "team" || team.Labels["team"] != "a" || team.SelectsAll || !strings.Contains(team.Namespaces, "only from its own") {
		t.Errorf("team Prometheus: %+v", team)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "monitoring/kps, team-b/all") {
		t.Errorf("two Prometheus selecting everything must be warned about: %v", res.Warnings)
	}

	set := &Resolved{ID: "team-a", Programs: []Program{{Kind: KindPrometheus, URL: "http://prometheus-operated.team-a.svc:9090"}, {Kind: KindAlertmanager, URL: "http://alertmanager-operated.monitoring:9093"}},
		RuleLabels: map[string]string{"team": "a"}}
	w := SetWarnings(set, &res.Operator)
	if len(w) != 1 || !strings.Contains(w[0], "monitoring/kps, team-a/team, team-b/all") {
		t.Errorf("rules picked by three Prometheus: %v", w)
	}
	alone := Operator{Available: true, Prometheuses: []OperatorInstance{res.Operator.Prometheuses[1]}, Alertmanagers: res.Operator.Alertmanagers}
	if w := SetWarnings(set, &alone); len(w) != 0 {
		t.Errorf("exactly one Prometheus picks the rules: %v", w)
	}
	set.RuleLabels = nil
	if w := SetWarnings(set, &alone); len(w) != 1 || !strings.Contains(w[0], "No Prometheus") {
		t.Errorf("no Prometheus picks the rules: %v", w)
	}
}

func TestDiscoverDegrades(t *testing.T) {
	ctx := context.Background()
	httpc := routedClient(t, discoveryRoutes())

	// The REST mapper knows no Prometheus Operator kinds: services are still found.
	noCRD := interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if u, ok := list.(*unstructured.UnstructuredList); ok {
			gvk := u.GroupVersionKind()
			return &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
		}
		return cl.List(ctx, list, opts...)
	}}
	c := fake.NewClientBuilder().WithScheme(scheme(t, false)).WithObjects(services()...).WithInterceptorFuncs(noCRD).Build()
	res, err := (&Discoverer{Client: c, HTTP: httpc}).Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Operator.Available || !strings.Contains(res.Operator.Reason, "not found") || len(res.Found) == 0 {
		t.Errorf("without CRDs: %+v", res.Operator)
	}
	for _, f := range res.Found {
		if f.Operator != nil {
			t.Errorf("no operator data expected: %+v", f)
		}
	}

	forbidCR := interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*unstructured.UnstructuredList); ok {
			return apierrors.NewForbidden(schema.GroupResource{Group: "monitoring.coreos.com", Resource: "prometheuses"}, "", nil)
		}
		return cl.List(ctx, list, opts...)
	}}
	c = fake.NewClientBuilder().WithScheme(scheme(t, true)).WithObjects(services()...).WithInterceptorFuncs(forbidCR).Build()
	res, err = (&Discoverer{Client: c, HTTP: httpc}).Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Operator.Available || !strings.Contains(res.Operator.Reason, "Not allowed") || len(res.Found) == 0 {
		t.Errorf("forbidden CRs: %+v", res.Operator)
	}

	forbidSvc := interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", nil)
	}}
	c = fake.NewClientBuilder().WithScheme(scheme(t, true)).WithInterceptorFuncs(forbidSvc).Build()
	_, err = (&Discoverer{Client: c, HTTP: httpc}).Discover(ctx)
	var de *DiscoveryError
	if err == nil || !errorsAs(err, &de) || !strings.Contains(de.Message, "list services") {
		t.Errorf("forbidden services: %v", err)
	}
}

func errorsAs(err error, target **DiscoveryError) bool {
	e, ok := err.(*DiscoveryError)
	if ok {
		*target = e
	}
	return ok
}

func TestRecognize(t *testing.T) {
	for _, tc := range []struct {
		s      *corev1.Service
		kind   string
		byPort bool
	}{
		{svc("m", "prometheus-operated", nil, nil, 9090), KindPrometheus, false},
		{svc("m", "alertmanager-operated", nil, nil, 9093), KindAlertmanager, false},
		{svc("m", "x", map[string]string{"app.kubernetes.io/name": "alertmanager"}, nil, 9093), KindAlertmanager, false},
		{svc("m", "x", map[string]string{"app": "kube-prometheus-stack-alertmanager"}, nil, 9093), KindAlertmanager, false},
		{svc("m", "x", map[string]string{"app.kubernetes.io/name": "opentelemetry-collector"}, nil, 4317), KindOTelCollector, false},
		{svc("m", "otel-collector", nil, nil, 4317), KindOTelCollector, false},
		{svc("m", "x", map[string]string{"k8s-app": "grafana"}, nil, 80), KindGrafana, false},
		{svc("m", "loki-gateway", nil, nil, 80), "", false},
		{svc("m", "tempo-query-frontend", nil, nil, 3200), KindTempo, false},
		{svc("m", "x", map[string]string{"app.kubernetes.io/name": "kube-prometheus-stack-operator"}, nil, 443), "", false},
		{svc("m", "grafana-agent", nil, nil, 80), "", false},
		{svc("m", "loki-headless", map[string]string{"app.kubernetes.io/name": "loki"}, nil, 3100), "", false},
		{svc("m", "something", nil, nil, 3100), KindLoki, true},
		{svc("m", "something", nil, nil, 8080), "", false},
	} {
		kind, byPort := Recognize(tc.s)
		if kind != tc.kind || byPort != tc.byPort {
			t.Errorf("%s %v: got %q/%v, want %q/%v", tc.s.Name, tc.s.Labels, kind, byPort, tc.kind, tc.byPort)
		}
	}
}
