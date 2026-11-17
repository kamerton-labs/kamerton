// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
)

// Rules that work without a threshold written by the team: a container
// killed for running out of memory, usage close to the container limit, CPU
// and latency far above their own normal, a queue nobody reads.
const (
	AlertOOMKilled            = "KamertonOOMKilled"
	AlertMemoryNearLimit      = "KamertonMemoryNearLimit"
	AlertCPUNearLimit         = "KamertonCPUNearLimit"
	AlertCPUAboveNormal       = "KamertonCPUAboveNormal"
	AlertLatencyAboveNormal   = "KamertonLatencyAboveNormal"
	AlertQueueConsumerStopped = "KamertonQueueConsumerStopped"
)

// Fixed parts of these rules. They are not settings: they define what
// "normal" and "recent" mean.
const (
	// NormalRange is the history the normal is taken from, and NormalOffset
	// how long before now it ends, so a spike that has just started does
	// not raise its own normal.
	NormalRange  = time.Hour
	NormalOffset = 10 * time.Minute
	// normalStep is the resolution of the CPU normal, and normalMinPoints
	// the steps with data it needs (half the range): a service seen for the
	// first time a few minutes ago has no normal and no alert.
	normalStep      = time.Minute
	normalMinPoints = 30
	// CPUAboveNormalMinCores: below half a core no CPU spike is worth an alert.
	CPUAboveNormalMinCores = 0.5
	// LatencyAboveNormalMinMs: a quantile under 100 ms is never "slow",
	// however many times its normal it is.
	LatencyAboveNormalMinMs = 100
	// latencyNormalMinCalls is the calls the normal hour must hold.
	latencyNormalMinCalls = 30
	// OOMRecent is how long the out-of-memory alert lasts after the restart.
	OOMRecent = 10 * time.Minute
	// MemoryTrendRange and MemoryTrendHorizon: memory close to the limit
	// alerts only while the trend over the last hour reaches the limit
	// within 30 minutes.
	MemoryTrendRange   = time.Hour
	MemoryTrendHorizon = 30 * time.Minute
	// MinCallsInWindow is the calls (latency) or failed calls (errors per
	// operation) the rate window must hold.
	MinCallsInWindow = 3
	// QueueNoMembersRange is how long a consumer group must have had no
	// members to count as stopped. A restart of a single consumer pod
	// usually leaves the group for less than that.
	QueueNoMembersRange = time.Minute
	// QueueStalledRange is how long messages must wait while the committed
	// offset of the group stays where it was. A slow consumer that still
	// commits in batches (the demo's kafkaQueueProblems fault: one commit
	// every 200 s) must not look stopped; with the pending period of a
	// minute the offset has to stand still for 4 minutes.
	QueueStalledRange = 3 * time.Minute
	// queueFreshRange: the offset series must have a sample this recent, so
	// the last samples before a gap in the data do not look like a stopped
	// offset.
	queueFreshRange = 2 * time.Minute
	// queueGroupsRange: without a list of consumer groups, the groups of
	// the topic are those that had lag series during the last hour.
	queueGroupsRange = time.Hour
)

func (t PodTarget) withContainer() []string {
	return []string{t.NamespaceLabel, t.PodLabel, t.ContainerLabel}
}

// OOMKilledParams parameterize the out-of-memory kill template.
type OOMKilledParams struct {
	Meta
	Target PodTarget
}

// OOMKilled renders an alert on a container whose last termination was an
// out-of-memory kill and that restarted within OOMRecent. It fires at once
// (one kill is already an outage of the container) and resolves OOMRecent
// after the restart. The value is the number of restarts in that time.
func OOMKilled(p OOMKilledParams) rulefmt.Rule {
	t := p.Target
	cby := by(t.withContainer()...)
	expr := fmt.Sprintf("sum %s (increase(%s[%s])) > 0\nand on (%s)\n(\n  max %s (%s) == 1\n)",
		cby, t.matchers().Selector(t.RestartsMetric), dur(OOMRecent),
		joinComma(t.withContainer()),
		cby, t.matchers().Eq("reason", "OOMKilled").Selector(t.TerminatedMetric))
	r := p.rule(AlertOOMKilled, SignalSaturation, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("Container killed for running out of memory: %s pod %s", p.Service, labelRef(t.PodLabel)),
		fmt.Sprintf(`Container %s was killed by the kernel (OOMKilled) and restarted {{ $value | printf "%%.0f" }} time(s) in the last %s.`, labelRef(t.ContainerLabel), dur(OOMRecent)))
	ph := p.phrases()
	p.explain(&r, ph.pod+" "+labelRef(t.PodLabel)+", "+ph.container+" "+labelRef(t.ContainerLabel),
		`{{ $value | printf "%.0f" }} `+ph.restartsIn+" "+dur(OOMRecent), ph.anyOOM)
	return r
}

// NearLimitParams parameterize the "close to the container limit" templates.
type NearLimitParams struct {
	Meta
	Target PodTarget
	// Share of the limit (0..1) above which the alert fires.
	Share float64
}

// limit renders the pod limit of a resource: the sum of the container limits,
// only where every container of the pod has one (otherwise the pod has no
// limit as a whole). Containers are counted by the restarts metric, which
// has one series per container.
func (t PodTarget) limit(resource string) string {
	pby := by(t.NamespaceLabel, t.PodLabel)
	lim := t.matchers().Eq("resource", resource).Selector(t.LimitsMetric)
	out := fmt.Sprintf("sum %s (%s)", pby, lim)
	if t.RestartsMetric != "" && t.RestartsMetric != "-" {
		out = fmt.Sprintf("(\n    %s\n  and on (%s)\n    count %s (%s) >= on (%s) count %s (%s)\n  )",
			out, joinComma([]string{t.NamespaceLabel, t.PodLabel}), pby, lim, joinComma([]string{t.NamespaceLabel, t.PodLabel}), pby, t.matchers().Selector(t.RestartsMetric))
	}
	return out
}

// MemoryNearLimit renders an alert on pod memory above Share of its limit
// that is still growing: the trend over MemoryTrendRange reaches the limit
// within MemoryTrendHorizon. Runtimes that size their heap to the limit (JVM,
// .NET) sit at 90-95% of it for days without harm; memory that keeps growing
// towards the limit ends in an out-of-memory kill. The value is the share of
// the limit.
func MemoryNearLimit(p NearLimitParams) rulefmt.Rule {
	t := p.Target
	pod := joinComma([]string{t.NamespaceLabel, t.PodLabel})
	lim := t.limit("memory")
	expr := fmt.Sprintf("max %s (%s)\n/ on (%s)\n  %s\n> %s\nand on (%s)\n(\n  max %s (predict_linear(%s[%s], %d))\n/ on (%s)\n  %s\n>= 1\n)",
		by(t.NamespaceLabel, t.PodLabel), t.matchers().Selector(t.MemoryMetric), pod, lim, num(p.Share),
		pod, by(t.NamespaceLabel, t.PodLabel), t.matchers().Selector(t.MemoryMetric), dur(MemoryTrendRange), int(MemoryTrendHorizon.Seconds()), pod, lim)
	r := p.rule(AlertMemoryNearLimit, SignalSaturation, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("Memory close to the limit: %s pod %s", p.Service, labelRef(t.PodLabel)),
		fmt.Sprintf("Memory usage is {{ $value | humanizePercentage }} of the pod memory limit (threshold %s) and its trend over the last %s reaches the limit within %s.", percent(p.Share), dur(MemoryTrendRange), dur(MemoryTrendHorizon)))
	ph := p.phrases()
	p.explain(&r, ph.pod+" "+labelRef(t.PodLabel), "{{ $value | humanizePercentage }} "+ph.ofLimit, percent(p.Share)+" "+ph.ofLimit)
	return r
}

// CPUNearLimit renders an alert on pod CPU usage above Share of its limit.
func CPUNearLimit(p NearLimitParams) rulefmt.Rule {
	t := p.Target
	expr := fmt.Sprintf("max %s (rate(%s[%s]))\n/ on (%s)\n  %s\n> %s",
		by(t.NamespaceLabel, t.PodLabel), t.matchers().Selector(t.CPUMetric), dur(t.Window),
		joinComma([]string{t.NamespaceLabel, t.PodLabel}), t.limit("cpu"), num(p.Share))
	r := p.rule(AlertCPUNearLimit, SignalSaturation, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("CPU close to the limit: %s pod %s", p.Service, labelRef(t.PodLabel)),
		fmt.Sprintf("CPU usage is {{ $value | humanizePercentage }} of the pod CPU limit over the last %s (threshold %s).", dur(t.Window), percent(p.Share)))
	ph := p.phrases()
	p.explain(&r, ph.pod+" "+labelRef(t.PodLabel), "{{ $value | humanizePercentage }} "+ph.ofLimit, percent(p.Share)+" "+ph.ofLimit)
	return r
}

// CPUAboveNormalParams parameterize the "CPU far above normal" template.
type CPUAboveNormalParams struct {
	Meta
	Target PodTarget
	// Factor: the alert fires above Factor × normal.
	Factor float64
}

// CPUAboveNormal renders an alert on a pod using more than
// CPUAboveNormalMinCores and more than Factor times the normal CPU of the
// service: the average over NormalRange ending NormalOffset ago of the
// busiest pod, taken per namespace so it survives pods being replaced. A
// service without half an hour of history has no normal and no alert. The
// value is the current usage in cores.
func CPUAboveNormal(p CPUAboveNormalParams) rulefmt.Rule {
	t := p.Target
	ns := t.NamespaceLabel
	rate := fmt.Sprintf("rate(%s[%s])", t.matchers().Selector(t.CPUMetric), dur(t.Window))
	sub := fmt.Sprintf("(max by (%s) (%s))[%s:%s] offset %s", ns, rate, dur(NormalRange), dur(normalStep), dur(NormalOffset))
	normal := fmt.Sprintf("(\n    avg_over_time(%s)\n  and on (%s)\n    count_over_time(%s) >= %d\n  )", sub, ns, sub, normalMinPoints)
	expr := fmt.Sprintf("max %s (%s) > %s\n> on (%s) group_left ()\n  %s * %s",
		by(ns, t.PodLabel), rate, num(CPUAboveNormalMinCores), ns, num(p.Factor), normal)
	r := p.rule(AlertCPUAboveNormal, SignalSaturation, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("CPU far above normal: %s pod %s", p.Service, labelRef(t.PodLabel)),
		fmt.Sprintf(`CPU usage is {{ $value | printf "%%.2f" }} cores over the last %s, more than %s cores and %sx the normal usage (average over the previous %s).`,
			dur(t.Window), num(CPUAboveNormalMinCores), num(p.Factor), dur(NormalRange)))
	ph := p.phrases()
	value := `{{ $value | printf "%.2f" }} ` + ph.cores
	if !containsBacktick(normal) {
		value += " (" + ph.normally + " {{ range query `" + normal + "` }}{{ . | value | printf \"%.2f\" }}{{ end }})"
	}
	p.explain(&r, ph.pod+" "+labelRef(t.PodLabel), value, fmt.Sprintf("%s× %s, %s %s %s", num(p.Factor), ph.normalWord, ph.moreThan, num(CPUAboveNormalMinCores), ph.cores))
	return r
}

// LatencyAboveNormalParams parameterize the "slower than normal" template.
type LatencyAboveNormalParams struct {
	Meta
	Target SpanTarget
	// Quantile, for example 0.95.
	Quantile float64
	// Factor: the alert fires above Factor × normal.
	Factor float64
}

// LatencyAboveNormal renders an alert on a service whose latency quantile is
// above LatencyAboveNormalMinMs and more than Factor times its normal: the
// same quantile over NormalRange ending NormalOffset ago (with at least 30
// calls in that hour). The window must hold MinCallsInWindow calls. The
// value is the current quantile.
func LatencyAboveNormal(p LatencyAboveNormalParams) rulefmt.Rule {
	t := p.Target
	groupBy := t.groupBy(false)
	g := joinComma(groupBy)
	floor := float64(LatencyAboveNormalMinMs)
	valueText := `{{ $value | printf "%.0f" }} ms`
	localValue := `{{ $value | printf "%.0f" }} ` + p.phrases().ms
	if t.DurationInSeconds {
		floor /= 1000
		valueText = `{{ $value | humanizeDuration }}`
		localValue = valueText
	}
	off := " offset " + dur(NormalOffset)
	normal := fmt.Sprintf("(\n    %s\n  and on (%s)\n    %s >= %d\n  )",
		t.quantile(p.Quantile, groupBy, "increase", NormalRange, off), g,
		fmt.Sprintf("sum %s (increase(%s[%s]%s))", by(groupBy...), t.matchers().Selector(t.CallsMetric), dur(NormalRange), off), latencyNormalMinCalls)
	expr := fmt.Sprintf("%s > %s\n> on (%s)\n  %s * %s\nand on (%s)\n(\n  %s >= %d\n)",
		t.quantile(p.Quantile, groupBy, "rate", t.Window, ""), num(floor), g, num(p.Factor), normal,
		g, t.callIncrease(t.matchers(), groupBy, t.Window), MinCallsInWindow)
	r := p.rule(AlertLatencyAboveNormal, SignalLatency, expr, nil)
	p.annotate(&r,
		fmt.Sprintf("Latency far above normal: %s", p.Service),
		fmt.Sprintf("p%s latency is %s over the last %s, more than %s ms and %sx the normal (the same quantile over the previous %s).",
			num(p.Quantile*100), valueText, dur(t.Window), num(LatencyAboveNormalMinMs), num(p.Factor), dur(NormalRange)))
	ph := p.phrases()
	value := "p" + num(p.Quantile*100) + " " + localValue
	if !containsBacktick(normal) {
		unit := ` | printf "%.0f" }} ` + ph.ms
		if t.DurationInSeconds {
			unit = ` | humanizeDuration }}`
		}
		value += " (" + ph.normally + " {{ range query `" + normal + "` }}{{ . | value" + unit + "{{ end }})"
	}
	p.explain(&r, "", value, fmt.Sprintf("%s× %s, %s %s %s", num(p.Factor), ph.normalWord, ph.moreThan, num(LatencyAboveNormalMinMs), ph.ms))
	return r
}

// QueueConsumerStoppedParams parameterize the "nobody reads the queue" template.
type QueueConsumerStoppedParams struct {
	Meta
	// Target selects the topic and its consumer groups. MembersMetric and
	// OffsetMetric turn on the two conditions; at least one must be set.
	Target QueueTarget
}

func metricOn(name string) bool { return name != "" && name != "-" }

// QueueConsumerStopped renders an alert on a consumer group that stopped
// reading the topic, whatever the lag threshold and the load: either
//
//   - the group has had no members for QueueNoMembersRange (the consumer is
//     down or left the group), or
//   - messages have been waiting (lag above zero) for all of
//     QueueStalledRange and no partition of the group moved its committed
//     offset in that time (the consumer is hung).
//
// A group without members may stop reporting lag and offsets, so the first
// condition uses only the members series; with a list of consumer groups it
// does not need any series of the topic at all. The offset series must have
// a recent sample, so a gap in the data does not look like a stopped offset.
// The value is the lag of the group, or 0 when the lag series is gone.
func QueueConsumerStopped(p QueueConsumerStoppedParams) rulefmt.Rule {
	t := p.Target
	g, tp := t.GroupLabel, t.TopicLabel
	gt := joinComma([]string{g, tp})
	topic := Matchers{}.Eq(tp, t.Topic).Re(g, quoteAll(t.Groups)...)
	var conds []string
	noMembers := ""
	if metricOn(t.MembersMetric) {
		zero := fmt.Sprintf("max by (%s) (max_over_time(%s[%s])) == 0", g, Matchers{}.Re(g, quoteAll(t.Groups)...).Selector(t.MembersMetric), dur(QueueNoMembersRange))
		if len(t.Groups) > 0 {
			// The members series has no topic label: the groups are listed, so the topic is added as is.
			noMembers = fmt.Sprintf("label_replace(%s, %q, %q, \"\", \"\")", zero, tp, t.Topic)
		} else {
			noMembers = fmt.Sprintf("group by (%s) (last_over_time(%s[%s]))\n    and on (%s)\n    %s", gt, topic.Selector(t.LagMetric), dur(queueGroupsRange), g, zero)
		}
		conds = append(conds, noMembers)
	}
	if metricOn(t.OffsetMetric) {
		off := topic.Selector(t.OffsetMetric)
		conds = append(conds, fmt.Sprintf("max by (%s) (min_over_time(%s[%s])) > 0\n    and on (%s)\n    sum by (%s) (changes(%s[%s])) == 0\n    and on (%s)\n    group by (%s) (present_over_time(%s[%s]))",
			gt, topic.Selector(t.LagMetric), dur(QueueStalledRange),
			gt, gt, off, dur(QueueStalledRange),
			gt, gt, off, dur(queueFreshRange)))
	}
	expr := fmt.Sprintf("max by (%s) (%s)\nand on (%s)\n(\n    %s\n)", gt, topic.Selector(t.LagMetric), gt, strings.Join(conds, "\n  or\n    "))
	if noMembers != "" {
		expr += fmt.Sprintf("\nor on (%s)\n0 * (\n    %s\n)", gt, noMembers)
	}

	r := p.rule(AlertQueueConsumerStopped, SignalQueueLag, expr, nil)
	var why, local []string
	ph := p.phrases()
	if metricOn(t.MembersMetric) {
		why = append(why, fmt.Sprintf("it has had no members for %s", dur(QueueNoMembersRange)))
		local = append(local, fmt.Sprintf(ph.noMembers, dur(QueueNoMembersRange)))
	}
	if metricOn(t.OffsetMetric) {
		why = append(why, fmt.Sprintf("messages have been waiting for %s while its committed offset did not move", dur(QueueStalledRange)))
		local = append(local, fmt.Sprintf(ph.offsetStill, dur(QueueStalledRange)))
	}
	p.annotate(&r,
		fmt.Sprintf("Consumer stopped reading topic %s: group %s", t.Topic, labelRef(g)),
		fmt.Sprintf(`Consumer group %s stopped reading topic %s: %s. Consumer lag is {{ $value | printf "%%.0f" }}.`, labelRef(g), t.Topic, strings.Join(why, ", or ")))
	p.explain(&r, t.Topic+", "+ph.group+" "+labelRef(g), `{{ $value | printf "%.0f" }} `+ph.msg, strings.Join(local, " "+ph.or+" "))
	return r
}

func containsBacktick(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			return true
		}
	}
	return false
}
