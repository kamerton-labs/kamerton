// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package rules is Kamerton's rule library: templates that render Prometheus
// alerting rules for the four signal types (error rate, latency, resource
// saturation, queue lag), an SLO burn-rate template and a "requests stopped
// arriving" template (traffic.go).
//
// Templates take plain parameter structs, not the description types, so the
// library can be reused and tested on its own. Every metric and label name is
// a parameter; nothing is tied to a particular demo.
package rules

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/rulefmt"
)

// Alert names produced by the library. One alert name per signal type, with
// the service in labels, keeps Alertmanager grouping and silencing simple.
const (
	AlertErrorRate          = "KamertonErrorRateHigh"
	AlertOperationErrorRate = "KamertonOperationErrorRateHigh"
	AlertLatency            = "KamertonLatencyHigh"
	AlertOperationLatency   = "KamertonOperationLatencyHigh"
	AlertMemory             = "KamertonMemoryHigh"
	AlertCPU                = "KamertonCPUHigh"
	AlertRestarts           = "KamertonPodRestarting"
	AlertQueueLag           = "KamertonQueueLagHigh"
	AlertSLOBurnFast        = "KamertonSLOBurnRateFast"
	AlertSLOBurnSlow        = "KamertonSLOBurnRateSlow"
)

// Labels set on every generated alert.
const (
	LabelService   = "service"
	LabelOperation = "operation"
	LabelSeverity  = "severity"
	LabelSignal    = "kamerton_signal"
	LabelManaged   = "kamerton_managed"
)

// Signal identifies the template that produced a rule.
type Signal string

const (
	SignalErrorRate  Signal = "error_rate"
	SignalLatency    Signal = "latency"
	SignalSaturation Signal = "saturation"
	SignalQueueLag   Signal = "queue_lag"
	SignalSLO        Signal = "slo_burn_rate"
)

// Meta is common to all templates.
type Meta struct {
	// Service is the logical service name (the "service" label of the alert).
	Service string
	// Severity: "critical" or "warning".
	Severity string
	// For is the pending period of the alert.
	For time.Duration
	// RunbookURL is added as the runbook_url annotation when not empty.
	RunbookURL string
	// Labels are extra static labels (for example environment).
	Labels map[string]string
	// RunbookPerAlert appends "/<alert name>" to RunbookURL, so the link
	// opens the section of that rule in Kamerton's runbook page.
	RunbookPerAlert bool
	// Language of the plain-words annotations (see AnnotationTitle): en (default) or ru.
	Language string
}

func (m Meta) rule(alert string, signal Signal, expr string, extra map[string]string) rulefmt.Rule {
	lbls := map[string]string{
		LabelService:  m.Service,
		LabelSeverity: m.Severity,
		LabelSignal:   string(signal),
		LabelManaged:  "true",
	}
	for k, v := range m.Labels {
		lbls[k] = v
	}
	for k, v := range extra {
		lbls[k] = v
	}
	return rulefmt.Rule{
		Alert:  alert,
		Expr:   expr,
		For:    model.Duration(m.For),
		Labels: lbls,
	}
}

func (m Meta) annotate(r *rulefmt.Rule, summary, description string) {
	r.Annotations = map[string]string{
		"summary":     summary,
		"description": description,
	}
	if m.RunbookURL != "" {
		r.Annotations["runbook_url"] = m.RunbookURL
		if m.RunbookPerAlert {
			r.Annotations["runbook_url"] = m.RunbookURL + "/" + r.Alert
		}
	}
}

// SpanTarget selects span metrics of one service (and optionally one operation).
type SpanTarget struct {
	CallsMetric       string
	BucketMetric      string
	DurationInSeconds bool // bucket "le" values are seconds instead of milliseconds
	ServiceLabel      string
	OperationLabel    string
	SpanKindLabel     string
	StatusCodeLabel   string
	ErrorStatusValue  string
	SpanKinds         []string // empty means all kinds
	ServiceValue      string   // value of ServiceLabel
	Operation         string   // exact operation; empty means all operations
	ExcludeOperations []string // RE2 regular expressions, ignored when Operation is set
	Window            time.Duration
}

func (t SpanTarget) matchers() Matchers {
	m := Matchers{}.Eq(t.ServiceLabel, t.ServiceValue)
	if len(t.SpanKinds) > 0 {
		m = m.Re(t.SpanKindLabel, quoteAll(t.SpanKinds)...)
	}
	if t.Operation != "" {
		m = m.Eq(t.OperationLabel, t.Operation)
	} else {
		m = m.NotRe(t.OperationLabel, t.ExcludeOperations...)
	}
	return m
}

func (t SpanTarget) groupBy(perOperation bool) []string {
	if perOperation || t.Operation != "" {
		return []string{t.ServiceLabel, t.OperationLabel}
	}
	return []string{t.ServiceLabel}
}

func (t SpanTarget) callRate(m Matchers, groupBy []string, window time.Duration) string {
	return fmt.Sprintf("sum %s (rate(%s[%s]))", by(groupBy...), m.Selector(t.CallsMetric), dur(window))
}

func (t SpanTarget) callIncrease(m Matchers, groupBy []string, window time.Duration) string {
	return fmt.Sprintf("sum %s (increase(%s[%s]))", by(groupBy...), m.Selector(t.CallsMetric), dur(window))
}

// unlessSingleOperation drops services that had only one operation in the
// window (and, when minRPS > 0, more traffic than minRPS): for them the
// per-operation rule would repeat the service rule on the same calls.
func (t SpanTarget) unlessSingleOperation(minRPS float64) string {
	svc := t.ServiceLabel
	all := t.matchers()
	single := fmt.Sprintf("count by (%s) (%s > 0) == 1", svc, t.callRate(all, []string{svc, t.OperationLabel}, t.Window))
	if minRPS > 0 {
		single = fmt.Sprintf("(%s)\n  and on (%s)\n  %s > %s", single, svc, t.callRate(all, []string{svc}, t.Window), num(minRPS))
	}
	return fmt.Sprintf("unless on (%s)\n(\n  %s\n)", svc, single)
}

// errorRatio renders failed calls / all calls over window.
func (t SpanTarget) errorRatio(groupBy []string, window time.Duration) string {
	all := t.matchers()
	failed := all.clone().Eq(t.StatusCodeLabel, t.ErrorStatusValue)
	return fmt.Sprintf("(\n  %s\n/\n  %s\n)", t.callRate(failed, groupBy, window), t.callRate(all, groupBy, window))
}

// ErrorRateParams parameterize the error rate template.
type ErrorRateParams struct {
	Meta
	Target SpanTarget
	// Threshold is the maximum share of failed calls (0..1).
	Threshold float64
	// MinRequestsPerSecond is the traffic below which the alert stays silent.
	MinRequestsPerSecond float64
	// PerOperation evaluates every operation separately.
	PerOperation bool
	// MinErrors, when above zero, is the number of failed calls the window
	// must hold (a single failure of a rare operation is not 100%).
	MinErrors float64
	// SingleOperationRPS, for a per-operation rule, leaves services with only
	// one operation in the window and traffic above this rate to the service
	// rule, which then alerts on exactly the same calls. Negative: off.
	SingleOperationRPS float64
}

// ErrorRate renders an error-rate alert: failed/all calls above a threshold,
// guarded by a minimum-traffic condition so idle services do not alert on a
// single failure.
func ErrorRate(p ErrorRateParams) rulefmt.Rule {
	t := p.Target
	groupBy := t.groupBy(p.PerOperation)
	expr := fmt.Sprintf("(\n%s > %s\n)", t.errorRatio(groupBy, t.Window), num(p.Threshold))
	if p.MinRequestsPerSecond > 0 || p.MinErrors <= 0 {
		expr += fmt.Sprintf("\nand on (%s)\n(\n  %s > %s\n)", joinComma(groupBy), t.callRate(t.matchers(), groupBy, t.Window), num(p.MinRequestsPerSecond))
	}
	if p.MinErrors > 0 {
		failed := t.matchers().clone().Eq(t.StatusCodeLabel, t.ErrorStatusValue)
		expr += fmt.Sprintf("\nand on (%s)\n(\n  %s >= %s\n)", joinComma(groupBy), t.callIncrease(failed, groupBy, t.Window), num(p.MinErrors))
	}
	if p.PerOperation && t.Operation == "" && p.SingleOperationRPS >= 0 {
		expr += "\n" + t.unlessSingleOperation(p.SingleOperationRPS)
	}

	alert := AlertErrorRate
	var extra map[string]string
	where := p.Service
	if p.PerOperation || t.Operation != "" {
		alert = AlertOperationErrorRate
		where = fmt.Sprintf(`%s %s`, p.Service, labelRef(t.OperationLabel))
	}
	if t.Operation != "" {
		extra = map[string]string{LabelOperation: t.Operation}
	}
	r := p.rule(alert, SignalErrorRate, expr, extra)
	guard := fmt.Sprintf("minimum traffic %s calls/s", num(p.MinRequestsPerSecond))
	if p.MinErrors > 0 {
		guard = fmt.Sprintf("at least %s failed calls", num(p.MinErrors))
	}
	p.annotate(&r,
		fmt.Sprintf("High error rate: %s", where),
		fmt.Sprintf("{{ $value | humanizePercentage }} of calls failed over the last %s (threshold %s, %s).",
			dur(t.Window), percent(p.Threshold), guard))
	detail := ""
	switch {
	case t.Operation != "":
		detail = p.phrases().operation + " " + t.Operation
	case p.PerOperation:
		detail = p.phrases().operation + " " + labelRef(t.OperationLabel)
	}
	p.explain(&r, detail, "{{ $value | humanizePercentage }}", percent(p.Threshold))
	return r
}

// LatencyParams parameterize the latency template.
type LatencyParams struct {
	Meta
	Target SpanTarget
	// Quantile, for example 0.95.
	Quantile float64
	// ThresholdMs is the latency threshold in milliseconds.
	ThresholdMs float64
	// MinRequestsPerSecond is the traffic below which the alert stays silent; 0: no such condition.
	MinRequestsPerSecond float64
	// MinCalls is the number of calls the window must hold, so a quantile
	// is not computed from one or two requests; 0: no such condition.
	MinCalls float64
	// PerOperation evaluates every operation separately. It alerts only on
	// operations that were faster than the threshold during the normal hour
	// (see NormalRange, with at least 30 calls): operations that are slow by
	// design, or seen for the first time, are left to the service rule and to
	// the team's own operations[] thresholds. Services with one operation in
	// the window are left to the service rule, which sees the same calls.
	PerOperation bool
}

// Latency renders a latency alert: histogram quantile above a threshold,
// guarded by a minimum-traffic condition.
func Latency(p LatencyParams) rulefmt.Rule {
	t := p.Target
	groupBy := t.groupBy(p.PerOperation)
	threshold := p.ThresholdMs
	valueText := `{{ $value | printf "%.0f" }} ms`
	localValue := `{{ $value | printf "%.0f" }} ` + p.phrases().ms
	if t.DurationInSeconds {
		threshold = p.ThresholdMs / 1000
		valueText = `{{ $value | humanizeDuration }}`
		localValue = valueText
	}
	expr := fmt.Sprintf("%s > %s", t.quantile(p.Quantile, groupBy, "rate", t.Window, ""), num(threshold))
	if p.MinRequestsPerSecond > 0 || p.MinCalls <= 0 {
		expr += fmt.Sprintf("\nand on (%s)\n(\n  %s > %s\n)", joinComma(groupBy), t.callRate(t.matchers(), groupBy, t.Window), num(p.MinRequestsPerSecond))
	}
	if p.MinCalls > 0 {
		expr += fmt.Sprintf("\nand on (%s)\n(\n  %s >= %s\n)", joinComma(groupBy), t.callIncrease(t.matchers(), groupBy, t.Window), num(p.MinCalls))
	}
	if p.PerOperation && t.Operation == "" {
		off := " offset " + dur(NormalOffset)
		expr += fmt.Sprintf("\nand on (%s)\n(\n  %s <= %s\n  and on (%s)\n  %s >= %d\n)",
			joinComma(groupBy), t.quantile(p.Quantile, groupBy, "increase", NormalRange, off), num(threshold),
			joinComma(groupBy), fmt.Sprintf("sum %s (increase(%s[%s]%s))", by(groupBy...), t.matchers().Selector(t.CallsMetric), dur(NormalRange), off), latencyNormalMinCalls)
		expr += "\n" + t.unlessSingleOperation(0)
	}

	alert := AlertLatency
	var extra map[string]string
	where := p.Service
	if p.PerOperation && t.Operation == "" {
		alert = AlertOperationLatency
		where = p.Service + " " + labelRef(t.OperationLabel)
	}
	if t.Operation != "" {
		alert = AlertOperationLatency
		extra = map[string]string{LabelOperation: t.Operation}
		where = p.Service + " " + t.Operation
	}
	r := p.rule(alert, SignalLatency, expr, extra)
	p.annotate(&r,
		fmt.Sprintf("High latency: %s", where),
		fmt.Sprintf("p%s latency is %s over the last %s (threshold %s ms).",
			num(p.Quantile*100), valueText, dur(t.Window), num(p.ThresholdMs)))
	detail := ""
	switch {
	case t.Operation != "":
		detail = p.phrases().operation + " " + t.Operation
	case p.PerOperation:
		detail = p.phrases().operation + " " + labelRef(t.OperationLabel)
	}
	p.explain(&r, detail, "p"+num(p.Quantile*100)+" "+localValue, num(p.ThresholdMs)+" "+p.phrases().ms)
	return r
}

// quantile renders histogram_quantile over fn (rate or increase) of the
// duration buckets.
func (t SpanTarget) quantile(q float64, groupBy []string, fn string, window time.Duration, offset string) string {
	bucketBy := append(append([]string{}, groupBy...), "le")
	return fmt.Sprintf("histogram_quantile(%s, sum %s (%s(%s[%s]%s)))", num(q), by(bucketBy...), fn, t.matchers().Selector(t.BucketMetric), dur(window), offset)
}

// PodTarget selects container metrics of the pods of one service.
type PodTarget struct {
	MemoryMetric     string
	CPUMetric        string
	RestartsMetric   string
	LimitsMetric     string // kube-state-metrics container limits, "resource" label memory/cpu
	TerminatedMetric string // kube-state-metrics last termination reason, "reason" label
	NamespaceLabel   string
	PodLabel         string
	ContainerLabel   string
	Namespace        string // empty means any namespace
	PodRegex         string // RE2, anchored by Prometheus
	Window           time.Duration
}

func (t PodTarget) matchers() Matchers {
	m := Matchers{}
	if t.Namespace != "" {
		m = m.Eq(t.NamespaceLabel, t.Namespace)
	}
	return m.Re(t.PodLabel, t.PodRegex)
}

// MemoryParams parameterize the memory saturation template.
type MemoryParams struct {
	Meta
	Target PodTarget
	// Bytes is the per-pod memory threshold.
	Bytes float64
}

// Memory renders a per-pod memory alert. max() is used rather than sum()
// because cAdvisor also exports a pod-level series next to per-container
// ones; max picks the pod total without double counting.
func Memory(p MemoryParams) rulefmt.Rule {
	t := p.Target
	expr := fmt.Sprintf("max %s (%s) > %s",
		by(t.NamespaceLabel, t.PodLabel), t.matchers().Selector(t.MemoryMetric), num(p.Bytes))
	r := p.rule(AlertMemory, SignalSaturation, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("High memory usage: %s pod %s", p.Service, labelRef(t.PodLabel)),
		fmt.Sprintf("Memory usage is {{ $value | humanize1024 }}B (threshold %sB).", humanizeBytes(p.Bytes)))
	p.explain(&r, p.phrases().pod+" "+labelRef(t.PodLabel), "{{ $value | humanize1024 }}B", humanizeBytes(p.Bytes)+"B")
	return r
}

// CPUParams parameterize the CPU saturation template.
type CPUParams struct {
	Meta
	Target PodTarget
	// Cores is the per-pod CPU threshold.
	Cores float64
}

// CPU renders a per-pod CPU usage alert (see Memory for why max is used).
func CPU(p CPUParams) rulefmt.Rule {
	t := p.Target
	expr := fmt.Sprintf("max %s (rate(%s[%s])) > %s",
		by(t.NamespaceLabel, t.PodLabel), t.matchers().Selector(t.CPUMetric), dur(t.Window), num(p.Cores))
	r := p.rule(AlertCPU, SignalSaturation, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("High CPU usage: %s pod %s", p.Service, labelRef(t.PodLabel)),
		fmt.Sprintf(`CPU usage is {{ $value | printf "%%.2f" }} cores over the last %s (threshold %s).`, dur(t.Window), num(p.Cores)))
	p.explain(&r, p.phrases().pod+" "+labelRef(t.PodLabel), `{{ $value | printf "%.2f" }} `+p.phrases().cores, num(p.Cores)+" "+p.phrases().cores)
	return r
}

// RestartsParams parameterize the restarts template.
type RestartsParams struct {
	Meta
	Target PodTarget
	// MaxPerHour is the number of restarts per hour above which the alert fires.
	MaxPerHour int
}

// Restarts renders an alert on container restarts during the last hour.
func Restarts(p RestartsParams) rulefmt.Rule {
	t := p.Target
	expr := fmt.Sprintf("sum %s (increase(%s[1h])) > %d",
		by(t.NamespaceLabel, t.PodLabel), t.matchers().Selector(t.RestartsMetric), p.MaxPerHour)
	r := p.rule(AlertRestarts, SignalSaturation, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("Pod restarting: %s pod %s", p.Service, labelRef(t.PodLabel)),
		fmt.Sprintf(`{{ $value | printf "%%.0f" }} container restarts in the last hour (threshold %d).`, p.MaxPerHour))
	p.explain(&r, p.phrases().pod+" "+labelRef(t.PodLabel), `{{ $value | printf "%.0f" }} `+p.phrases().perHour, fmt.Sprintf("%d %s", p.MaxPerHour, p.phrases().perHour))
	return r
}

// QueueTarget selects consumer series of one topic.
type QueueTarget struct {
	LagMetric     string
	MembersMetric string // members of a consumer group, group label only; "" or "-": not used
	OffsetMetric  string // committed offset of a consumer group; "" or "-": not used
	GroupLabel    string
	TopicLabel    string
	Topic         string
	Groups        []string // empty means all consumer groups
}

// QueueLagParams parameterize the queue lag template.
type QueueLagParams struct {
	Meta
	Target QueueTarget
	// MaxLag is the maximum allowed consumer lag.
	MaxLag float64
}

// QueueLag renders a consumer lag alert per consumer group and topic.
func QueueLag(p QueueLagParams) rulefmt.Rule {
	t := p.Target
	m := Matchers{}.Eq(t.TopicLabel, t.Topic).Re(t.GroupLabel, quoteAll(t.Groups)...)
	expr := fmt.Sprintf("max %s (%s) > %s", by(t.GroupLabel, t.TopicLabel), m.Selector(t.LagMetric), num(p.MaxLag))
	r := p.rule(AlertQueueLag, SignalQueueLag, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("Consumer lag on topic %s: group %s", t.Topic, labelRef(t.GroupLabel)),
		fmt.Sprintf(`Consumer lag is {{ $value | printf "%%.0f" }} (threshold %s).`, num(p.MaxLag)))
	p.explain(&r, t.Topic+", "+p.phrases().group+" "+labelRef(t.GroupLabel), `{{ $value | printf "%.0f" }} `+p.phrases().msg, num(p.MaxLag)+" "+p.phrases().msg)
	return r
}

// SLOParams parameterize the SLO burn-rate template.
type SLOParams struct {
	Meta
	Target SpanTarget
	// Objective is the target share of successful calls in percent, e.g. 99.5.
	Objective float64
	// Window is the SLO window, e.g. 30 days.
	Window time.Duration
}

// burnWindow is one multiwindow pair from the Google SRE workbook.
type burnWindow struct {
	long, short time.Duration
	// budget is the share of the error budget consumed over the long window
	// that should trigger the alert.
	budget float64
	alert  string
	pend   time.Duration
}

// SLOBurnRate renders multiwindow, multi-burn-rate alerts (Google SRE
// workbook, chapter 5): a fast-burn alert (2% of the budget in 1h, confirmed
// over 5m) and a slow-burn alert (5% of the budget in 6h, confirmed over
// 30m). Burn-rate factors are derived from the SLO window, so 30d gives the
// classic 14.4 and 6.
func SLOBurnRate(p SLOParams) []rulefmt.Rule {
	t := p.Target
	errorBudget := round((100-p.Objective)/100, 10)
	groupBy := t.groupBy(false)
	windows := []burnWindow{
		{long: time.Hour, short: 5 * time.Minute, budget: 0.02, alert: AlertSLOBurnFast, pend: time.Minute},
		{long: 6 * time.Hour, short: 30 * time.Minute, budget: 0.05, alert: AlertSLOBurnSlow, pend: 15 * time.Minute},
	}
	out := make([]rulefmt.Rule, 0, len(windows))
	for _, w := range windows {
		factor := w.budget * float64(p.Window) / float64(w.long)
		burn := func(window time.Duration) string {
			return fmt.Sprintf("(\n%s / %s\n) > %s", t.errorRatio(groupBy, window), num(errorBudget), num(round(factor, 4)))
		}
		expr := burn(w.long) + "\nand on (" + joinComma(groupBy) + ")\n" + burn(w.short)
		meta := p.Meta
		meta.For = w.pend
		if w.alert == AlertSLOBurnSlow {
			meta.Severity = "warning"
		}
		r := meta.rule(w.alert, SignalSLO, expr, nil)
		meta.annotate(&r,
			fmt.Sprintf("Error budget burning fast: %s", p.Service),
			fmt.Sprintf(`Error budget of the %s%% SLO (%s window) burns {{ $value | printf "%%.1f" }}x faster than allowed over %s (alert at %sx; %s%% of the budget would be spent in %s).`,
				num(p.Objective), dur(p.Window), dur(w.long), num(round(factor, 4)), num(w.budget*100), dur(w.long)))
		if w.alert == AlertSLOBurnSlow {
			r.Annotations["summary"] = fmt.Sprintf("Error budget burning: %s", p.Service)
		}
		meta.explain(&r, "SLO "+num(p.Objective)+"%", `{{ $value | printf "%.1f" }}x`, num(round(factor, 4))+"x")
		out = append(out, r)
	}
	return out
}

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = regexp.QuoteMeta(v)
	}
	return out
}

// QuoteOperation turns an exact operation name into a regular expression.
func QuoteOperation(name string) string { return regexp.QuoteMeta(name) }

func labelRef(name string) string { return fmt.Sprintf(`{{ index $labels %q }}`, name) }

func joinComma(s []string) string { return strings.Join(s, ", ") }

func percent(f float64) string { return num(round(f*100, 4)) + "%" }

func round(f float64, digits int) float64 {
	p := 1.0
	for range digits {
		p *= 10
	}
	if f < 0 {
		return -float64(int64(-f*p+0.5)) / p
	}
	return float64(int64(f*p+0.5)) / p
}

func humanizeBytes(b float64) string {
	units := []string{"", "Ki", "Mi", "Gi", "Ti"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	return num(round(b, 2)) + units[i]
}
