// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package generator turns a system description into the artifacts Kamerton
// manages: Prometheus alerting rules, Grafana dashboards, Alertmanager routing
// and runbook stubs. It is pure: no I/O, no cluster access. Applying the
// result is the job of package deployer.
package generator

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// Built-in policy values used when neither defaults nor the service set them.
const (
	builtinErrorThreshold   = 0.05
	builtinMinRPS           = 0.05
	builtinLatencyMinRPS    = 0 // latency rules need rules.MinCallsInWindow calls instead
	builtinLatencyFactor    = 5
	builtinMemoryLimitShare = 0.95
	builtinCPULimitShare    = 0.9
	builtinCPUFactor        = 3
	// The per-operation latency and "slower than normal" rules have their own
	// pending periods (shorter ones in the policy win): rare operations give
	// few samples, and a slowdown should page within five minutes.
	builtinOperationLatencyFor = 30 * time.Second
	builtinAboveNormalFor      = time.Minute
	builtinErrorFor            = 1 * time.Minute
	builtinLatencyQuantile     = 0.95
	builtinLatencyMs           = 1000
	builtinLatencyFor          = 2 * time.Minute
	builtinSaturationFor       = 2 * time.Minute
	builtinMaxRestarts         = 3
	builtinQueueFor            = 2 * time.Minute
	// The stopped consumer rule waits a minute (a shorter queue "for" wins):
	// its conditions already span one to three minutes.
	builtinQueueStoppedFor   = time.Minute
	builtinCustomFor         = 2 * time.Minute
	builtinTrafficThreshold  = 0.1
	builtinTrafficMinRPS     = 0.02
	builtinTrafficFor        = 2 * time.Minute
	builtinSLOWindow         = 30 * 24 * time.Hour
	builtinRepeatInterval    = "4h"
	defaultPodRegexSuffix    = `-([a-z0-9]+-[a-z0-9]{5}|[0-9]+)` // Deployment or StatefulSet pod names
	LabelSystem              = "kamerton_system"
	LabelEnvironment         = "environment"
	runbookAnnotationPathFmt = "%s/runbooks/%s"
)

// Options tune generation.
type Options struct {
	// RunbookBaseURL is Kamerton's external URL. When set, generated
	// runbook_url annotations point to <RunbookBaseURL>/runbooks/<service>/<alert>
	// (a page of the UI) for services without runbookURL, and notification
	// messages link to the alert and the runbook in the UI. When empty,
	// notification messages contain no links at all.
	RunbookBaseURL string
	// Now is the time of generation, used for the daylight-saving changes
	// of the notification time zone; zero means time.Now().
	Now time.Time
	// Webhook, when set, adds a route that also notifies Kamerton about
	// every group (see WebhookOptions). Nil: no webhook.
	Webhook *WebhookOptions
}

// Bundle is everything generated from one description.
type Bundle struct {
	System string
	// RuleGroups are the alerting rules, one group per service.
	RuleGroups rulefmt.RuleGroups
	// RulesYAML is RuleGroups rendered as a Prometheus rule file and
	// validated with Prometheus' rulefmt parser.
	RulesYAML []byte
	// RuleCount is the number of alerting rules.
	RuleCount int
	// Dashboards maps a file name to Grafana dashboard JSON.
	Dashboards map[string][]byte
	// AlertmanagerConfig is the spec of a prometheus-operator
	// AlertmanagerConfig (monitoring.coreos.com/v1alpha1), or nil when the
	// description has no notifications.
	AlertmanagerConfig map[string]any
	// Runbooks maps a file name to a Markdown runbook stub.
	Runbooks map[string]string
}

// Generate builds the bundle for a valid description. Callers must validate
// the description first (description.Validate or description.ValidateSpec).
func Generate(sd *v1alpha1.SystemDescription, opts Options) (*Bundle, error) {
	g := newGen(sd, opts)
	b := &Bundle{System: sd.Name, Dashboards: map[string][]byte{}, Runbooks: map[string]string{}}
	for i := range sd.Spec.Services {
		svc := &sd.Spec.Services[i]
		rs, err := g.serviceRules(svc)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", svc.Name, err)
		}
		if len(rs) == 0 {
			continue
		}
		b.RuleGroups.Groups = append(b.RuleGroups.Groups, rulefmt.RuleGroup{
			Name:  fmt.Sprintf("kamerton/%s/%s", sd.Name, svc.Name),
			Rules: rs,
		})
	}
	out, err := rules.RenderAndValidate(b.RuleGroups)
	if err != nil {
		return nil, fmt.Errorf("generated rules failed Prometheus validation (this is a Kamerton bug): %w", err)
	}
	b.RulesYAML = out
	b.RuleCount = rules.Count(b.RuleGroups)

	dash, err := g.dashboard()
	if err != nil {
		return nil, err
	}
	b.Dashboards[fmt.Sprintf("kamerton-%s.json", sd.Name)] = dash
	b.AlertmanagerConfig = g.alertmanagerConfig()
	for _, rg := range b.RuleGroups.Groups {
		svc := g.service(rg.Name)
		b.Runbooks[svc.Name+".md"] = runbook(sd, svc, rg.Rules)
	}
	return b, nil
}

type gen struct {
	sd     *v1alpha1.SystemDescription
	spec   *v1alpha1.SystemDescriptionSpec
	tel    description.ResolvedTelemetry
	window time.Duration
	opts   Options
	byName map[string]*v1alpha1.Service
}

func newGen(sd *v1alpha1.SystemDescription, opts Options) *gen {
	g := &gen{
		sd:     sd,
		spec:   &sd.Spec,
		tel:    description.ResolveTelemetry(sd.Spec.Telemetry),
		opts:   opts,
		byName: map[string]*v1alpha1.Service{},
	}
	g.window = mustDuration(string(description.RateWindow(g.spec)), 2*time.Minute)
	for i := range sd.Spec.Services {
		s := &sd.Spec.Services[i]
		g.byName[s.Name] = s
		g.byName[fmt.Sprintf("kamerton/%s/%s", sd.Name, s.Name)] = s
	}
	return g
}

// language of notification text: the description's notifications.language, default en.
func (g *gen) language() string {
	if n := g.spec.Notifications; n != nil && n.Language != "" {
		return n.Language
	}
	return rules.LanguageEnglish
}

func (g *gen) now() time.Time {
	if g.opts.Now.IsZero() {
		return time.Now()
	}
	return g.opts.Now
}

func (g *gen) service(groupName string) *v1alpha1.Service { return g.byName[groupName] }

// SpanTarget returns the span-metric selection the request rules of a
// service use (metric and label names, excluded operations, rate window),
// so other code queries exactly what the rules alert on.
func SpanTarget(sd *v1alpha1.SystemDescription, svc *v1alpha1.Service) rules.SpanTarget {
	return newGen(sd, Options{}).spanTarget(svc)
}

// PodTarget returns the pod selection the saturation rules of a service use:
// the namespace (workload.namespace, else defaults.namespace; empty: any)
// and the pod name regex (workload.podRegex, else the service name followed
// by a Deployment or StatefulSet pod suffix).
func PodTarget(sd *v1alpha1.SystemDescription, svc *v1alpha1.Service) rules.PodTarget {
	return newGen(sd, Options{}).podTarget(svc)
}

func (g *gen) defaults() *v1alpha1.Defaults {
	if g.spec.Defaults == nil {
		return &v1alpha1.Defaults{}
	}
	return g.spec.Defaults
}

func (g *gen) meta(svc *v1alpha1.Service, forD time.Duration) rules.Meta {
	sev := string(v1alpha1.SeverityWarning)
	if svc.Critical {
		sev = string(v1alpha1.SeverityCritical)
	}
	lbls := map[string]string{LabelSystem: g.sd.Name}
	if g.spec.Environment != "" {
		lbls[LabelEnvironment] = g.spec.Environment
	}
	runbook, perAlert := svc.RunbookURL, false
	if runbook == "" && g.opts.RunbookBaseURL != "" {
		runbook, perAlert = fmt.Sprintf(runbookAnnotationPathFmt, strings.TrimRight(g.opts.RunbookBaseURL, "/"), svc.Name), true
	}
	return rules.Meta{Service: svc.Name, Severity: sev, For: forD, RunbookURL: runbook, Labels: lbls, RunbookPerAlert: perAlert, Language: g.language()}
}

// metaSev is meta with the severity set by a policy (empty keeps the
// service's default).
func (g *gen) metaSev(svc *v1alpha1.Service, forD time.Duration, sev v1alpha1.Severity) rules.Meta {
	m := g.meta(svc, forD)
	if sev != "" {
		m.Severity = string(sev)
	}
	return m
}

func (g *gen) spanTarget(svc *v1alpha1.Service) rules.SpanTarget {
	s := g.tel.Spans
	exclude := append(append([]string{}, g.defaults().ExcludeOperations...), svc.ExcludeOperations...)
	return rules.SpanTarget{
		CallsMetric:       s.CallsMetric,
		BucketMetric:      s.DurationBucketMetric,
		DurationInSeconds: s.DurationUnit == "s",
		ServiceLabel:      s.ServiceLabel,
		OperationLabel:    s.OperationLabel,
		SpanKindLabel:     s.SpanKindLabel,
		StatusCodeLabel:   s.StatusCodeLabel,
		ErrorStatusValue:  s.ErrorStatusValue,
		SpanKinds:         s.ServerSpanKinds,
		ServiceValue:      description.TelemetryName(svc),
		ExcludeOperations: exclude,
		Window:            g.window,
	}
}

func (g *gen) podTarget(svc *v1alpha1.Service) rules.PodTarget {
	c := g.tel.Containers
	ns := g.defaults().Namespace
	podRegex := regexp.QuoteMeta(svc.Name) + defaultPodRegexSuffix
	if w := svc.Workload; w != nil {
		if w.Namespace != "" {
			ns = w.Namespace
		}
		if w.PodRegex != "" {
			podRegex = w.PodRegex
		}
	}
	return rules.PodTarget{
		MemoryMetric:     c.MemoryMetric,
		CPUMetric:        c.CPUMetric,
		RestartsMetric:   c.RestartsMetric,
		LimitsMetric:     c.LimitsMetric,
		TerminatedMetric: c.TerminatedReasonMetric,
		NamespaceLabel:   c.NamespaceLabel,
		PodLabel:         c.PodLabel,
		ContainerLabel:   c.ContainerLabel,
		Namespace:        ns,
		PodRegex:         podRegex,
		Window:           g.window,
	}
}

// hasRequestRules says whether span-based request rules apply: always for
// http/grpc services; for queue/database only when explicitly configured.
func hasRequestRules(svc *v1alpha1.Service) bool {
	switch svc.Type {
	case v1alpha1.ServiceTypeHTTP, v1alpha1.ServiceTypeGRPC:
		return true
	default:
		return svc.ErrorRate != nil || svc.Latency != nil || len(svc.Operations) > 0 || svc.SLO != nil
	}
}

func (g *gen) serviceRules(svc *v1alpha1.Service) ([]rulefmt.Rule, error) {
	var out []rulefmt.Rule
	d := g.defaults()
	spansOn := g.tel.Spans.CallsMetric != description.DisabledMetric

	if hasRequestRules(svc) && spansOn {
		target := g.spanTarget(svc)

		// Error rate, service level and optionally per operation.
		if ep := mergeErrorRate(d.ErrorRate, svc.ErrorRate); ep.enabled {
			out = append(out, g.withMessage(rules.ErrorRate(rules.ErrorRateParams{
				Meta: g.metaSev(svc, ep.forD, ep.severity), Target: target,
				Threshold: ep.threshold, MinRequestsPerSecond: ep.minRPS,
			}), svc, ep.message, ""))
			if ep.perOperation {
				// Operations with their own policy are evaluated separately.
				perOp := withoutOperations(target, svc, func(op v1alpha1.Operation) bool { return op.ErrorRate != nil })
				out = append(out, g.withMessage(rules.ErrorRate(rules.ErrorRateParams{
					Meta: g.metaSev(svc, ep.forD, ep.severity), Target: perOp,
					Threshold: ep.threshold, PerOperation: true,
					MinErrors: rules.MinCallsInWindow, SingleOperationRPS: ep.minRPS,
				}), svc, ep.message, operationRef("", g.tel.Spans.OperationLabel, true)))
			}
		}
		// Latency, service level.
		if lp := mergeLatency(d.Latency, svc.Latency); lp.enabled && g.tel.Spans.DurationBucketMetric != description.DisabledMetric {
			out = append(out, g.withMessage(rules.Latency(rules.LatencyParams{
				Meta: g.metaSev(svc, lp.forD, lp.severity), Target: target,
				Quantile: lp.quantile, ThresholdMs: lp.thresholdMs, MinRequestsPerSecond: lp.minRPS, MinCalls: rules.MinCallsInWindow,
			}), svc, lp.message, ""))
			if lp.perOperation {
				perOp := withoutOperations(target, svc, func(op v1alpha1.Operation) bool { return op.Latency != nil })
				out = append(out, g.withMessage(rules.Latency(rules.LatencyParams{
					Meta: g.metaSev(svc, min(lp.forD, builtinOperationLatencyFor), lp.severity), Target: perOp,
					Quantile: lp.quantile, ThresholdMs: lp.thresholdMs, MinCalls: rules.MinCallsInWindow, PerOperation: true,
				}), svc, lp.message, operationRef("", g.tel.Spans.OperationLabel, true)))
			}
			if lp.factor > 0 {
				out = append(out, g.withMessage(rules.LatencyAboveNormal(rules.LatencyAboveNormalParams{
					Meta: g.metaSev(svc, min(lp.forD, builtinAboveNormalFor), lp.severity), Target: target, Quantile: lp.quantile, Factor: lp.factor,
				}), svc, lp.message, ""))
			}
		}
		// Explicit operations.
		for _, op := range svc.Operations {
			opTarget := target
			opTarget.Operation = op.Name
			if op.ErrorRate != nil {
				if ep := mergeErrorRate(d.ErrorRate, svc.ErrorRate, op.ErrorRate); ep.enabled {
					out = append(out, g.withMessage(rules.ErrorRate(rules.ErrorRateParams{
						Meta: g.metaSev(svc, ep.forD, ep.severity), Target: opTarget,
						Threshold: ep.threshold, MinRequestsPerSecond: ep.minRPS,
					}), svc, ep.message, operationRef(op.Name, "", false)))
				}
			}
			if op.Latency != nil && g.tel.Spans.DurationBucketMetric != description.DisabledMetric {
				if lp := mergeLatency(d.Latency, svc.Latency, op.Latency); lp.enabled {
					out = append(out, g.withMessage(rules.Latency(rules.LatencyParams{
						Meta: g.metaSev(svc, lp.forD, lp.severity), Target: opTarget,
						Quantile: lp.quantile, ThresholdMs: lp.thresholdMs, MinRequestsPerSecond: lp.minRPS, MinCalls: rules.MinCallsInWindow,
					}), svc, lp.message, operationRef(op.Name, "", false)))
				}
			}
		}
		// SLO burn rate.
		if svc.SLO != nil {
			window := mustDuration(string(svc.SLO.Window), builtinSLOWindow)
			for _, r := range rules.SLOBurnRate(rules.SLOParams{
				Meta: g.meta(svc, 0), Target: target, Objective: svc.SLO.Objective, Window: window,
			}) {
				out = append(out, g.withMessage(r, svc, svc.SLO.Message, ""))
			}
		}
	}

	// Requests stopped arriving.
	if tp := mergeTraffic(svc, d.TrafficDrop, svc.TrafficDrop); tp.enabled && spansOn && trafficApplies(svc) {
		out = append(out, g.withMessage(rules.TrafficStopped(rules.TrafficParams{
			Meta: g.metaSev(svc, tp.forD, tp.severity), Target: g.spanTarget(svc),
			Threshold: tp.threshold, MinRequestsPerSecond: tp.minRPS, SystemServices: g.telemetryNames(),
		}), svc, tp.message, ""))
	}

	// Saturation.
	sp := mergeSaturation(d.Saturation, svc.Saturation)
	if sp.enabled {
		pt := g.podTarget(svc)
		meta := g.metaSev(svc, sp.forD, sp.severity)
		limits := pt.LimitsMetric != description.DisabledMetric
		if pt.MemoryMetric != description.DisabledMetric {
			switch {
			case sp.memoryBytes > 0:
				out = append(out, g.withMessage(rules.Memory(rules.MemoryParams{Meta: meta, Target: pt, Bytes: sp.memoryBytes}), svc, sp.memoryMessage, ""))
			case sp.memoryLimitShare > 0 && limits:
				out = append(out, g.withMessage(rules.MemoryNearLimit(rules.NearLimitParams{Meta: meta, Target: pt, Share: sp.memoryLimitShare}), svc, sp.memoryMessage, ""))
			}
		}
		if pt.CPUMetric != description.DisabledMetric {
			if sp.cpuCores > 0 {
				out = append(out, g.withMessage(rules.CPU(rules.CPUParams{Meta: meta, Target: pt, Cores: sp.cpuCores}), svc, sp.cpuMessage, ""))
			} else {
				if sp.cpuLimitShare > 0 && limits {
					out = append(out, g.withMessage(rules.CPUNearLimit(rules.NearLimitParams{Meta: meta, Target: pt, Share: sp.cpuLimitShare}), svc, sp.cpuMessage, ""))
				}
				if sp.cpuFactor > 0 {
					out = append(out, g.withMessage(rules.CPUAboveNormal(rules.CPUAboveNormalParams{Meta: meta, Target: pt, Factor: sp.cpuFactor}), svc, sp.cpuMessage, ""))
				}
			}
		}
		if sp.restarts && sp.maxRestarts >= 0 && pt.RestartsMetric != description.DisabledMetric {
			out = append(out, g.withMessage(rules.Restarts(rules.RestartsParams{Meta: g.metaSev(svc, 0, sp.severity), Target: pt, MaxPerHour: sp.maxRestarts}), svc, sp.restartsMessage, ""))
		}
		if sp.oomKilled && pt.RestartsMetric != description.DisabledMetric && pt.TerminatedMetric != description.DisabledMetric {
			out = append(out, g.withMessage(rules.OOMKilled(rules.OOMKilledParams{Meta: g.metaSev(svc, 0, sp.severity), Target: pt}), svc, sp.oomKilledMessage, ""))
		}
	}

	// Queue lag.
	if q := svc.Queue; q != nil && g.tel.Queues.LagMetric != description.DisabledMetric {
		qt := g.tel.Queues
		target := rules.QueueTarget{
			LagMetric: qt.LagMetric, MembersMetric: qt.MembersMetric, OffsetMetric: qt.OffsetMetric,
			GroupLabel: qt.GroupLabel, TopicLabel: qt.TopicLabel,
			Topic: q.Topic, Groups: q.ConsumerGroups,
		}
		qfor := mustDuration(string(q.For), builtinQueueFor)
		out = append(out, g.withMessage(rules.QueueLag(rules.QueueLagParams{
			Meta: g.metaSev(svc, qfor, q.Severity), Target: target, MaxLag: q.MaxLag,
		}), svc, q.Message, ""))
		// Nobody reads the queue: on by default, needs no threshold.
		stoppedOn := q.ConsumerStoppedEnabled == nil || *q.ConsumerStoppedEnabled
		if stoppedOn && (qt.MembersMetric != description.DisabledMetric || qt.OffsetMetric != description.DisabledMetric) {
			out = append(out, g.withMessage(rules.QueueConsumerStopped(rules.QueueConsumerStoppedParams{
				Meta: g.metaSev(svc, min(qfor, builtinQueueStoppedFor), q.Severity), Target: target,
			}), svc, q.ConsumerStoppedMessage, ""))
		}
	}

	// Rules written by the user.
	for _, cr := range svc.CustomRules {
		if cr.Enabled != nil && !*cr.Enabled {
			continue
		}
		p := rules.CustomParams{
			Meta: g.metaSev(svc, mustDuration(string(cr.For), builtinCustomFor), cr.Severity),
			Name: cr.Name, Title: cr.Title, Expr: cr.Expr, Unit: cr.Unit,
		}
		if c := cr.Condition; c != nil && cr.Expr == "" {
			p.Condition = &rules.CustomCondition{
				Metric: c.Metric, Labels: c.Labels, Function: c.Function, Aggregation: c.Aggregation,
				By: c.By, Operator: c.Operator, Threshold: c.Threshold, Window: g.window,
			}
		}
		out = append(out, g.withMessage(rules.Custom(p), svc, cr.Message, ""))
	}
	return out, nil
}

// withoutOperations excludes the operations that have their own policy
// (own(op)) from a per-operation target.
func withoutOperations(t rules.SpanTarget, svc *v1alpha1.Service, own func(v1alpha1.Operation) bool) rules.SpanTarget {
	t.ExcludeOperations = append([]string{}, t.ExcludeOperations...)
	for _, op := range svc.Operations {
		if own(op) {
			t.ExcludeOperations = append(t.ExcludeOperations, rules.QuoteOperation(op.Name))
		}
	}
	return t
}

// trafficApplies says whether the "requests stopped arriving" rule can apply:
// http and grpc services receive calls as server or consumer spans of their
// own. Queues and databases usually appear only as client or producer spans
// of other services, so for them the rule exists only when the service
// itself sets trafficDrop (for example a database behind an instrumented proxy).
func trafficApplies(svc *v1alpha1.Service) bool {
	switch svc.Type {
	case v1alpha1.ServiceTypeHTTP, v1alpha1.ServiceTypeGRPC:
		return true
	default:
		return svc.TrafficDrop != nil
	}
}

// telemetryNames are the service label values of all services of the system.
func (g *gen) telemetryNames() []string {
	out := make([]string, 0, len(g.spec.Services))
	for i := range g.spec.Services {
		out = append(out, description.TelemetryName(&g.spec.Services[i]))
	}
	return out
}

type traffic struct {
	enabled           bool
	threshold, minRPS float64
	forD              time.Duration
	severity          v1alpha1.Severity
	message           *v1alpha1.NotificationMessage
}

// mergeTraffic merges the traffic policy layers. The rule is on by default
// only for critical services: a page about a quiet service nobody depends on
// is noise, and the services worth waking someone for are marked critical.
func mergeTraffic(svc *v1alpha1.Service, layers ...*v1alpha1.TrafficDropPolicy) traffic {
	t := traffic{enabled: svc.Critical, threshold: builtinTrafficThreshold, minRPS: builtinTrafficMinRPS, forD: builtinTrafficFor}
	for _, l := range layers {
		if l == nil {
			continue
		}
		if l.Enabled != nil {
			t.enabled = *l.Enabled
		}
		if l.Threshold != nil {
			t.threshold = *l.Threshold
		}
		if l.MinRequestsPerSecond != nil {
			t.minRPS = *l.MinRequestsPerSecond
		}
		if l.For != "" {
			t.forD = mustDuration(string(l.For), t.forD)
		}
		if l.Severity != "" {
			t.severity = l.Severity
		}
		t.message = lastMessage(t.message, l.Message)
	}
	return t
}

type errorRate struct {
	enabled, perOperation bool
	threshold, minRPS     float64
	forD                  time.Duration
	severity              v1alpha1.Severity
	message               *v1alpha1.NotificationMessage
}

func mergeErrorRate(layers ...*v1alpha1.ErrorRatePolicy) errorRate {
	e := errorRate{enabled: true, perOperation: true, threshold: builtinErrorThreshold, minRPS: builtinMinRPS, forD: builtinErrorFor}
	for _, l := range layers {
		if l == nil {
			continue
		}
		if l.Enabled != nil {
			e.enabled = *l.Enabled
		}
		if l.Threshold != nil {
			e.threshold = *l.Threshold
		}
		if l.MinRequestsPerSecond != nil {
			e.minRPS = *l.MinRequestsPerSecond
		}
		if l.For != "" {
			e.forD = mustDuration(string(l.For), e.forD)
		}
		if l.PerOperation != nil {
			e.perOperation = *l.PerOperation
		}
		if l.Severity != "" {
			e.severity = l.Severity
		}
		e.message = lastMessage(e.message, l.Message)
	}
	return e
}

type latency struct {
	enabled, perOperation         bool
	quantile, thresholdMs, minRPS float64
	factor                        float64
	forD                          time.Duration
	severity                      v1alpha1.Severity
	message                       *v1alpha1.NotificationMessage
}

func mergeLatency(layers ...*v1alpha1.LatencyPolicy) latency {
	l := latency{enabled: true, perOperation: true, quantile: builtinLatencyQuantile, thresholdMs: builtinLatencyMs, minRPS: builtinLatencyMinRPS, factor: builtinLatencyFactor, forD: builtinLatencyFor}
	for _, x := range layers {
		if x == nil {
			continue
		}
		if x.Enabled != nil {
			l.enabled = *x.Enabled
		}
		if x.Quantile != nil {
			l.quantile = *x.Quantile
		}
		if x.ThresholdMs != nil {
			l.thresholdMs = *x.ThresholdMs
		}
		if x.MinRequestsPerSecond != nil {
			l.minRPS = *x.MinRequestsPerSecond
		}
		if x.PerOperation != nil {
			l.perOperation = *x.PerOperation
		}
		if x.AboveNormalFactor != nil {
			l.factor = *x.AboveNormalFactor
		}
		if x.For != "" {
			l.forD = mustDuration(string(x.For), l.forD)
		}
		if x.Severity != "" {
			l.severity = x.Severity
		}
		l.message = lastMessage(l.message, x.Message)
	}
	return l
}

type saturation struct {
	enabled, restarts, oomKilled    bool
	memoryBytes, cpuCores           float64
	memoryLimitShare, cpuLimitShare float64
	cpuFactor                       float64
	maxRestarts                     int
	forD                            time.Duration
	severity                        v1alpha1.Severity

	memoryMessage, cpuMessage, restartsMessage, oomKilledMessage *v1alpha1.NotificationMessage
}

func mergeSaturation(layers ...*v1alpha1.SaturationPolicy) saturation {
	s := saturation{enabled: true, restarts: true, oomKilled: true, maxRestarts: builtinMaxRestarts, forD: builtinSaturationFor,
		memoryLimitShare: builtinMemoryLimitShare, cpuLimitShare: builtinCPULimitShare, cpuFactor: builtinCPUFactor}
	for _, x := range layers {
		if x == nil {
			continue
		}
		if x.Enabled != nil {
			s.enabled = *x.Enabled
		}
		if x.Memory != "" {
			if q, err := resource.ParseQuantity(x.Memory); err == nil {
				s.memoryBytes = q.AsApproximateFloat64()
			}
		}
		if x.CPUCores != nil {
			s.cpuCores = *x.CPUCores
		}
		if x.MaxRestartsPerHour != nil {
			s.maxRestarts = int(*x.MaxRestartsPerHour)
		}
		if x.RestartsEnabled != nil {
			s.restarts = *x.RestartsEnabled
		}
		if x.OOMKilledEnabled != nil {
			s.oomKilled = *x.OOMKilledEnabled
		}
		if x.MemoryLimitShare != nil {
			s.memoryLimitShare = *x.MemoryLimitShare
		}
		if x.CPULimitShare != nil {
			s.cpuLimitShare = *x.CPULimitShare
		}
		if x.CPUAboveNormalFactor != nil {
			s.cpuFactor = *x.CPUAboveNormalFactor
		}
		if x.For != "" {
			s.forD = mustDuration(string(x.For), s.forD)
		}
		if x.Severity != "" {
			s.severity = x.Severity
		}
		s.memoryMessage = lastMessage(s.memoryMessage, x.MemoryMessage)
		s.cpuMessage = lastMessage(s.cpuMessage, x.CPUMessage)
		s.restartsMessage = lastMessage(s.restartsMessage, x.RestartsMessage)
		s.oomKilledMessage = lastMessage(s.oomKilledMessage, x.OOMKilledMessage)
	}
	return s
}

func mustDuration(s string, def time.Duration) time.Duration {
	d, err := rules.ParseDuration(s, def)
	if err != nil {
		return def
	}
	return d
}
