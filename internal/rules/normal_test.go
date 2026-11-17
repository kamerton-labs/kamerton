// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"strings"
	"testing"
	"time"
)

func fullPodTarget() PodTarget {
	t := podTarget()
	t.LimitsMetric = "kube_pod_container_resource_limits"
	t.TerminatedMetric = "kube_pod_container_status_last_terminated_reason"
	t.ContainerLabel = "container"
	t.Window = 2 * 60 * 1e9
	return t
}

func contains(t *testing.T, what, s string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("%s does not contain %q:\n%s", what, w, s)
		}
	}
}

func TestOOMKilled(t *testing.T) {
	m := meta()
	m.For = 0
	m.Language = LanguageRussian
	r := OOMKilled(OOMKilledParams{Meta: m, Target: fullPodTarget()})
	mustValidate(t, r)
	if r.Alert != AlertOOMKilled || r.For != 0 {
		t.Fatalf("alert %s for %s", r.Alert, r.For)
	}
	contains(t, "expr", r.Expr,
		`sum by (namespace, pod, container) (increase(kube_pod_container_status_restarts_total{namespace="otel-demo", pod=~"checkout-.*"}[10m])) > 0`,
		`kube_pod_container_status_last_terminated_reason{namespace="otel-demo", pod=~"checkout-.*", reason="OOMKilled"}`,
		"== 1")
	contains(t, "title", r.Annotations[AnnotationTitle], "Контейнер убит из-за нехватки памяти", "контейнер")
}

func TestNearLimit(t *testing.T) {
	m := meta()
	mem := MemoryNearLimit(NearLimitParams{Meta: m, Target: fullPodTarget(), Share: 0.95})
	cpu := CPUNearLimit(NearLimitParams{Meta: m, Target: fullPodTarget(), Share: 0.9})
	mustValidate(t, mem, cpu)
	contains(t, "memory", mem.Expr, `resource="memory"`, "> 0.95", "predict_linear(", "[1h], 1800)", ">= 1",
		// a pod is compared with its limit only when every container has one
		`count by (namespace, pod) (kube_pod_container_resource_limits{namespace="otel-demo", pod=~"checkout-.*", resource="memory"}) >= on (namespace, pod) count by (namespace, pod) (kube_pod_container_status_restarts_total`)
	contains(t, "cpu", cpu.Expr, `resource="cpu"`, "rate(container_cpu_usage_seconds_total", "> 0.9")
	if strings.Contains(cpu.Expr, "predict_linear") {
		t.Error("the CPU rule has no trend condition")
	}
	// Without a restarts metric the guard is left out rather than broken.
	pt := fullPodTarget()
	pt.RestartsMetric = "-"
	r := MemoryNearLimit(NearLimitParams{Meta: m, Target: pt, Share: 0.9})
	mustValidate(t, r)
	if strings.Contains(r.Expr, "count by") {
		t.Errorf("guard without restarts metric: %s", r.Expr)
	}
}

func TestCPUAboveNormal(t *testing.T) {
	m := meta()
	m.Language = LanguageRussian
	r := CPUAboveNormal(CPUAboveNormalParams{Meta: m, Target: fullPodTarget(), Factor: 3})
	mustValidate(t, r)
	contains(t, "expr", r.Expr,
		`max by (namespace, pod) (rate(container_cpu_usage_seconds_total{namespace="otel-demo", pod=~"checkout-.*"}[2m])) > 0.5`,
		"> on (namespace) group_left ()", "3 * (",
		"[1h:1m] offset 10m", "count_over_time(", ">= 30")
	contains(t, "value", r.Annotations[AnnotationValue], "обычно", "{{ range query `")
}

func TestLatencyAboveNormal(t *testing.T) {
	m := meta()
	r := LatencyAboveNormal(LatencyAboveNormalParams{Meta: m, Target: spanTarget(), Quantile: 0.95, Factor: 5})
	mustValidate(t, r)
	contains(t, "expr", r.Expr,
		"histogram_quantile(0.95, sum by (service_name, le) (rate(traces_span_metrics_duration_milliseconds_bucket{", "[5m]))) > 100",
		"> on (service_name)\n  5 * (",
		"increase(traces_span_metrics_duration_milliseconds_bucket{", "[1h] offset 10m",
		">= 30", ">= 3")
	st := spanTarget()
	st.DurationInSeconds = true
	r = LatencyAboveNormal(LatencyAboveNormalParams{Meta: m, Target: st, Quantile: 0.95, Factor: 5})
	mustValidate(t, r)
	contains(t, "seconds", r.Expr, "> 0.1\n")
}

func TestErrorRatePerOperationMinErrors(t *testing.T) {
	r := ErrorRate(ErrorRateParams{Meta: meta(), Target: spanTarget(), Threshold: 0.05, PerOperation: true, MinErrors: 3, SingleOperationRPS: 0.05})
	mustValidate(t, r)
	contains(t, "expr", r.Expr,
		`sum by (service_name, span_name) (increase(traces_span_metrics_calls_total{service_name="checkout", span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER", span_name!~"flagd\\.evaluation\\.v2\\.Service/EventStream", status_code="STATUS_CODE_ERROR"}[5m])) >= 3`,
		"unless on (service_name)", "== 1)", "> 0.05")
	if r.Alert != AlertOperationErrorRate {
		t.Errorf("alert %s", r.Alert)
	}
	// No traffic-rate condition on the operations: a rare operation that fails
	// three times is enough.
	if strings.Contains(r.Expr, "and on (service_name, span_name)\n(\n  sum by (service_name, span_name) (rate(") {
		t.Errorf("per-operation rule still has a traffic-rate condition:\n%s", r.Expr)
	}
}

func TestLatencyPerOperation(t *testing.T) {
	m := meta()
	m.Language = LanguageRussian
	r := Latency(LatencyParams{Meta: m, Target: spanTarget(), Quantile: 0.95, ThresholdMs: 1000, MinCalls: 3, PerOperation: true})
	mustValidate(t, r)
	if r.Alert != AlertOperationLatency {
		t.Fatalf("alert %s", r.Alert)
	}
	contains(t, "expr", r.Expr,
		"sum by (service_name, span_name, le)", "> 1000",
		">= 3",
		// alerts only on operations that were faster than the threshold during the normal hour
		"[1h] offset 10m))) <= 1000", ">= 30",
		"unless on (service_name)")
	contains(t, "title", r.Annotations[AnnotationTitle], "операция {{ index $labels \"span_name\" }}")
	// Without MinRequestsPerSecond there is no rate condition, only the call count.
	if strings.Contains(r.Expr, "[5m])) > 0\n") {
		t.Errorf("rate condition with zero rate:\n%s", r.Expr)
	}
}

func queueTarget() QueueTarget {
	return QueueTarget{
		LagMetric: "kafka_consumer_group_lag_ratio", MembersMetric: "kafka_consumer_group_members", OffsetMetric: "kafka_consumer_group_offset_ratio",
		GroupLabel: "group", TopicLabel: "topic", Topic: "orders", Groups: []string{"fraud-detection", "accounting"},
	}
}

func TestQueueConsumerStopped(t *testing.T) {
	m := meta()
	m.For = time.Minute
	m.Language = LanguageRussian
	r := QueueConsumerStopped(QueueConsumerStoppedParams{Meta: m, Target: queueTarget()})
	mustValidate(t, r)
	if r.Alert != AlertQueueConsumerStopped || r.For.String() != "1m" || r.Labels[LabelSignal] != string(SignalQueueLag) {
		t.Fatalf("alert %s for %s labels %v", r.Alert, r.For, r.Labels)
	}
	want := `max by (group, topic) (kafka_consumer_group_lag_ratio{topic="orders", group=~"fraud-detection|accounting"})
and on (group, topic)
(
    label_replace(max by (group) (max_over_time(kafka_consumer_group_members{group=~"fraud-detection|accounting"}[1m])) == 0, "topic", "orders", "", "")
  or
    max by (group, topic) (min_over_time(kafka_consumer_group_lag_ratio{topic="orders", group=~"fraud-detection|accounting"}[3m])) > 0
    and on (group, topic)
    sum by (group, topic) (changes(kafka_consumer_group_offset_ratio{topic="orders", group=~"fraud-detection|accounting"}[3m])) == 0
    and on (group, topic)
    group by (group, topic) (present_over_time(kafka_consumer_group_offset_ratio{topic="orders", group=~"fraud-detection|accounting"}[2m]))
)
or on (group, topic)
0 * (
    label_replace(max by (group) (max_over_time(kafka_consumer_group_members{group=~"fraud-detection|accounting"}[1m])) == 0, "topic", "orders", "", "")
)`
	if r.Expr != want {
		t.Errorf("expr:\n%s\nwant:\n%s", r.Expr, want)
	}
	contains(t, "title", r.Annotations[AnnotationTitle], "Очередь перестали читать · orders, группа ")
	contains(t, "threshold", r.Annotations[AnnotationThreshold], "нет потребителей 1m", " или ", "стоит 3m")
	contains(t, "description", r.Annotations["description"], "no members for 1m", "did not move")

	m.Language = LanguageEnglish
	if r := QueueConsumerStopped(QueueConsumerStoppedParams{Meta: m, Target: queueTarget()}); r.Annotations[AnnotationTitle] != `Nobody reads the queue · orders, group {{ index $labels "group" }}` {
		t.Errorf("English title: %s", r.Annotations[AnnotationTitle])
	}
}

func TestQueueConsumerStoppedVariants(t *testing.T) {
	// All groups of the topic: a group belongs to it when it had lag series during the last hour.
	qt := queueTarget()
	qt.Groups = nil
	r := QueueConsumerStopped(QueueConsumerStoppedParams{Meta: meta(), Target: qt})
	mustValidate(t, r)
	contains(t, "expr", r.Expr,
		`group by (group, topic) (last_over_time(kafka_consumer_group_lag_ratio{topic="orders"}[1h]))`+"\n    and on (group)\n    "+`max by (group) (max_over_time(kafka_consumer_group_members{}[1m])) == 0`)
	if strings.Contains(r.Expr, "label_replace") {
		t.Errorf("without groups the topic comes from the lag series:\n%s", r.Expr)
	}

	// Members metric off: only the stalled offset condition, no fallback value.
	qt = queueTarget()
	qt.MembersMetric = "-"
	r = QueueConsumerStopped(QueueConsumerStoppedParams{Meta: meta(), Target: qt})
	mustValidate(t, r)
	if strings.Contains(r.Expr, "members") || strings.Contains(r.Expr, "0 * (") || !strings.Contains(r.Expr, "changes(") {
		t.Errorf("members off:\n%s", r.Expr)
	}
	if strings.Contains(r.Annotations[AnnotationThreshold], "consumers") {
		t.Errorf("threshold mentions members: %s", r.Annotations[AnnotationThreshold])
	}

	// Offset metric off: only the members condition.
	qt = queueTarget()
	qt.OffsetMetric = ""
	r = QueueConsumerStopped(QueueConsumerStoppedParams{Meta: meta(), Target: qt})
	mustValidate(t, r)
	if strings.Contains(r.Expr, "offset") || !strings.Contains(r.Expr, "kafka_consumer_group_members") {
		t.Errorf("offset off:\n%s", r.Expr)
	}

	// Hostile topic names stay quoted.
	qt = queueTarget()
	qt.Topic = `or"ders\x`
	r = QueueConsumerStopped(QueueConsumerStoppedParams{Meta: meta(), Target: qt})
	mustValidate(t, r)
	contains(t, "expr", r.Expr, `"topic", "or\"ders\\x", "", ""`, `topic="or\"ders\\x"`)
}
