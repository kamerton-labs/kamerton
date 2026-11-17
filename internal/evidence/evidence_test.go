// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

var now = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

func ago(d time.Duration) metav1.Time { return metav1.NewTime(now.Add(-d)) }

func shop() *v1alpha1.SystemDescription {
	return &v1alpha1.SystemDescription{
		ObjectMeta: metav1.ObjectMeta{Name: "shop"},
		Spec: v1alpha1.SystemDescriptionSpec{
			Defaults: &v1alpha1.Defaults{Namespace: "shop", ExcludeOperations: []string{"noisy"}},
			Services: []v1alpha1.Service{
				{Name: "payment", Type: v1alpha1.ServiceTypeGRPC, TelemetryName: "payment-svc"},
				{Name: "cache", Type: v1alpha1.ServiceTypeDatabase, Workload: &v1alpha1.Workload{PodRegex: "valkey-.*"}},
			},
		},
	}
}

func owner(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, UID: types.UID("u-" + name)}}
}

func rs(name string, revision string, created time.Duration, image string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, CreationTimestamp: ago(created),
			Annotations: map[string]string{revisionAnnotation: revision}, OwnerReferences: owner("Deployment", "payment")},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar", Image: "envoy:1.30"}, {Name: "payment", Image: image},
		}}}},
	}
}

func kube(t *testing.T, objs ...client.Object) client.Reader {
	t.Helper()
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build()
}

func paymentCluster() []client.Object {
	ready := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}
	notReady := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse}
	return []client.Object{
		rs("payment-old", "4", 48*time.Hour, "ghcr.io/shop/payment:v1.4.2"),
		rs("payment-new", "5", 16*time.Minute, "ghcr.io/shop/payment:v1.4.3"),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "payment-new-abcde", CreationTimestamp: ago(15 * time.Minute), OwnerReferences: owner("ReplicaSet", "payment-new")},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{notReady}, ContainerStatuses: []corev1.ContainerStatus{{
				Name: "payment", RestartCount: 3,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: ago(2 * time.Minute)}},
			}}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "payment-new-fghij", CreationTimestamp: ago(14 * time.Minute), OwnerReferences: owner("ReplicaSet", "payment-new")},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{ready}},
		},
		// An old restart of a long-lived pod of another service, and a pod
		// of another namespace with a matching name: neither counts.
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "payments-api-7d9f8-xyzab", CreationTimestamp: ago(72 * time.Hour)},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{notReady}, ContainerStatuses: []corev1.ContainerStatus{{
				Name: "api", RestartCount: 9, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", FinishedAt: ago(time.Minute)}},
			}}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "payment-new-zzzzz"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "e1"}, Type: corev1.EventTypeWarning, Reason: "BackOff",
			Message: "Back-off restarting failed container payment", Count: 5, LastTimestamp: ago(time.Minute),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "payment-new-abcde"}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "e2"}, Type: corev1.EventTypeWarning, Reason: "FailedScheduling",
			Message: "old", Count: 1, LastTimestamp: ago(3 * time.Hour),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "payment-new-abcde"}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "e3"}, Type: corev1.EventTypeNormal, Reason: "Pulled",
			Message: "pulled", Count: 1, LastTimestamp: ago(time.Minute),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "payment-new-abcde"}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "e4"}, Type: corev1.EventTypeWarning, Reason: "Unhealthy",
			Message: "Readiness probe failed", Count: 2, LastTimestamp: ago(4 * time.Minute),
			InvolvedObject: corev1.ObjectReference{Kind: "ReplicaSet", Name: "payment-new"}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "e5"}, Type: corev1.EventTypeWarning, Reason: "BackOff",
			Message: "elsewhere", Count: 50, LastTimestamp: ago(time.Minute),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "payments-api-7d9f8-xyzab"}},
	}
}

func TestWorkload(t *testing.T) {
	c := &Collector{Kube: kube(t, paymentCluster()...)}
	sd := shop()
	got, err := c.Workload(context.Background(), sd, &sd.Spec.Services[0], nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rollouts) != 1 || got.Rollouts[0].Workload != "payment" || got.Rollouts[0].OldImage != "v1.4.2" || got.Rollouts[0].NewImage != "v1.4.3" ||
		!got.Rollouts[0].At.Equal(now.Add(-16*time.Minute)) {
		t.Errorf("rollouts = %+v", got.Rollouts)
	}
	if len(got.Restarts) != 1 {
		t.Fatalf("restarts = %+v", got.Restarts)
	}
	r := got.Restarts[0]
	if r.Container != "payment" || r.Count != 3 || r.Reason != "OOMKilled" || r.ExitCode != 137 || !r.CrashLoop {
		t.Errorf("restart = %+v", r)
	}
	if got.Pods != 2 || got.NotReady != 1 {
		t.Errorf("pods %d, not ready %d", got.Pods, got.NotReady)
	}
	if len(got.Warnings) != 2 || got.Warnings[0].Reason != "BackOff" || got.Warnings[0].Count != 5 || got.Warnings[1].Reason != "Unhealthy" {
		t.Errorf("warnings = %+v", got.Warnings)
	}
}

func TestWorkloadQuiet(t *testing.T) {
	// A long-running pod without changes: nothing to report.
	objs := []client.Object{
		rs("payment-old", "4", 48*time.Hour, "ghcr.io/shop/payment:v1.4.2"),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "payment-old-abcde", CreationTimestamp: ago(40 * time.Hour), OwnerReferences: owner("ReplicaSet", "payment-old")},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "payment", RestartCount: 2,
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", FinishedAt: ago(5 * time.Hour)}}}}},
		},
	}
	c := &Collector{Kube: kube(t, objs...)}
	sd := shop()
	got, err := c.Workload(context.Background(), sd, &sd.Spec.Services[0], nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Empty() {
		t.Errorf("changes = %+v", got)
	}
}

func TestWorkloadStatefulSetAndNamespaceFromMetrics(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "data", Name: "valkey"},
		Spec:       appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "valkey", Image: "valkey/valkey:8.1@sha256:0123456789abcdef0123"}}}}},
		Status:     appsv1.StatefulSetStatus{CurrentRevision: "valkey-1", UpdateRevision: "valkey-2"},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "data", Name: "valkey-0", CreationTimestamp: ago(3 * time.Minute),
			Labels: map[string]string{revisionHashLabel: "valkey-2"}, OwnerReferences: owner("StatefulSet", "valkey")},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	sd := shop()
	sd.Spec.Defaults.Namespace = "" // no namespace: found in the metrics
	prom := &fakeProm{fn: func(q string) (model.Value, error) {
		if !strings.Contains(q, `group by (namespace) (kube_pod_container_status_restarts_total{pod=~"valkey-.*"})`) {
			t.Errorf("namespace query = %s", q)
		}
		return model.Vector{{Metric: model.Metric{"namespace": "data"}, Value: 1}}, nil
	}}
	c := &Collector{Kube: kube(t, sts, pod)}
	got, err := c.Workload(context.Background(), sd, &sd.Spec.Services[1], prom, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rollouts) != 1 || got.Rollouts[0].Workload != "valkey" || got.Rollouts[0].NewImage != "0123456789ab" || got.Rollouts[0].OldImage != "" ||
		!got.Rollouts[0].At.Equal(now.Add(-3*time.Minute)) {
		t.Errorf("rollouts = %+v", got.Rollouts)
	}
}

func TestImageTag(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/shop/payment:v1.4.3":           "v1.4.3",
		"localhost:5000/payment":                "payment",
		"localhost:5000/payment:2":              "2",
		"payment@sha256:0123456789abcdef012345": "0123456789ab",
		"nginx":                                 "nginx",
	} {
		if got := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeProm struct {
	mu      sync.Mutex
	queries []string
	fn      func(q string) (model.Value, error)
}

func (f *fakeProm) Query(_ context.Context, q string, _ time.Time) (model.Value, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	return f.fn(q)
}

func (f *fakeProm) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (model.Matrix, error) {
	return nil, errors.New("not used")
}

func TestFailingOperations(t *testing.T) {
	prom := &fakeProm{fn: func(string) (model.Value, error) {
		return model.Vector{
			{Metric: model.Metric{"span_name": "oteldemo.PaymentService/Charge"}, Value: 42},
			{Metric: model.Metric{"span_name": "grpc.health"}, Value: 3},
			{Metric: model.Metric{"span_name": "rounding"}, Value: 0.2},
		}, nil
	}}
	c := &Collector{}
	sd := shop()
	ops, err := c.FailingOperations(context.Background(), prom, sd, &sd.Spec.Services[0], now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].Operation != "oteldemo.PaymentService/Charge" || ops[0].Failed != 42 {
		t.Errorf("ops = %+v", ops)
	}
	q := prom.queries[0]
	// The selection of the error-rate rules: service label value, server
	// span kinds, excluded operations, error status.
	for _, want := range []string{
		`topk(3, sum by (service_name, span_name) (increase(traces_span_metrics_calls_total{`,
		`service_name="payment-svc"`, `span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER"`,
		`span_name!~"noisy"`, `status_code="STATUS_CODE_ERROR"`, `[30m])) > 0)`,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %s: %s", want, q)
		}
	}
	// Span metrics turned off: no query.
	sd.Spec.Telemetry = &v1alpha1.Telemetry{Spans: &v1alpha1.SpanMetrics{CallsMetric: "-"}}
	if ops, err := c.FailingOperations(context.Background(), prom, sd, &sd.Spec.Services[0], now); err != nil || ops != nil || len(prom.queries) != 1 {
		t.Errorf("disabled spans: %v %v", ops, err)
	}
}

func TestLogLine(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		resp := map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{
			map[string]any{"stream": map[string]string{"service_name": "payment-svc"}, "values": [][2]string{{"1700000000000000000", "older error"}}},
			map[string]any{"stream": map[string]string{"service_name": "payment-svc"}, "values": [][2]string{{"1700000005000000000", "card declined:\n  upstream timeout " + strings.Repeat("x", 3000)}}},
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	c := &Collector{}
	sd := shop()
	line, err := c.LogLine(context.Background(), srv.URL+"/", sd, &sd.Spec.Services[0], now.Add(-10*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "card declined: upstream timeout x") || strings.Contains(line, "\n") || len(line) > maxRawLine {
		t.Errorf("line = %.80q (%d bytes)", line, len(line))
	}
	if q := got.Get("query"); q != `{service_name="payment-svc"} |~ "(?i)(error|exception|fail|fatal|panic|timeout|refused)"` {
		t.Errorf("query = %s", q)
	}
	if got.Get("limit") != "1" || got.Get("direction") != "backward" || got.Get("start") != strconv.FormatInt(now.Add(-15*time.Minute).UnixNano(), 10) {
		t.Errorf("params = %v", got)
	}

	// Loki errors are reported, not fatal.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusBadGateway) }))
	defer bad.Close()
	if _, err := c.LogLine(context.Background(), bad.URL, sd, &sd.Spec.Services[0], now, now); err == nil {
		t.Error("no error for a failing Loki")
	}
}

func TestTraceURL(t *testing.T) {
	sd := shop()
	svc := &sd.Spec.Services[0]
	if u := (&Collector{}).TraceURL(sd, svc, now, now); u != "" {
		t.Errorf("link without Grafana: %s", u)
	}
	if u := (&Collector{GrafanaURL: "ftp://grafana"}).TraceURL(sd, svc, now, now); u != "" {
		t.Errorf("link with a bad Grafana URL: %s", u)
	}
	u := (&Collector{GrafanaURL: "https://grafana.example.com/"}).TraceURL(sd, svc, now.Add(-10*time.Minute), now)
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host != "grafana.example.com" || parsed.Path != "/explore" {
		t.Fatalf("link = %s", u)
	}
	var panes map[string]struct {
		Datasource string `json:"datasource"`
		Queries    []struct {
			Query     string `json:"query"`
			QueryType string `json:"queryType"`
		} `json:"queries"`
		Range struct{ From, To string } `json:"range"`
	}
	if err := json.Unmarshal([]byte(parsed.Query().Get("panes")), &panes); err != nil {
		t.Fatal(err)
	}
	p := panes["a"]
	if p.Datasource != "tempo" || len(p.Queries) != 1 || p.Queries[0].QueryType != "traceql" ||
		p.Queries[0].Query != `{resource.service.name="payment-svc" && status=error}` || p.Range.From != strconv.FormatInt(now.Add(-15*time.Minute).UnixMilli(), 10) {
		t.Errorf("panes = %+v", p)
	}
}

func TestCollectBoundsAndSurvivesFailures(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	slowLoki := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer slowLoki.Close()
	prom := &fakeProm{fn: func(string) (model.Value, error) { return nil, errors.New("prometheus down") }}
	c := &Collector{Kube: kube(t, paymentCluster()...), Timeout: 200 * time.Millisecond, Total: time.Second, Now: func() time.Time { return now },
		GrafanaURL: "https://grafana.example.com"}
	sd := shop()
	start := time.Now()
	ev := c.Collect(context.Background(), sd, &sd.Spec.Services[0], Sources{Prom: prom, LokiURL: slowLoki.URL}, now.Add(-5*time.Minute))
	if time.Since(start) > 2*time.Second {
		t.Errorf("Collect took %s", time.Since(start))
	}
	if ev.Service != "payment" || len(ev.Changes.Rollouts) != 1 || ev.Operations != nil || ev.LogLine != "" || ev.TraceURL == "" {
		t.Errorf("evidence = %+v", ev)
	}
}
