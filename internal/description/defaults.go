// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package description

import (
	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

// Built-in defaults. They follow the OpenTelemetry span metrics connector
// (with the "traces.span.metrics" namespace) and cAdvisor naming; every one of
// them can be overridden in spec.telemetry.
const (
	DefaultCallsMetric          = "traces_span_metrics_calls_total"
	DefaultDurationBucketMetric = "traces_span_metrics_duration_milliseconds_bucket"
	DefaultDurationUnit         = "ms"
	DefaultServiceLabel         = "service_name"
	DefaultOperationLabel       = "span_name"
	DefaultSpanKindLabel        = "span_kind"
	DefaultStatusCodeLabel      = "status_code"
	DefaultErrorStatusValue     = "STATUS_CODE_ERROR"

	DefaultMemoryMetric     = "container_memory_working_set_bytes"
	DefaultCPUMetric        = "container_cpu_usage_seconds_total"
	DefaultRestartsMetric   = "kube_pod_container_status_restarts_total"
	DefaultLimitsMetric     = "kube_pod_container_resource_limits"
	DefaultTerminatedMetric = "kube_pod_container_status_last_terminated_reason"
	DefaultNamespaceLabel   = "namespace"
	DefaultPodLabel         = "pod"
	DefaultContainerLabel   = "container"

	DefaultLagMetric     = "kafka_consumer_group_lag_ratio"
	DefaultMembersMetric = "kafka_consumer_group_members"
	DefaultOffsetMetric  = "kafka_consumer_group_offset_ratio"
	DefaultGroupLabel    = "group"
	DefaultTopicLabel    = "topic"

	// DefaultRateWindow is used for rate()/increase() when not set. It is
	// kept short so that a failure is visible within a couple of minutes;
	// span metrics that update once a minute still give two samples in 2m.
	DefaultRateWindow v1alpha1.Duration = "2m"
	// MinRecommendedRateWindow is the smallest window that still covers two
	// samples at a one-minute update interval.
	MinRecommendedRateWindow v1alpha1.Duration = "2m"

	// DisabledMetric disables a metric-based rule when used as a metric name.
	DisabledMetric = "-"
)

// DefaultServerSpanKinds are span kinds that represent incoming work.
var DefaultServerSpanKinds = []string{"SPAN_KIND_SERVER", "SPAN_KIND_CONSUMER"}

// ResolvedTelemetry is spec.telemetry with every default filled in.
type ResolvedTelemetry struct {
	Spans      v1alpha1.SpanMetrics
	Containers v1alpha1.ContainerMetrics
	Queues     v1alpha1.QueueMetrics
}

// ResolveTelemetry returns the telemetry mapping with defaults applied.
func ResolveTelemetry(t *v1alpha1.Telemetry) ResolvedTelemetry {
	var r ResolvedTelemetry
	if t != nil {
		if t.Spans != nil {
			r.Spans = *t.Spans.DeepCopy()
		}
		if t.Containers != nil {
			r.Containers = *t.Containers
		}
		if t.Queues != nil {
			r.Queues = *t.Queues
		}
	}
	s := &r.Spans
	or(&s.CallsMetric, DefaultCallsMetric)
	or(&s.DurationBucketMetric, DefaultDurationBucketMetric)
	or(&s.DurationUnit, DefaultDurationUnit)
	or(&s.ServiceLabel, DefaultServiceLabel)
	or(&s.OperationLabel, DefaultOperationLabel)
	or(&s.SpanKindLabel, DefaultSpanKindLabel)
	or(&s.StatusCodeLabel, DefaultStatusCodeLabel)
	or(&s.ErrorStatusValue, DefaultErrorStatusValue)
	if len(s.ServerSpanKinds) == 0 {
		s.ServerSpanKinds = append([]string(nil), DefaultServerSpanKinds...)
	}

	c := &r.Containers
	or(&c.MemoryMetric, DefaultMemoryMetric)
	or(&c.CPUMetric, DefaultCPUMetric)
	or(&c.RestartsMetric, DefaultRestartsMetric)
	or(&c.NamespaceLabel, DefaultNamespaceLabel)
	or(&c.PodLabel, DefaultPodLabel)
	or(&c.LimitsMetric, DefaultLimitsMetric)
	or(&c.TerminatedReasonMetric, DefaultTerminatedMetric)
	or(&c.ContainerLabel, DefaultContainerLabel)

	q := &r.Queues
	or(&q.LagMetric, DefaultLagMetric)
	or(&q.MembersMetric, DefaultMembersMetric)
	or(&q.OffsetMetric, DefaultOffsetMetric)
	or(&q.GroupLabel, DefaultGroupLabel)
	or(&q.TopicLabel, DefaultTopicLabel)
	return r
}

// RateWindow returns spec.defaults.rateWindow or the built-in default.
func RateWindow(spec *v1alpha1.SystemDescriptionSpec) v1alpha1.Duration {
	if spec.Defaults != nil && spec.Defaults.RateWindow != "" {
		return spec.Defaults.RateWindow
	}
	return DefaultRateWindow
}

// TelemetryName returns the value of the service label for a service.
func TelemetryName(s *v1alpha1.Service) string {
	if s.TelemetryName != "" {
		return s.TelemetryName
	}
	return s.Name
}

func or(dst *string, def string) {
	if *dst == "" {
		*dst = def
	}
}
