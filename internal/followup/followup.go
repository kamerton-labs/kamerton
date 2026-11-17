// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package followup sends a second message with evidence about the cause after Alertmanager delivers an incident.
package followup

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/evidence"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

const (
	// DefaultDelay makes the follow-up arrive after the alert itself.
	DefaultDelay = 10 * time.Second
	// DefaultTTL is how long an incident and its evidence are remembered.
	DefaultTTL     = 24 * time.Hour
	maxRoots       = 3
	processTimeout = time.Minute
	maxEvidence    = 200
	maxLikely      = 3
	// checkConcurrency bounds the instant queries of the dependency check.
	checkConcurrency = 8
)

type TelegramSender interface {
	SendFollowUp(ctx context.Context, ref v1alpha1.SecretKeyRef, chatID int64, html string) error
}

type EmailSender interface {
	SendFollowUp(ctx context.Context, rcv *v1alpha1.EmailReceiver, subject, text, html string) error
}

type Service struct {
	Descriptions descstore.Store
	// Alerts returns a system's active alerts, muted ones included. On nil or an
	// error only the alerts of the notification are used.
	Alerts    func(ctx context.Context, sd *v1alpha1.SystemDescription) ([]alertmanager.Alert, error)
	Sources   func(ctx context.Context, sd *v1alpha1.SystemDescription) evidence.Sources
	Collector *evidence.Collector
	// Telegram and Email are nil when that channel is unavailable.
	Telegram TelegramSender
	Email    EmailSender
	// State remembers incidents across restarts and replicas; nil: memory.
	State  StateStore
	Delay  time.Duration
	TTL    time.Duration
	Logger *slog.Logger
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration)

	mu        sync.Mutex
	memory    MemoryStateStore
	collected map[string]cachedEvidence
	wg        sync.WaitGroup
}

type cachedEvidence struct {
	at      time.Time
	summary generator.EvidenceSummary
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Service) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if s.Sleep != nil {
		s.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Handle works in the background and returns at once: Alertmanager waits only
// a few seconds for the webhook answer.
func (s *Service) Handle(hook *alertmanager.Webhook) {
	received := s.now()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
		defer cancel()
		s.Process(ctx, hook, received)
	}()
}

func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultTTL
}

// update writes to State and falls back to memory when State fails, so a
// broken store never stops a follow-up (at worst one is sent twice).
func (s *Service) update(ctx context.Context, mutate func(*State) error) {
	if s.State != nil {
		_, err := s.State.Update(ctx, mutate)
		if err == nil {
			return
		}
		s.log().Warn("follow-up state unavailable; remembering in memory", "error", err)
	}
	_, _ = s.memory.Update(ctx, mutate)
}

// claim records the incident and reports false when it was already recorded.
// earlier are the follow-ups sent for other incidents of the same system.
func (s *Service) claim(ctx context.Context, key string, rec Record) (claimed bool, earlier []Record) {
	now := s.now()
	s.update(ctx, func(st *State) error {
		claimed, earlier = false, nil
		st.prune(now, s.ttl())
		if _, ok := st.Records[key]; ok {
			return ErrNoChange
		}
		for _, r := range st.Records {
			if r.System == rec.System && r.Sent {
				earlier = append(earlier, r)
			}
		}
		st.Records[key] = rec
		claimed = true
		return nil
	})
	return claimed, earlier
}

func (s *Service) markSent(ctx context.Context, key string, likely []string) {
	s.update(ctx, func(st *State) error {
		r, ok := st.Records[key]
		if !ok {
			return ErrNoChange
		}
		r.Sent, r.Likely = true, likely
		st.Records[key] = r
		return nil
	})
}

func (s *Service) remember(incident string, sum generator.EvidenceSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.collected == nil {
		s.collected = map[string]cachedEvidence{}
	}
	now := s.now()
	oldest := ""
	for k, e := range s.collected {
		if now.Sub(e.at) > s.ttl() {
			delete(s.collected, k)
			continue
		}
		if oldest == "" || e.at.Before(s.collected[oldest].at) {
			oldest = k
		}
	}
	if _, ok := s.collected[incident]; !ok && len(s.collected) >= maxEvidence {
		delete(s.collected, oldest)
	}
	s.collected[incident] = cachedEvidence{at: now, summary: sum}
}

// Evidence returns the last evidence collected for an incident ID, kept in
// this replica's memory for TTL; nil when there is none.
func (s *Service) Evidence(incident string) *generator.EvidenceSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.collected[incident]
	if !ok || s.now().Sub(e.at) > s.ttl() {
		return nil
	}
	sum := e.summary
	return &sum
}

type Result struct {
	Incident string
	// Skipped explains why nothing was sent; empty when sent.
	Skipped         string
	Telegram, Email int
}

// Process handles one notification synchronously; the follow-up goes out no
// earlier than Delay after received.
func (s *Service) Process(ctx context.Context, hook *alertmanager.Webhook, received time.Time) []Result {
	if hook == nil || hook.Status != "firing" {
		return nil
	}
	var firing []alertmanager.WebhookAlert
	for _, a := range hook.Alerts {
		if a.Status == "firing" && a.Labels[rules.LabelManaged] == "true" {
			firing = append(firing, a)
		}
	}
	if len(firing) == 0 {
		return nil
	}
	system := hook.CommonLabels[generator.LabelSystem]
	if system == "" {
		system = firing[0].Labels[generator.LabelSystem]
	}
	sd := s.description(ctx, system)
	if sd == nil {
		s.log().Warn("follow-up: unknown system", "system", system)
		return nil
	}
	if !sd.Spec.Notifications.SendsEvidence() {
		return nil
	}
	recipients := Recipients(sd.Spec.Notifications, firing)
	alerts := s.systemAlerts(ctx, sd, firing)
	incidents := triage.NewGraph(&sd.Spec).Incidents(sd.Name, alerts)

	byIncident := map[string][]string{}
	var order []string
	for _, a := range firing {
		svc := a.Labels[rules.LabelService]
		if svc == "" {
			continue // about the whole system: there is no data to collect evidence from
		}
		inc := triage.IncidentOf(incidents, svc)
		if inc == nil {
			continue
		}
		if _, seen := byIncident[inc.ID]; !seen {
			order = append(order, inc.ID)
		}
		byIncident[inc.ID] = appendUnique(byIncident[inc.ID], svc)
	}
	var results []Result
	for _, id := range order {
		var inc *triage.Incident
		for i := range incidents {
			if incidents[i].ID == id {
				inc = &incidents[i]
			}
		}
		res := Result{Incident: id}
		key := id + "@" + strconv.FormatInt(inc.StartsAt.Unix(), 10)
		rec := Record{At: s.now(), System: sd.Name, Roots: inc.Roots}
		if len(inc.Roots) > 0 {
			_, rec.Since = rootAlert(inc.RootAlerts, inc.Roots[0])
		}
		if claimed, earlier := s.claim(ctx, key, rec); claimed {
			res = s.send(ctx, sd, inc, key, byIncident[id], recipients, earlier, received)
		} else {
			res.Skipped = "already sent"
		}
		if res.Skipped != "" {
			s.log().Debug("follow-up skipped", "system", sd.Name, "incident", id, "reason", res.Skipped)
		}
		results = append(results, res)
	}
	return results
}

// send collects the evidence, keeps it for the alerts API and sends it when it
// says more than the alert and someone got the alert.
func (s *Service) send(ctx context.Context, sd *v1alpha1.SystemDescription, inc *triage.Incident, key string, services []string,
	recipients []v1alpha1.Receiver, earlier []Record, received time.Time) Result {
	res := Result{Incident: inc.ID}
	f := s.evidence(ctx, sd, inc, services)
	f.Explains, f.ExplainsSince = explains(earlier, inc)
	n := sd.Spec.Notifications
	lang, tz := rules.LanguageEnglish, ""
	if n != nil {
		if n.Language != "" {
			lang = n.Language
		}
		tz = n.TimeZone
	}
	s.remember(inc.ID, generator.Summarize(f, lang, tz, s.now()))
	switch {
	case len(recipients) == 0:
		res.Skipped = "no recipients"
		return res
	case !f.Worth():
		res.Skipped = "nothing worth sending"
		return res
	}
	msg := generator.RenderFollowUp(f, lang, tz)
	delay := s.Delay
	if delay == 0 {
		delay = DefaultDelay
	}
	s.sleep(ctx, received.Add(delay).Sub(s.now()))
	seenChat := map[int64]bool{}
	for i := range recipients {
		r := &recipients[i]
		switch {
		case r.Telegram != nil && s.Telegram != nil && !seenChat[r.Telegram.ChatID]:
			seenChat[r.Telegram.ChatID] = true
			if err := s.Telegram.SendFollowUp(ctx, r.Telegram.BotTokenSecret, r.Telegram.ChatID, msg.TelegramHTML); err != nil {
				s.log().Warn("follow-up to Telegram failed", "system", sd.Name, "receiver", r.Name, "error", err)
				continue
			}
			res.Telegram++
		case r.Email != nil && s.Email != nil:
			if err := s.Email.SendFollowUp(ctx, r.Email, msg.EmailSubject, msg.EmailText, msg.EmailHTML); err != nil {
				s.log().Warn("follow-up email failed", "system", sd.Name, "receiver", r.Name, "error", err)
				continue
			}
			res.Email++
		}
	}
	if res.Telegram+res.Email == 0 {
		res.Skipped = "no message could be sent"
		return res
	}
	likely := make([]string, 0, len(f.Likely))
	for _, l := range f.Likely {
		likely = append(likely, l.Service)
	}
	s.markSent(ctx, key, likely)
	s.log().Info("follow-up sent", "system", sd.Name, "incident", inc.ID, "telegram", res.Telegram, "email", res.Email)
	return res
}

// explains finds earlier follow-ups whose roots this incident now explains:
// they are among its affected services, or they named one of its roots as
// the likely cause (their alert may have resolved since).
func explains(earlier []Record, inc *triage.Incident) ([]string, time.Time) {
	affected, roots := map[string]bool{}, map[string]bool{}
	for _, a := range inc.Affected {
		affected[a] = true
	}
	for _, r := range inc.Roots {
		roots[r] = true
	}
	var names []string
	var since time.Time
	for _, r := range earlier {
		hit := false
		for _, x := range r.Roots {
			hit = hit || affected[x]
		}
		for _, x := range r.Likely {
			hit = hit || roots[x]
		}
		if !hit {
			continue
		}
		for _, x := range r.Roots {
			if !roots[x] {
				names = appendUnique(names, x)
			}
		}
		if !r.Since.IsZero() && (since.IsZero() || r.Since.Before(since)) {
			since = r.Since
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, time.Time{}
	}
	return names, since
}

func (s *Service) evidence(ctx context.Context, sd *v1alpha1.SystemDescription, inc *triage.Incident, services []string) *generator.FollowUp {
	f := &generator.FollowUp{System: sd.Name, Environment: sd.Spec.Environment, Window: evidence.DefaultWindow}
	if s.Collector != nil {
		f.Window = s.Collector.Window
		if f.Window == 0 {
			f.Window = evidence.DefaultWindow
		}
	}
	isRoot := map[string]bool{}
	for _, r := range inc.Roots {
		isRoot[r] = true
	}
	inNotification := map[string]bool{}
	for _, svc := range services {
		inNotification[svc] = true
		if !isRoot[svc] {
			f.Alerting = append(f.Alerting, svc)
		}
	}
	for _, a := range inc.Affected {
		if !inNotification[a] {
			f.Affected = append(f.Affected, a)
		}
	}
	var src evidence.Sources
	if s.Sources != nil {
		src = s.Sources(ctx, sd)
	}
	roots := inc.Roots
	if len(roots) > maxRoots {
		roots = roots[:maxRoots]
	}
	f.Roots = make([]generator.RootEvidence, len(roots))
	var wg sync.WaitGroup
	defer wg.Wait()
	for i, root := range roots {
		title, since := rootAlert(inc.RootAlerts, root)
		f.Roots[i] = generator.RootEvidence{Service: root, Title: title, Since: since}
		svc := service(sd, root)
		if svc == nil || s.Collector == nil {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := s.Collector.Collect(ctx, sd, svc, src, since)
			ev.Title, ev.Since = f.Roots[i].Title, f.Roots[i].Since
			f.Roots[i] = ev
		}(i)
	}
	if src.Prom == nil {
		return f
	}
	for _, root := range roots {
		if svc := service(sd, root); svc != nil && needsDependencyCheck(inc, root) {
			f.Likely = append(f.Likely, s.likelyCauses(ctx, sd, svc, src.Prom)...)
		}
	}
	if len(f.Likely) > maxLikely {
		f.Likely = f.Likely[:maxLikely]
	}
	if len(f.Likely) > 0 && s.Collector != nil {
		if svc := service(sd, f.Likely[0].Service); svc != nil {
			f.Likely[0].Evidence = s.Collector.Collect(ctx, sd, svc, src, inc.StartsAt)
		}
	}
	return f
}

// needsDependencyCheck: the root shows what a failing dependency causes (a
// symptom target) and has no cause of its own such as no ready pods.
func needsDependencyCheck(inc *triage.Incident, root string) bool {
	consequence := false
	for _, a := range inc.RootAlerts {
		if a.Service != root {
			continue
		}
		if rules.IsSymptomSource(a.Name) && !rules.IsSymptomTarget(a.Name) {
			return false
		}
		consequence = consequence || rules.IsSymptomTarget(a.Name)
	}
	return consequence
}

func (s *Service) checkTimeout() time.Duration {
	if s.Collector != nil && s.Collector.Timeout > 0 {
		return s.Collector.Timeout
	}
	return evidence.DefaultTimeout
}

// likelyCauses looks at the direct dependencies of a root whose own alerts
// have not fired: alerts Prometheus reports as pending, and the conditions of
// generator.DependencyChecks queried now. Most observations first.
func (s *Service) likelyCauses(ctx context.Context, sd *v1alpha1.SystemDescription, root *v1alpha1.Service, prom prometheus.Querier) []generator.LikelyCause {
	var deps []string
	for _, d := range root.DependsOn {
		if service(sd, d) != nil {
			deps = append(deps, d)
		}
	}
	if len(deps) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.checkTimeout())
	defer cancel()
	now := s.now()
	var mu sync.Mutex
	seen := map[string]map[string]bool{} // dependency -> alert -> pending
	add := func(dep, alert string, pending bool) {
		mu.Lock()
		defer mu.Unlock()
		if seen[dep] == nil {
			seen[dep] = map[string]bool{}
		}
		seen[dep][alert] = seen[dep][alert] || pending
	}
	sem := make(chan struct{}, checkConcurrency)
	var wg sync.WaitGroup
	query := func(expr string, found func(model.Vector)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			v, err := prom.Query(ctx, expr, now)
			if err != nil {
				s.log().Debug("dependency check failed", "system", sd.Name, "error", err)
				return
			}
			if vec, ok := v.(model.Vector); ok && len(vec) > 0 {
				found(vec)
			}
		}()
	}
	pending := rules.Matchers{}.Eq(rules.LabelManaged, "true").Eq(generator.LabelSystem, sd.Name).Eq("alertstate", "pending").
		Re(rules.LabelService, rules.QuoteValues(deps)...).Selector("ALERTS")
	query(pending, func(vec model.Vector) {
		for _, smp := range vec {
			if name := string(smp.Metric["alertname"]); rules.IsSymptomSource(name) {
				add(string(smp.Metric[model.LabelName(rules.LabelService)]), name, true)
			}
		}
	})
	for _, d := range deps {
		for _, c := range generator.DependencyChecks(sd, service(sd, d)) {
			query(c.Expr, func(model.Vector) { add(d, c.Alert, false) })
		}
	}
	wg.Wait()

	var out []generator.LikelyCause
	for _, d := range deps {
		if len(seen[d]) == 0 {
			continue
		}
		l := generator.LikelyCause{Service: d, Of: root.Name}
		// No ready pods first: it is the most direct of the observations.
		if p, ok := seen[d][rules.AlertNoReadyPods]; ok {
			l.Observed = append(l.Observed, generator.Observation{Alert: rules.AlertNoReadyPods, Pending: p})
		}
		for _, alert := range rules.SymptomSources() {
			if p, ok := seen[d][alert]; ok && alert != rules.AlertNoReadyPods {
				l.Observed = append(l.Observed, generator.Observation{Alert: alert, Pending: p})
			}
		}
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Observed) > len(out[j].Observed) })
	return out
}

// rootAlert picks a root's earliest alert of the kind that explains others
// most (a symptom source, then a target), else its earliest alert.
func rootAlert(alerts []triage.Alert, svc string) (string, time.Time) {
	rank := func(name string) int {
		switch {
		case rules.IsSymptomSource(name):
			return 2
		case rules.IsSymptomTarget(name):
			return 1
		}
		return 0
	}
	var best *triage.Alert
	for i := range alerts {
		a := &alerts[i]
		if a.Service != svc {
			continue
		}
		better := best == nil || rank(a.Name) > rank(best.Name) ||
			(rank(a.Name) == rank(best.Name) && a.StartsAt.Before(best.StartsAt))
		if better {
			best = a
		}
	}
	if best == nil {
		return "", time.Time{}
	}
	return best.Title, best.StartsAt
}

func service(sd *v1alpha1.SystemDescription, name string) *v1alpha1.Service {
	for i := range sd.Spec.Services {
		if sd.Spec.Services[i].Name == name {
			return &sd.Spec.Services[i]
		}
	}
	return nil
}

func (s *Service) description(ctx context.Context, system string) *v1alpha1.SystemDescription {
	if system == "" || s.Descriptions == nil {
		return nil
	}
	list, err := s.Descriptions.List(ctx)
	if err != nil {
		s.log().Warn("follow-up: descriptions unavailable", "error", err)
		return nil
	}
	for _, snap := range list {
		if snap.SD != nil && snap.SD.Name == system {
			return snap.SD
		}
	}
	return nil
}

// systemAlerts returns the system's alerts, muted ones included, adding those of
// the notification when Alertmanager fails or does not have them yet.
func (s *Service) systemAlerts(ctx context.Context, sd *v1alpha1.SystemDescription, firing []alertmanager.WebhookAlert) []triage.Alert {
	var out []triage.Alert
	seen := map[string]bool{}
	if s.Alerts != nil {
		list, err := s.Alerts(ctx, sd)
		if err != nil {
			s.log().Warn("follow-up: alerts of the system unavailable; using the notification only", "system", sd.Name, "error", err)
		}
		for _, a := range list {
			if a.Labels[generator.LabelSystem] != sd.Name {
				continue
			}
			out = append(out, toTriage(a.Fingerprint, a.Labels, a.Annotations, a.StartsAt, a.Inhibited()))
			seen[key(a.Fingerprint, a.Labels)] = true
		}
	}
	for _, a := range firing {
		if !seen[key(a.Fingerprint, a.Labels)] {
			out = append(out, toTriage(a.Fingerprint, a.Labels, a.Annotations, a.StartsAt, false))
		}
	}
	return out
}

func key(fp string, labels map[string]string) string {
	if fp != "" {
		return fp
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "\x00" + labels[k] + "\x00")
	}
	return b.String()
}

func toTriage(fp string, labels, ann map[string]string, starts time.Time, muted bool) triage.Alert {
	title := ann[rules.AnnotationTitle]
	if t := ann[generator.AnnotationMessageTitle]; t != "" {
		title = strings.TrimSpace(strings.ReplaceAll(t, "{since}", ""))
	}
	if title == "" {
		title = ann["summary"]
	}
	return triage.Alert{
		Fingerprint: fp, Name: labels["alertname"], Service: labels[rules.LabelService], Severity: labels[rules.LabelSeverity],
		StartsAt: starts, Title: title, Value: ann[rules.AnnotationValue], Muted: muted,
	}
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// Recipients returns the receivers of the alerts' severity and of the routes
// listing their service, in description order.
func Recipients(n *v1alpha1.Notifications, alerts []alertmanager.WebhookAlert) []v1alpha1.Receiver {
	if n == nil || n.Routing == nil {
		return nil
	}
	want := map[string]bool{}
	for _, a := range alerts {
		sev, svc := a.Labels[rules.LabelSeverity], a.Labels[rules.LabelService]
		switch v1alpha1.Severity(sev) {
		case v1alpha1.SeverityCritical:
			for _, r := range n.Routing.Critical {
				want[r] = true
			}
		case v1alpha1.SeverityWarning:
			for _, r := range n.Routing.Warning {
				want[r] = true
			}
		}
		for _, rt := range n.Routing.Routes {
			if !contains(rt.Services, svc) {
				continue
			}
			if len(rt.Severities) == 0 || containsSeverity(rt.Severities, sev) {
				want[rt.Receiver] = true
			}
		}
	}
	var out []v1alpha1.Receiver
	for _, r := range n.Receivers {
		if want[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsSeverity(list []v1alpha1.Severity, v string) bool {
	for _, x := range list {
		if string(x) == v {
			return true
		}
	}
	return false
}
