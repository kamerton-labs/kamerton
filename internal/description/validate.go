// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package description

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// ValidateSpec runs semantic checks that a JSON Schema cannot express. It
// assumes the spec already passed schema validation (types, enums, patterns).
// It is also used by the controller for custom resources, where the API
// server has already enforced the CRD schema.
func ValidateSpec(spec *v1alpha1.SystemDescriptionSpec) []Issue {
	v := &validator{}
	v.spec(spec)
	return v.issues
}

type validator struct {
	issues []Issue
}

func (v *validator) errf(path, format string, args ...any) {
	v.issues = append(v.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...), Severity: SeverityError})
}

func (v *validator) warnf(path, format string, args ...any) {
	v.issues = append(v.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...), Severity: SeverityWarning})
}

func (v *validator) duration(path string, d v1alpha1.Duration) (time.Duration, bool) {
	if d == "" {
		return 0, false
	}
	md, err := model.ParseDuration(string(d))
	if err != nil {
		v.errf(path, "invalid duration %q: %v", d, err)
		return 0, false
	}
	return time.Duration(md), true
}

func (v *validator) regex(path, expr string) {
	// Prometheus anchors label regular expressions; compile the same way.
	if _, err := regexp.Compile("^(?:" + expr + ")$"); err != nil {
		v.errf(path, "invalid regular expression %q: %v", expr, err)
	}
}

func (v *validator) spec(spec *v1alpha1.SystemDescriptionSpec) {
	const base = "/spec"
	if d := spec.Defaults; d != nil {
		p := base + "/defaults"
		if w, ok := v.duration(p+"/rateWindow", d.RateWindow); ok {
			min, _ := model.ParseDuration(string(MinRecommendedRateWindow))
			if w < time.Duration(min) {
				v.warnf(p+"/rateWindow", "rate window %s is shorter than %s; with metrics updated once a minute rate() returns no data", d.RateWindow, MinRecommendedRateWindow)
			}
		}
		for i, e := range d.ExcludeOperations {
			v.regex(fmt.Sprintf("%s/excludeOperations/%d", p, i), e)
		}
		v.errorRate(p+"/errorRate", d.ErrorRate)
		v.latency(p+"/latency", d.Latency)
		v.saturation(p+"/saturation", d.Saturation)
		v.traffic(p+"/trafficDrop", d.TrafficDrop)
	}
	if t := spec.Telemetry; t != nil && t.Spans != nil {
		for i, k := range t.Spans.ServerSpanKinds {
			if k == "" {
				v.errf(fmt.Sprintf("%s/telemetry/spans/serverSpanKinds/%d", base, i), "span kind must not be empty")
			}
		}
	}

	names := make(map[string]int, len(spec.Services))
	for i, s := range spec.Services {
		p := fmt.Sprintf("%s/services/%d", base, i)
		if prev, dup := names[s.Name]; dup {
			v.errf(p+"/name", "duplicate service name %q (also at /spec/services/%d)", s.Name, prev)
			continue
		}
		names[s.Name] = i
	}
	for i := range spec.Services {
		v.service(fmt.Sprintf("%s/services/%d", base, i), &spec.Services[i], names)
	}
	if cycle := findCycle(spec.Services); len(cycle) > 0 {
		msg := "dependency cycle: %v; probable-source search stops at the cycle"
		if spec.Notifications.GroupsByCause() {
			msg += ", and services in the cycle do not mute each other's alerts"
		}
		v.warnf(base+"/services", msg, cycle)
	}
	v.notifications(base+"/notifications", spec)
}

func (v *validator) service(p string, s *v1alpha1.Service, names map[string]int) {
	seen := map[string]bool{}
	for i, dep := range s.DependsOn {
		dp := fmt.Sprintf("%s/dependsOn/%d", p, i)
		switch {
		case dep == s.Name:
			v.errf(dp, "service %q cannot depend on itself", s.Name)
		case seen[dep]:
			v.warnf(dp, "duplicate dependency %q", dep)
		default:
			if _, ok := names[dep]; !ok {
				v.errf(dp, "unknown service %q", dep)
			}
		}
		seen[dep] = true
	}
	if s.Workload != nil && s.Workload.PodRegex != "" {
		v.regex(p+"/workload/podRegex", s.Workload.PodRegex)
	}
	for i, e := range s.ExcludeOperations {
		v.regex(fmt.Sprintf("%s/excludeOperations/%d", p, i), e)
	}
	v.errorRate(p+"/errorRate", s.ErrorRate)
	v.latency(p+"/latency", s.Latency)
	v.saturation(p+"/saturation", s.Saturation)
	v.traffic(p+"/trafficDrop", s.TrafficDrop)
	if s.TrafficDrop != nil && (s.Type == v1alpha1.ServiceTypeQueue || s.Type == v1alpha1.ServiceTypeDatabase) {
		v.warnf(p+"/trafficDrop", "the rule counts server and consumer spans of %q itself; a %s usually appears only in client spans of other services, so check that such spans exist", s.Name, s.Type)
	}
	opNames := map[string]bool{}
	for i, op := range s.Operations {
		pp := fmt.Sprintf("%s/operations/%d", p, i)
		if opNames[op.Name] {
			v.errf(pp+"/name", "duplicate operation %q", op.Name)
		}
		opNames[op.Name] = true
		v.errorRate(pp+"/errorRate", op.ErrorRate)
		v.latency(pp+"/latency", op.Latency)
		if op.ErrorRate == nil && op.Latency == nil {
			v.warnf(pp, "operation %q has no errorRate or latency policy and generates no rules", op.Name)
		}
	}
	if s.Type == v1alpha1.ServiceTypeQueue && s.Queue == nil {
		v.errf(p+"/queue", "service of type queue needs queue.topic and queue.maxLag")
	}
	if s.Queue != nil {
		v.duration(p+"/queue/for", s.Queue.For)
		v.message(p+"/queue/message", s.Queue.Message)
		v.message(p+"/queue/consumerStoppedMessage", s.Queue.ConsumerStoppedMessage)
		if s.Type != v1alpha1.ServiceTypeQueue {
			v.warnf(p+"/queue", "queue settings on a service of type %s; consider a separate service of type queue", s.Type)
		}
	}
	if problem := CheckLinkURL(s.RunbookURL); s.RunbookURL != "" && problem != "" {
		v.errf(p+"/runbookURL", "runbook link: %s", problem)
	}
	v.customRules(p, s)
	if s.SLO != nil {
		v.message(p+"/slo/message", s.SLO.Message)
		w, ok := v.duration(p+"/slo/window", s.SLO.Window)
		if ok && w < 24*time.Hour {
			v.warnf(p+"/slo/window", "SLO window %s is shorter than one day; burn-rate alerts will be noisy", s.SLO.Window)
		}
	}
}

// CheckLinkURL returns the problem with a URL that ends up as a link in
// notifications or the UI, or "": it must be an absolute http or https URL
// with a host and no user name or password.
func CheckLinkURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return "the address is empty"
	case err != nil:
		return fmt.Sprintf("cannot parse the address %q: %v", raw, err)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Sprintf("the address %q must start with http:// or https://", raw)
	case u.Host == "" || u.Hostname() == "":
		return fmt.Sprintf("the address %q has no host name", raw)
	case u.User != nil:
		return fmt.Sprintf("the address %q must not contain a user name or password", raw)
	case strings.ContainsAny(raw, " \t\r\n"):
		return fmt.Sprintf("the address %q contains spaces or line breaks", raw)
	}
	return ""
}

var legacyLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// MessagePlaceholders are the substitutions allowed in notification texts.
var MessagePlaceholders = []string{"value", "threshold", "service", "operation", "system", "since"}

var (
	placeholderRe = regexp.MustCompile(`\{([^{}]*)\}`)
	markupRe      = regexp.MustCompile(`<\s*/?\s*[a-zA-Z!?][^>]*>`)
)

// CheckMessageText returns the problem with one field of a notification
// text, or "": placeholders must be from MessagePlaceholders, braces are only
// for placeholders, and markup is not allowed (the text is shown as written).
func CheckMessageText(text string) string {
	known := map[string]bool{}
	for _, p := range MessagePlaceholders {
		known[p] = true
	}
	for _, m := range placeholderRe.FindAllStringSubmatch(text, -1) {
		if !known[m[1]] {
			return fmt.Sprintf("unknown placeholder {%s}; allowed: {%s}", m[1], strings.Join(MessagePlaceholders, "}, {"))
		}
	}
	if rest := placeholderRe.ReplaceAllString(text, ""); strings.ContainsAny(rest, "{}") {
		return "braces are only for placeholders such as {value}"
	}
	if m := markupRe.FindString(text); m != "" {
		return fmt.Sprintf("markup %q is not allowed; the text is shown as written", m)
	}
	return ""
}

func (v *validator) message(p string, m *v1alpha1.NotificationMessage) {
	if m == nil {
		return
	}
	for field, text := range map[string]string{"title": m.Title, "text": m.Text, "action": m.Action} {
		if problem := CheckMessageText(text); problem != "" {
			v.errf(p+"/"+field, "%s", problem)
		}
	}
}

func (v *validator) customRules(p string, s *v1alpha1.Service) {
	names := map[string]bool{}
	for i, cr := range s.CustomRules {
		cp := fmt.Sprintf("%s/customRules/%d", p, i)
		if names[cr.Name] {
			v.errf(cp+"/name", "duplicate custom rule %q", cr.Name)
		}
		names[cr.Name] = true
		for field, text := range map[string]string{"title": cr.Title, "unit": cr.Unit} {
			if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
				v.errf(cp+"/"+field, "must not contain {{ or }}")
			}
		}
		v.duration(cp+"/for", cr.For)
		v.message(cp+"/message", cr.Message)
		switch {
		case cr.Condition == nil && cr.Expr == "":
			v.errf(cp, "custom rule %q needs a condition or an expr", cr.Name)
		case cr.Condition != nil && cr.Expr != "":
			v.errf(cp, "custom rule %q has both a condition and an expr; keep one", cr.Name)
		case cr.Expr != "":
			if err := rules.ValidateExpr(cr.Expr); err != nil {
				v.errf(cp+"/expr", "%v", err)
			}
		default:
			c := cr.Condition
			for k := range c.Labels {
				if !legacyLabelName.MatchString(k) {
					v.errf(cp+"/condition/labels", "invalid label name %q", k)
				}
			}
			for j, k := range c.By {
				if !legacyLabelName.MatchString(k) {
					v.errf(fmt.Sprintf("%s/condition/by/%d", cp, j), "invalid label name %q", k)
				}
			}
			expr := rules.CustomCondition{Metric: c.Metric, Labels: c.Labels, Function: c.Function, Aggregation: c.Aggregation, By: c.By, Operator: c.Operator, Threshold: c.Threshold}.Expr()
			if err := rules.ValidateExpr(expr); err != nil {
				v.errf(cp+"/condition", "%v", err)
			}
		}
	}
}

func (v *validator) errorRate(p string, e *v1alpha1.ErrorRatePolicy) {
	if e == nil {
		return
	}
	v.duration(p+"/for", e.For)
	v.message(p+"/message", e.Message)
}

func (v *validator) latency(p string, l *v1alpha1.LatencyPolicy) {
	if l == nil {
		return
	}
	v.duration(p+"/for", l.For)
	v.message(p+"/message", l.Message)
}

// minTrafficFor is the shortest "for" of the traffic rule that rides out a
// restart of the telemetry pipeline (series re-created, a sample or two
// missing for some services).
const minTrafficFor = time.Minute

func (v *validator) traffic(p string, t *v1alpha1.TrafficDropPolicy) {
	if t == nil {
		return
	}
	if d, ok := v.duration(p+"/for", t.For); ok && d < minTrafficFor {
		v.warnf(p+"/for", "for %s is shorter than %s; the alert may fire while the telemetry collector restarts", t.For, model.Duration(minTrafficFor))
	}
	v.message(p+"/message", t.Message)
}

func (v *validator) saturation(p string, s *v1alpha1.SaturationPolicy) {
	if s == nil {
		return
	}
	v.duration(p+"/for", s.For)
	v.message(p+"/memoryMessage", s.MemoryMessage)
	v.message(p+"/cpuMessage", s.CPUMessage)
	v.message(p+"/restartsMessage", s.RestartsMessage)
	if s.Memory != "" {
		if _, err := resource.ParseQuantity(s.Memory); err != nil {
			v.errf(p+"/memory", "invalid quantity %q: %v", s.Memory, err)
		}
	}
}

func (v *validator) notifications(p string, spec *v1alpha1.SystemDescriptionSpec) {
	hasCritical := false
	for _, s := range spec.Services {
		if s.Critical {
			hasCritical = true
			break
		}
	}
	n := spec.Notifications
	if n == nil {
		if hasCritical {
			v.warnf(p, "there are critical services but no notification receivers; alerts will only be visible in Alertmanager")
		}
		return
	}
	if _, err := LoadTimeZone(n.TimeZone); err != nil {
		v.errf(p+"/timeZone", "%v", err)
	}
	if len(n.Receivers) == 0 {
		if n.GroupByCause != nil {
			v.warnf(p+"/groupByCause", "groupByCause has no effect without receivers: no Alertmanager configuration is generated")
		}
		if n.EvidenceFollowUp != nil && *n.EvidenceFollowUp {
			v.warnf(p+"/evidenceFollowUp", "evidenceFollowUp has no effect without receivers: a follow-up goes to the receivers of the alert")
		}
	}
	receivers := map[string]bool{}
	for i, r := range n.Receivers {
		rp := fmt.Sprintf("%s/receivers/%d", p, i)
		if receivers[r.Name] {
			v.errf(rp+"/name", "duplicate receiver %q", r.Name)
		}
		receivers[r.Name] = true
		count := 0
		if r.Telegram != nil {
			count++
		}
		if r.Email != nil {
			count++
		}
		if count != 1 {
			v.errf(rp, "receiver %q must have exactly one of telegram or email", r.Name)
		}
	}
	if n.Routing == nil {
		if len(n.Receivers) > 0 {
			v.warnf(p+"/routing", "receivers are defined but routing is empty; no alert will be delivered")
		}
		return
	}
	for i, name := range n.Routing.Critical {
		if !receivers[name] {
			v.errf(fmt.Sprintf("%s/routing/critical/%d", p, i), "unknown receiver %q", name)
		}
	}
	for i, name := range n.Routing.Warning {
		if !receivers[name] {
			v.errf(fmt.Sprintf("%s/routing/warning/%d", p, i), "unknown receiver %q", name)
		}
	}
	v.duration(p+"/routing/repeatInterval", n.Routing.RepeatInterval)
	services := map[string]bool{}
	for _, s := range spec.Services {
		services[s.Name] = true
	}
	inList := func(list []string, name string) bool {
		for _, x := range list {
			if x == name {
				return true
			}
		}
		return false
	}
	routedCritical := len(n.Routing.Critical) > 0
	for i, r := range n.Routing.Routes {
		rp := fmt.Sprintf("%s/routing/routes/%d", p, i)
		if !receivers[r.Receiver] {
			v.errf(rp+"/receiver", "unknown receiver %q", r.Receiver)
		}
		for j, s := range r.Services {
			if !services[s] {
				v.errf(fmt.Sprintf("%s/services/%d", rp, j), "unknown service %q", s)
			}
		}
		sevs := r.Severities
		if len(sevs) == 0 {
			sevs = []v1alpha1.Severity{v1alpha1.SeverityCritical, v1alpha1.SeverityWarning}
		}
		for _, sev := range sevs {
			if sev == v1alpha1.SeverityCritical {
				routedCritical = true
			}
			if (sev == v1alpha1.SeverityCritical && inList(n.Routing.Critical, r.Receiver)) ||
				(sev == v1alpha1.SeverityWarning && inList(n.Routing.Warning, r.Receiver)) {
				v.warnf(rp, "receiver %q already gets all %s alerts; this route sends some of them twice", r.Receiver, sev)
			}
		}
	}
	if hasCritical && !routedCritical {
		v.warnf(p+"/routing/critical", "there are critical services but no receivers for critical alerts")
	}
}

// findCycle returns one dependency cycle (service names) or nil.
func findCycle(services []v1alpha1.Service) []string {
	deps := make(map[string][]string, len(services))
	for _, s := range services {
		deps[s.Name] = s.DependsOn
	}
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var visit func(string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, d := range deps[n] {
			if _, known := deps[d]; !known {
				continue
			}
			switch color[d] {
			case grey:
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i] == d {
						cycle = append(append([]string{}, stack[i:]...), d)
						return true
					}
				}
			case white:
				if visit(d) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, s := range services {
		if color[s.Name] == white && visit(s.Name) {
			return cycle
		}
	}
	return nil
}
