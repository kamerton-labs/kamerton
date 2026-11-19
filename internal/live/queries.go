// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package live is the system as it is now: the state of its services, their numbers over time,
// the requests that failed and what was written while they ran. It reads Prometheus, Tempo and
// Loki and keeps nothing.
package live

import (
	"fmt"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// Queries are the PromQL of one service, built from its description the way its rules are, so
// the numbers shown are the numbers alerted on. An empty query means the signal is off.
type Queries struct {
	svc      *v1alpha1.Service
	requests bool
	span     rules.SpanTarget
	pods     bool
	pod      rules.PodTarget
	queue    bool
	q        rules.QueueTarget
}

// For builds the queries of svc.
func For(sd *v1alpha1.SystemDescription, svc *v1alpha1.Service) Queries {
	q := Queries{svc: svc, requests: generator.HasRequests(sd, svc), span: generator.SpanTarget(sd, svc), pod: generator.PodTarget(sd, svc)}
	q.pods = q.pod.MemoryMetric != description.DisabledMetric && q.pod.MemoryMetric != ""
	q.q, q.queue = generator.QueueTarget(sd, svc)
	return q
}

func (q Queries) sel() rules.Matchers {
	t := q.span
	return rules.Matchers{}.Eq(t.ServiceLabel, t.ServiceValue).Re(t.SpanKindLabel, rules.QuoteValues(t.SpanKinds)...).NotRe(t.OperationLabel, t.ExcludeOperations...)
}

func (q Queries) errSel() rules.Matchers {
	return append(q.sel(), rules.Matchers{}.Eq(q.span.StatusCodeLabel, q.span.ErrorStatusValue)...)
}

func by(labels ...string) string {
	out := ""
	for i, l := range labels {
		if l == "" {
			continue
		}
		if i > 0 && out != "" {
			out += ", "
		}
		out += l
	}
	if out == "" {
		return ""
	}
	return "by (" + out + ") "
}

// Requests per second; per operation when op is set.
func (q Queries) Requests(w time.Duration, perOp bool) string {
	if !q.requests {
		return ""
	}
	g := ""
	if perOp {
		g = by(q.span.OperationLabel)
	}
	return fmt.Sprintf("sum %s(rate(%s[%s]))", g, q.sel().Selector(q.span.CallsMetric), rules.FormatDuration(w))
}

// ErrorRatio is the share of requests that failed, 0..1.
func (q Queries) ErrorRatio(w time.Duration, perOp bool) string {
	if !q.requests {
		return ""
	}
	g := ""
	if perOp {
		g = by(q.span.OperationLabel)
	}
	d := rules.FormatDuration(w)
	// "or 0" keeps a ratio of 0 where requests went through and none failed.
	return fmt.Sprintf("(sum %[1]s(rate(%[2]s[%[4]s])) or 0 * sum %[1]s(rate(%[3]s[%[4]s]))) / sum %[1]s(rate(%[3]s[%[4]s]))",
		g, q.errSel().Selector(q.span.CallsMetric), q.sel().Selector(q.span.CallsMetric), d)
}

// Latency is the quantile of request duration in milliseconds.
func (q Queries) Latency(quantile float64, w time.Duration, perOp bool) string {
	if !q.requests || q.span.BucketMetric == description.DisabledMetric || q.span.BucketMetric == "" {
		return ""
	}
	g := "le"
	if perOp {
		g = q.span.OperationLabel + ", le"
	}
	expr := fmt.Sprintf("histogram_quantile(%s, sum by (%s) (rate(%s[%s])))", rules.FormatFloat(quantile), g, q.sel().Selector(q.span.BucketMetric), rules.FormatDuration(w))
	if q.span.DurationInSeconds {
		expr = "1000 * " + expr
	}
	return expr
}

func (q Queries) podSel() rules.Matchers {
	m := rules.Matchers{}
	if q.pod.Namespace != "" {
		m = m.Eq(q.pod.NamespaceLabel, q.pod.Namespace)
	}
	return m.Re(q.pod.PodLabel, q.pod.PodRegex)
}

// Memory of each pod in bytes.
func (q Queries) Memory() string {
	if !q.pods {
		return ""
	}
	return fmt.Sprintf("max by (%s) (%s)", q.pod.PodLabel, q.podSel().Selector(q.pod.MemoryMetric))
}

// MemoryLimit of each pod in bytes, where every container has one.
func (q Queries) MemoryLimit() string {
	if !q.pods || q.pod.LimitsMetric == "" || q.pod.LimitsMetric == description.DisabledMetric {
		return ""
	}
	return fmt.Sprintf("max by (%s) (%s)", q.pod.PodLabel, q.pod.Limit("memory"))
}

// CPU of each pod in cores.
func (q Queries) CPU(w time.Duration) string {
	if !q.pods || q.pod.CPUMetric == "" || q.pod.CPUMetric == description.DisabledMetric {
		return ""
	}
	return fmt.Sprintf("max by (%s) (rate(%s[%s]))", q.pod.PodLabel, q.podSel().Selector(q.pod.CPUMetric), rules.FormatDuration(w))
}

// Restarts of the service's containers over the range.
func (q Queries) Restarts(r time.Duration) string {
	if !q.pods || q.pod.RestartsMetric == "" || q.pod.RestartsMetric == description.DisabledMetric {
		return ""
	}
	return fmt.Sprintf("sum (increase(%s[%s]))", q.podSel().Selector(q.pod.RestartsMetric), rules.FormatDuration(r))
}

// QueueLag of each consumer group of the queue.
func (q Queries) QueueLag() string {
	if !q.queue {
		return ""
	}
	m := rules.Matchers{}.Eq(q.q.TopicLabel, q.q.Topic).Re(q.q.GroupLabel, rules.QuoteValues(q.q.Groups)...)
	return fmt.Sprintf("max by (%s) (%s)", q.q.GroupLabel, m.Selector(q.q.LagMetric))
}

// OperationLabel names the label of operations, for reading results per operation.
func (q Queries) OperationLabel() string { return q.span.OperationLabel }

// PodLabel names the label of pods.
func (q Queries) PodLabel() string { return q.pod.PodLabel }

// GroupLabel names the label of consumer groups.
func (q Queries) GroupLabel() string { return q.q.GroupLabel }
