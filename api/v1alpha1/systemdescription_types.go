// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Field comments in this file become descriptions in the generated CRD and in
// the JSON Schema used by the core validator, the UI forms and the YAML editor.
// Keep them short and user-facing. Run `make generate` after changing them.

// Duration is a Prometheus-style duration, for example 30s, 5m, 1h, 30d.
// +kubebuilder:validation:Pattern=`^([0-9]+(ms|s|m|h|d|w|y))+$`
type Duration string

// ServiceType is the kind of workload a service is.
// +kubebuilder:validation:Enum=http;grpc;queue;database
type ServiceType string

const (
	ServiceTypeHTTP     ServiceType = "http"
	ServiceTypeGRPC     ServiceType = "grpc"
	ServiceTypeQueue    ServiceType = "queue"
	ServiceTypeDatabase ServiceType = "database"
)

// Severity of generated alerts.
// +kubebuilder:validation:Enum=warning;critical
type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// SystemDescriptionSpec describes the services of one system, their thresholds
// and SLOs, and where notifications go.
type SystemDescriptionSpec struct {
	// Human-readable name of the environment, added as the "environment" label to every alert.
	// +optional
	Environment string `json:"environment,omitempty"`

	// ID of the set of observability programs (Prometheus, Alertmanager,
	// Grafana, Loki, Tempo, OpenTelemetry Collector) the system uses, as
	// shown on Kamerton's status page. Empty: the built-in set. An unknown
	// ID falls back to the built-in set with a warning.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Stack string `json:"stack,omitempty"`

	// Names of metrics and labels in your Prometheus. Defaults follow the
	// OpenTelemetry span metrics connector and cAdvisor; override them if your
	// pipeline names things differently.
	// +optional
	Telemetry *Telemetry `json:"telemetry,omitempty"`

	// Default policies applied to every service unless the service overrides them.
	// +optional
	Defaults *Defaults `json:"defaults,omitempty"`

	// Services of the system.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Services []Service `json:"services"`

	// Notification receivers and routing.
	// +optional
	Notifications *Notifications `json:"notifications,omitempty"`
}

// Telemetry maps Kamerton's concepts onto metric and label names.
type Telemetry struct {
	// Span metrics (RED metrics derived from traces).
	// +optional
	Spans *SpanMetrics `json:"spans,omitempty"`
	// Container resource metrics.
	// +optional
	Containers *ContainerMetrics `json:"containers,omitempty"`
	// Queue consumer lag metrics.
	// +optional
	Queues *QueueMetrics `json:"queues,omitempty"`
}

// SpanMetrics names the span-derived request metrics.
type SpanMetrics struct {
	// Counter of calls. Default: traces_span_metrics_calls_total.
	// +optional
	CallsMetric string `json:"callsMetric,omitempty"`
	// Histogram bucket series of call durations. Default: traces_span_metrics_duration_milliseconds_bucket.
	// +optional
	DurationBucketMetric string `json:"durationBucketMetric,omitempty"`
	// Unit of the duration histogram buckets. Default: ms.
	// +kubebuilder:validation:Enum=ms;s
	// +optional
	DurationUnit string `json:"durationUnit,omitempty"`
	// Label holding the service name. Default: service_name.
	// +optional
	ServiceLabel string `json:"serviceLabel,omitempty"`
	// Label holding the operation (span) name. Default: span_name.
	// +optional
	OperationLabel string `json:"operationLabel,omitempty"`
	// Label holding the span kind. Default: span_kind.
	// +optional
	SpanKindLabel string `json:"spanKindLabel,omitempty"`
	// Span kinds that represent incoming work of a service. Default: SPAN_KIND_SERVER, SPAN_KIND_CONSUMER.
	// +optional
	ServerSpanKinds []string `json:"serverSpanKinds,omitempty"`
	// Label holding the span status code. Default: status_code.
	// +optional
	StatusCodeLabel string `json:"statusCodeLabel,omitempty"`
	// Value of the status code label that marks an error. Default: STATUS_CODE_ERROR.
	// +optional
	ErrorStatusValue string `json:"errorStatusValue,omitempty"`
}

// ContainerMetrics names container resource metrics.
type ContainerMetrics struct {
	// Memory gauge in bytes. Default: container_memory_working_set_bytes.
	// +optional
	MemoryMetric string `json:"memoryMetric,omitempty"`
	// CPU usage counter in seconds. Default: container_cpu_usage_seconds_total.
	// +optional
	CPUMetric string `json:"cpuMetric,omitempty"`
	// Container restarts counter (from kube-state-metrics). Default: kube_pod_container_status_restarts_total. Set to "-" to disable restart alerts.
	// +optional
	RestartsMetric string `json:"restartsMetric,omitempty"`
	// Container resource limits (from kube-state-metrics, with a "resource"
	// label of memory or cpu). Default: kube_pod_container_resource_limits.
	// Set to "-" to disable the alerts on usage close to the limit.
	// +optional
	LimitsMetric string `json:"limitsMetric,omitempty"`
	// Reason of the last container termination (from kube-state-metrics, with
	// a "reason" label). Default: kube_pod_container_status_last_terminated_reason.
	// Set to "-" to disable the out-of-memory kill alert.
	// +optional
	TerminatedReasonMetric string `json:"terminatedReasonMetric,omitempty"`
	// Label holding the Kubernetes namespace. Default: namespace.
	// +optional
	NamespaceLabel string `json:"namespaceLabel,omitempty"`
	// Label holding the pod name. Default: pod.
	// +optional
	PodLabel string `json:"podLabel,omitempty"`
	// Label holding the container name (kube-state-metrics). Default: container.
	// +optional
	ContainerLabel string `json:"containerLabel,omitempty"`
}

// QueueMetrics names queue consumer metrics.
type QueueMetrics struct {
	// Consumer lag gauge. Default: kafka_consumer_group_lag_ratio.
	// +optional
	LagMetric string `json:"lagMetric,omitempty"`
	// Number of members of a consumer group (with the group label only).
	// Default: kafka_consumer_group_members. Set to "-" to stop treating a
	// group without members as a stopped consumer.
	// +optional
	MembersMetric string `json:"membersMetric,omitempty"`
	// Committed offset of a consumer group (per group, topic and partition).
	// Default: kafka_consumer_group_offset_ratio. Set to "-" to stop treating
	// an offset that does not move while messages wait as a stopped consumer.
	// +optional
	OffsetMetric string `json:"offsetMetric,omitempty"`
	// Label holding the consumer group. Default: group.
	// +optional
	GroupLabel string `json:"groupLabel,omitempty"`
	// Label holding the topic. Default: topic.
	// +optional
	TopicLabel string `json:"topicLabel,omitempty"`
}

// Defaults are policies applied to all services.
type Defaults struct {
	// Kubernetes namespace where the services run.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Window for rate() and increase(). Must cover at least two scrapes; with
	// metrics updated once a minute use 2m or more. Default: 5m.
	// +optional
	RateWindow Duration `json:"rateWindow,omitempty"`
	// Operations (span names, RE2 regular expressions) ignored by all request
	// rules, for example noisy streaming endpoints.
	// +optional
	ExcludeOperations []string `json:"excludeOperations,omitempty"`
	// Default error rate policy.
	// +optional
	ErrorRate *ErrorRatePolicy `json:"errorRate,omitempty"`
	// Default latency policy.
	// +optional
	Latency *LatencyPolicy `json:"latency,omitempty"`
	// Default resource saturation policy.
	// +optional
	Saturation *SaturationPolicy `json:"saturation,omitempty"`
	// Default policy of the "requests stopped arriving" alert.
	// +optional
	TrafficDrop *TrafficDropPolicy `json:"trafficDrop,omitempty"`
}

// ErrorRatePolicy alerts when the share of failed calls is too high.
type ErrorRatePolicy struct {
	// Set to false to disable this alert.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Maximum allowed share of failed calls, from 0 to 1 (0.05 = 5%).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	Threshold *float64 `json:"threshold,omitempty"`
	// Minimum traffic, in calls per second, for the alert to be evaluated.
	// Prevents alerts on a single failed call in an idle service. Not used by
	// the per-operation rule, which needs at least 3 failed calls of the
	// operation in the window instead.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinRequestsPerSecond *float64 `json:"minRequestsPerSecond,omitempty"`
	// How long the condition must hold before the alert fires.
	// +optional
	For Duration `json:"for,omitempty"`
	// Also evaluate the error rate of every operation separately, so a failure
	// of one operation is not hidden by healthy traffic of others. The
	// per-operation rule needs at least 3 failed calls of the operation in the
	// window. Default: true; set to false to turn it off.
	// +optional
	PerOperation *bool `json:"perOperation,omitempty"`
	// Severity of this alert: critical or warning. Default: critical for
	// critical services, warning for others.
	// +optional
	Severity Severity `json:"severity,omitempty"`
	// Notification text for this alert (also used by the per-operation rule) written by the team, in any language.
	// When absent, Kamerton's default text is used.
	// +optional
	Message *NotificationMessage `json:"message,omitempty"`
}

// LatencyPolicy alerts when a latency quantile is too high.
type LatencyPolicy struct {
	// Set to false to disable this alert.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Quantile to evaluate, for example 0.95.
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	Quantile *float64 `json:"quantile,omitempty"`
	// Latency threshold in milliseconds.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ThresholdMs *float64 `json:"thresholdMs,omitempty"`
	// Minimum traffic, in calls per second, for the alert to be evaluated.
	// Default: none; the latency rules always need at least 3 calls in the
	// window, so a quantile is never computed from one or two requests.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinRequestsPerSecond *float64 `json:"minRequestsPerSecond,omitempty"`
	// How long the condition must hold before the alert fires.
	// +optional
	For Duration `json:"for,omitempty"`
	// Also evaluate the latency of every operation separately, so a slow
	// operation is not hidden by fast traffic of others. Operations with their
	// own latency policy are left to that policy. Default: true.
	// +optional
	PerOperation *bool `json:"perOperation,omitempty"`
	// Alert when the latency quantile of the service is this many times its
	// normal value (the quantile over the previous hour, ending 10 minutes
	// ago) and above 100 ms, whatever the threshold. Default: 5; 0 turns
	// the alert off.
	// +kubebuilder:validation:Minimum=0
	// +optional
	AboveNormalFactor *float64 `json:"aboveNormalFactor,omitempty"`
	// Severity of this alert: critical or warning. Default: critical for
	// critical services, warning for others.
	// +optional
	Severity Severity `json:"severity,omitempty"`
	// Notification text for this alert (also used by the per-operation and
	// "slower than normal" rules) written by the team, in any language.
	// When absent, Kamerton's default text is used.
	// +optional
	Message *NotificationMessage `json:"message,omitempty"`
}

// TrafficDropPolicy alerts when a service that normally receives requests
// stops receiving them, or gets only a small share of its normal traffic.
// Normal traffic is the average rate of incoming calls (server and consumer
// spans) over the last day, counted only where data exists; with less than an
// hour of history there is no normal and no alert. The alert stays silent
// while data is missing for most services of the system at once, because then
// the telemetry pipeline, not the service, has stopped.
type TrafficDropPolicy struct {
	// Set to true or false to turn this alert on or off. Default: on for
	// critical services of type http or grpc, off for others; services of type
	// queue or database get it only when set on the service itself.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Share of the normal traffic at or below which the alert fires, from 0
	// to 1: 0.1 = 10%, 0 = only when calls stop completely. Default: 0.1.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:ExclusiveMaximum=true
	// +kubebuilder:validation:Maximum=1
	// +optional
	Threshold *float64 `json:"threshold,omitempty"`
	// Minimum normal traffic, in calls per second, for the alert to apply:
	// quieter services go minutes without a call in normal operation.
	// Default: 0.02 (about one call a minute).
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinRequestsPerSecond *float64 `json:"minRequestsPerSecond,omitempty"`
	// How long the condition must hold before the alert fires. Default: 2m.
	// +optional
	For Duration `json:"for,omitempty"`
	// Severity of this alert: critical or warning. Default: critical for
	// critical services, warning for others.
	// +optional
	Severity Severity `json:"severity,omitempty"`
	// Notification text for this alert written by the team, in any language.
	// When absent, Kamerton's default text is used.
	// +optional
	Message *NotificationMessage `json:"message,omitempty"`
}

// SaturationPolicy alerts on memory, CPU and restarts of the service pods.
type SaturationPolicy struct {
	// Set to false to disable these alerts.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Memory threshold per pod as a Kubernetes quantity, for example 512Mi.
	// When set, it replaces the alert on memory close to the limit.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$`
	// +optional
	Memory string `json:"memory,omitempty"`
	// CPU threshold per pod in cores, for example 0.5. When set, it replaces
	// the alerts on CPU close to the limit and far above normal.
	// +kubebuilder:validation:Minimum=0
	// +optional
	CPUCores *float64 `json:"cpuCores,omitempty"`
	// Without an explicit memory threshold: alert when the memory of a pod
	// exceeds this share of its memory limit (all containers must have a
	// limit) and its trend over the last hour reaches the limit within 30
	// minutes. From 0 to 1. Default: 0.95; 0 turns the alert off.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	MemoryLimitShare *float64 `json:"memoryLimitShare,omitempty"`
	// Without an explicit CPU threshold: alert when the CPU usage of a pod
	// exceeds this share of its CPU limit (all containers must have a limit).
	// From 0 to 1. Default: 0.9; 0 turns the alert off.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	CPULimitShare *float64 `json:"cpuLimitShare,omitempty"`
	// Without an explicit CPU threshold: alert when a pod uses more than 0.5
	// cores and this many times the normal CPU of the service (the average
	// over the previous hour, ending 10 minutes ago). Default: 3; 0 turns
	// the alert off.
	// +kubebuilder:validation:Minimum=0
	// +optional
	CPUAboveNormalFactor *float64 `json:"cpuAboveNormalFactor,omitempty"`
	// Set to false to disable the alert on a container killed for running
	// out of memory (OOMKilled). It fires at once and lasts 10 minutes.
	// +optional
	OOMKilledEnabled *bool `json:"oomKilledEnabled,omitempty"`
	// Maximum container restarts per hour.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxRestartsPerHour *int32 `json:"maxRestartsPerHour,omitempty"`
	// Set to false to disable only the container restarts alert.
	// +optional
	RestartsEnabled *bool `json:"restartsEnabled,omitempty"`
	// How long the condition must hold before the alert fires.
	// +optional
	For Duration `json:"for,omitempty"`
	// Severity of these alerts: critical or warning. Default: critical for
	// critical services, warning for others.
	// +optional
	Severity Severity `json:"severity,omitempty"`
	// Notification text of the memory alerts written by the team.
	// +optional
	MemoryMessage *NotificationMessage `json:"memoryMessage,omitempty"`
	// Notification text of the CPU alerts written by the team.
	// +optional
	CPUMessage *NotificationMessage `json:"cpuMessage,omitempty"`
	// Notification text of the out-of-memory kill alert written by the team.
	// +optional
	OOMKilledMessage *NotificationMessage `json:"oomKilledMessage,omitempty"`
	// Notification text of the restarts alert written by the team.
	// +optional
	RestartsMessage *NotificationMessage `json:"restartsMessage,omitempty"`
}

// Service is one component of the system.
type Service struct {
	// Unique name of the service.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// Kind of service.
	Type ServiceType `json:"type"`
	// Critical services page on-call; others create warnings.
	// +optional
	Critical bool `json:"critical,omitempty"`
	// Short description shown in the UI and in notifications.
	// +optional
	Description string `json:"description,omitempty"`
	// Team or person responsible for the service.
	// +optional
	Owner string `json:"owner,omitempty"`
	// Value of the service label in telemetry, if it differs from name.
	// +optional
	TelemetryName string `json:"telemetryName,omitempty"`
	// Services this service calls. Used to build the service map and to find
	// the probable source of an incident.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// Where the service pods run.
	// +optional
	Workload *Workload `json:"workload,omitempty"`
	// Error rate policy, overriding defaults.
	// +optional
	ErrorRate *ErrorRatePolicy `json:"errorRate,omitempty"`
	// Latency policy, overriding defaults.
	// +optional
	Latency *LatencyPolicy `json:"latency,omitempty"`
	// Resource saturation policy, overriding defaults.
	// +optional
	Saturation *SaturationPolicy `json:"saturation,omitempty"`
	// Policy of the "requests stopped arriving" alert, overriding defaults.
	// +optional
	TrafficDrop *TrafficDropPolicy `json:"trafficDrop,omitempty"`
	// Operations with their own thresholds.
	// +optional
	// +listType=map
	// +listMapKey=name
	Operations []Operation `json:"operations,omitempty"`
	// Additional operations (RE2 regular expressions) ignored for this service.
	// +optional
	ExcludeOperations []string `json:"excludeOperations,omitempty"`
	// Queue settings, for services of type queue.
	// +optional
	Queue *QueueSpec `json:"queue,omitempty"`
	// Availability service level objective.
	// +optional
	SLO *SLO `json:"slo,omitempty"`
	// Link to the runbook. If empty, a runbook stub is generated.
	// +optional
	RunbookURL string `json:"runbookURL,omitempty"`
	// Rules for cases the built-in policies do not cover. They get the same
	// labels, notification text and runbook link as built-in rules.
	// +optional
	// +listType=map
	// +listMapKey=name
	CustomRules []CustomRule `json:"customRules,omitempty"`
}

// CustomRule is an alert written by the user. Exactly one of condition or expr must be set.
type CustomRule struct {
	// Identifier, unique within the service: lowercase letters, digits and dashes.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// What is wrong, in plain words. Shown as the title of the notification.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	Title string `json:"title"`
	// Set to false to disable this alert.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Condition assembled from parts: metric, function, aggregation, comparison.
	// +optional
	Condition *CustomCondition `json:"condition,omitempty"`
	// PromQL expression; the alert fires for every series it returns.
	// +optional
	Expr string `json:"expr,omitempty"`
	// How long the condition must hold before the alert fires. Default: 5m.
	// +optional
	For Duration `json:"for,omitempty"`
	// Severity of this alert: critical or warning. Default: critical for
	// critical services, warning for others.
	// +optional
	Severity Severity `json:"severity,omitempty"`
	// Notification text for this alert written by the team, in any language.
	// When absent, Kamerton's default text is used.
	// +optional
	Message *NotificationMessage `json:"message,omitempty"`
	// Unit shown after the value in notifications, for example %, ms or orders.
	// +kubebuilder:validation:MaxLength=20
	// +optional
	Unit string `json:"unit,omitempty"`
}

// CustomCondition is a simple threshold condition on one metric.
type CustomCondition struct {
	// Metric name, for example http_requests_total.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_:][a-zA-Z0-9_:]*$`
	Metric string `json:"metric"`
	// Only series with these exact label values.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// What to take from the metric: the value as is (none), the per-second
	// rate of a counter (rate) or its increase over the rate window (increase). Default: none.
	// +kubebuilder:validation:Enum=none;rate;increase
	// +optional
	Function string `json:"function,omitempty"`
	// How to combine series: max, min, avg, sum or count. Default: max.
	// +kubebuilder:validation:Enum=max;min;avg;sum;count
	// +optional
	Aggregation string `json:"aggregation,omitempty"`
	// Labels kept after aggregation: one alert per combination of their values.
	// +optional
	By []string `json:"by,omitempty"`
	// Comparison with the threshold: gt (>), gte (>=), lt (<), lte (<=), eq (==), ne (!=).
	// +kubebuilder:validation:Enum=gt;gte;lt;lte;eq;ne
	Operator string `json:"operator"`
	// Threshold value.
	Threshold float64 `json:"threshold"`
}

// Workload locates the pods of a service.
type Workload struct {
	// Kubernetes namespace. Defaults to defaults.namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// RE2 regular expression matching pod names. Default: "<name>-.*".
	// +optional
	PodRegex string `json:"podRegex,omitempty"`
}

// Operation is a single operation (span name) of a service with its own policy.
type Operation struct {
	// Span name of the operation, for example oteldemo.CheckoutService/PlaceOrder.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Error rate policy for this operation.
	// +optional
	ErrorRate *ErrorRatePolicy `json:"errorRate,omitempty"`
	// Latency policy for this operation.
	// +optional
	Latency *LatencyPolicy `json:"latency,omitempty"`
}

// QueueSpec describes a queue topic and its consumers.
type QueueSpec struct {
	// Topic name.
	// +kubebuilder:validation:MinLength=1
	Topic string `json:"topic"`
	// Consumer groups to watch. Empty means all groups of the topic.
	// +optional
	ConsumerGroups []string `json:"consumerGroups,omitempty"`
	// Maximum allowed consumer lag (in messages).
	// +kubebuilder:validation:Minimum=0
	MaxLag float64 `json:"maxLag"`
	// How long the condition must hold before the alert fires.
	// +optional
	For Duration `json:"for,omitempty"`
	// Severity of the queue alerts (lag and stopped consumer): critical or
	// warning. Default: critical for critical services, warning for others.
	// +optional
	Severity Severity `json:"severity,omitempty"`
	// Notification text for this alert written by the team, in any language.
	// When absent, Kamerton's default text is used.
	// +optional
	Message *NotificationMessage `json:"message,omitempty"`
	// Set to false to disable the alert on a consumer group that stopped
	// reading the topic: the group has no members for a minute, or messages
	// wait for 3 minutes while its committed offset does not move. It needs
	// no threshold and holds for 1 minute (or the shorter for above).
	// +optional
	ConsumerStoppedEnabled *bool `json:"consumerStoppedEnabled,omitempty"`
	// Notification text of the stopped consumer alert written by the team.
	// +optional
	ConsumerStoppedMessage *NotificationMessage `json:"consumerStoppedMessage,omitempty"`
}

// SLO is an availability objective measured on span metrics.
type SLO struct {
	// Target share of successful calls in percent, for example 99.5.
	// +kubebuilder:validation:Minimum=50
	// +kubebuilder:validation:ExclusiveMaximum=true
	// +kubebuilder:validation:Maximum=100
	Objective float64 `json:"objective"`
	// SLO window. Default: 30d.
	// +optional
	Window Duration `json:"window,omitempty"`
	// Notification text for both burn-rate alerts written by the team, in any language.
	// When absent, Kamerton's default text is used.
	// +optional
	Message *NotificationMessage `json:"message,omitempty"`
}

// NotificationMessage is the text of a notification written by the team, in
// any language. Placeholders in braces are replaced: {value} (current value),
// {threshold}, {service}, {operation} (empty when the rule is not about one
// operation), {system} and {since} (when the alert started). Markup is not
// allowed: the text is shown as written.
type NotificationMessage struct {
	// What happened, in the team's words. Replaces the title of the notification.
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Title string `json:"title,omitempty"`
	// What it means, with values, for example "Error rate {value} (threshold {threshold})".
	// +kubebuilder:validation:MaxLength=1000
	// +optional
	Text string `json:"text,omitempty"`
	// First steps for the person on call.
	// +kubebuilder:validation:MaxLength=1000
	// +optional
	Action string `json:"action,omitempty"`
}

// Notifications configure where alerts are delivered.
type Notifications struct {
	// Notification receivers.
	// +optional
	// +listType=map
	// +listMapKey=name
	Receivers []Receiver `json:"receivers,omitempty"`
	// Routing of alerts to receivers.
	// +optional
	Routing *Routing `json:"routing,omitempty"`
	// Language of notification messages and of the plain-words alert text: en (default) or ru.
	// +kubebuilder:validation:Enum=ru;en
	// +optional
	Language string `json:"language,omitempty"`
	// Time zone of the times in notification messages, as an IANA name such as
	// Asia/Almaty or Europe/Berlin. Times are shown with their offset, for
	// example "09:18 16.11 UTC+5". Default: UTC.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	TimeZone string `json:"timeZone,omitempty"`
}

// Receiver is one notification channel. Exactly one of telegram or email must be set.
type Receiver struct {
	// Unique receiver name.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// Telegram chat.
	// +optional
	Telegram *TelegramReceiver `json:"telegram,omitempty"`
	// Email recipients.
	// +optional
	Email *EmailReceiver `json:"email,omitempty"`
}

// TelegramReceiver sends alerts to a Telegram chat via a bot.
type TelegramReceiver struct {
	// Chat ID, for example -1001234567890.
	ChatID int64 `json:"chatID"`
	// Kubernetes Secret key holding the bot token. The token never goes into this description.
	BotTokenSecret SecretKeyRef `json:"botTokenSecret"`
}

// EmailReceiver sends alerts by email.
type EmailReceiver struct {
	// Recipient addresses.
	// +kubebuilder:validation:MinItems=1
	To []string `json:"to"`
	// Sender address.
	From string `json:"from"`
	// SMTP server as host:port.
	// +kubebuilder:validation:Pattern=`^[^:\s]+:[0-9]+$`
	Smarthost string `json:"smarthost"`
	// SMTP username.
	// +optional
	AuthUsername string `json:"authUsername,omitempty"`
	// Kubernetes Secret key holding the SMTP password.
	// +optional
	AuthPasswordSecret *SecretKeyRef `json:"authPasswordSecret,omitempty"`
}

// SecretKeyRef points to a key of a Kubernetes Secret in the namespace of the description.
type SecretKeyRef struct {
	// Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Key inside the Secret.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// Routing decides which receivers get which alerts.
type Routing struct {
	// Receivers for critical alerts.
	// +optional
	Critical []string `json:"critical,omitempty"`
	// Receivers for warnings.
	// +optional
	Warning []string `json:"warning,omitempty"`
	// Labels used to group alerts into one notification. Default: alertname, service.
	// +optional
	GroupBy []string `json:"groupBy,omitempty"`
	// How often to repeat a notification for an alert that is still firing. Default: 4h.
	// +optional
	RepeatInterval Duration `json:"repeatInterval,omitempty"`
	// Routes limited to some services: the receiver gets only alerts of the
	// listed services with the listed severities. Use critical/warning above
	// for receivers that get alerts of every service.
	// +optional
	Routes []Route `json:"routes,omitempty"`
}

// Route sends alerts of some services to one receiver.
type Route struct {
	// Receiver name.
	// +kubebuilder:validation:MinLength=1
	Receiver string `json:"receiver"`
	// Severities to deliver. Empty means all severities.
	// +optional
	Severities []Severity `json:"severities,omitempty"`
	// Service names whose alerts are delivered.
	// +kubebuilder:validation:MinItems=1
	Services []string `json:"services"`
}

// SystemDescriptionStatus is the observed state written by the controller.
type SystemDescriptionStatus struct {
	// Generation of the spec that was last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Number of generated alerting rules.
	// +optional
	GeneratedRules int32 `json:"generatedRules,omitempty"`
	// Conditions: Valid, Generated, Applied.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SystemDescription is the Schema for the systemdescriptions API.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sysdesc;sd
// +kubebuilder:printcolumn:name="Rules",type=integer,JSONPath=`.status.generatedRules`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type SystemDescription struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SystemDescriptionSpec   `json:"spec"`
	Status SystemDescriptionStatus `json:"status,omitempty"`
}

// SystemDescriptionList contains a list of SystemDescription.
//
// +kubebuilder:object:root=true
type SystemDescriptionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SystemDescription `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SystemDescription{}, &SystemDescriptionList{})
}
