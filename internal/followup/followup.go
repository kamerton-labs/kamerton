// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package followup sends the second message of an incident: Alertmanager
// delivers the alert to Telegram and email itself (so alerts arrive while
// Kamerton is down) and also notifies Kamerton's webhook. On a firing
// notification Kamerton groups the system's alerts by cause
// (triage.Incidents), collects evidence about the root service
// (internal/evidence) and sends one short message to the same chats and
// addresses, a little after the alert so it arrives second.
//
// One follow-up per incident: an incident is identified by its system, its
// root services and the start of its earliest root alert, so Alertmanager's
// repeats and later updates of the same group send nothing new, while a new
// episode (the root resolved and fired again) does. The record is kept in
// memory of the replica that received the notification.
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
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/evidence"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// Defaults of a Service.
const (
	// DefaultDelay is how long after the notification the follow-up goes
	// out at the earliest, so it arrives after the alert itself.
	DefaultDelay = 10 * time.Second
	// DefaultTTL is how long an incident is remembered.
	DefaultTTL = 24 * time.Hour
	// maxRoots bounds the roots evidence is collected for.
	maxRoots = 3
	// processTimeout bounds the work on one notification.
	processTimeout = time.Minute
)

// TelegramSender sends a message with Kamerton's bot.
type TelegramSender interface {
	SendFollowUp(ctx context.Context, ref v1alpha1.SecretKeyRef, chatID int64, html string) error
}

// EmailSender sends a message through Kamerton's mail server.
type EmailSender interface {
	SendFollowUp(ctx context.Context, rcv *v1alpha1.EmailReceiver, subject, text, html string) error
}

// Service handles webhook notifications.
type Service struct {
	Descriptions descstore.Store
	// Alerts returns the active alerts of a system, muted ones included,
	// from the Alertmanager of its set; nil or an error: only the alerts of
	// the notification are used.
	Alerts func(ctx context.Context, sd *v1alpha1.SystemDescription) ([]alertmanager.Alert, error)
	// Sources returns the programs of the system's set.
	Sources   func(ctx context.Context, sd *v1alpha1.SystemDescription) evidence.Sources
	Collector *evidence.Collector
	// Telegram and Email are nil when that channel is unavailable.
	Telegram TelegramSender
	Email    EmailSender
	Delay    time.Duration
	TTL      time.Duration
	Logger   *slog.Logger
	Now      func() time.Time
	// Sleep waits for d or until ctx ends; replaceable in tests.
	Sleep func(ctx context.Context, d time.Duration)

	mu   sync.Mutex
	sent map[string]time.Time
	wg   sync.WaitGroup
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

// Handle processes a notification in the background and returns at once:
// Alertmanager waits for the webhook answer only a few seconds.
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

// Wait waits for notifications handled in the background (tests, shutdown).
func (s *Service) Wait() { s.wg.Wait() }

// claim records an incident; false when it was already recorded.
func (s *Service) claim(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if s.sent == nil {
		s.sent = map[string]time.Time{}
	}
	for k, t := range s.sent {
		if now.Sub(t) > ttl {
			delete(s.sent, k)
		}
	}
	if _, ok := s.sent[key]; ok {
		return false
	}
	s.sent[key] = now
	return true
}

// Result says what Process did with one incident (for logs and tests).
type Result struct {
	Incident string
	// Skipped explains why nothing was sent; empty when sent.
	Skipped string
	// Telegram and Email count the messages sent.
	Telegram, Email int
}

// Process handles one notification synchronously; received is when it
// arrived (the follow-up goes out at the earliest Delay after it).
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

	// The services of the notification, by incident.
	byIncident := map[string][]string{}
	var order []string
	for _, a := range firing {
		svc := a.Labels[rules.LabelService]
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
		switch {
		case len(recipients) == 0:
			res.Skipped = "no recipients"
		case !s.claim(key):
			res.Skipped = "already sent"
		default:
			res = s.send(ctx, sd, inc, byIncident[id], recipients, received)
		}
		if res.Skipped != "" {
			s.log().Debug("follow-up skipped", "system", sd.Name, "incident", id, "reason", res.Skipped)
		}
		results = append(results, res)
	}
	return results
}

// send collects evidence about the incident and sends the follow-up.
func (s *Service) send(ctx context.Context, sd *v1alpha1.SystemDescription, inc *triage.Incident, services []string, recipients []v1alpha1.Receiver, received time.Time) Result {
	res := Result{Incident: inc.ID}
	f := s.evidence(ctx, sd, inc, services)
	if !f.Worth() {
		res.Skipped = "nothing worth sending"
		return res
	}
	n := sd.Spec.Notifications
	lang, tz := rules.LanguageEnglish, ""
	if n != nil {
		if n.Language != "" {
			lang = n.Language
		}
		tz = n.TimeZone
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
	} else {
		s.log().Info("follow-up sent", "system", sd.Name, "incident", inc.ID, "telegram", res.Telegram, "email", res.Email)
	}
	return res
}

// evidence assembles the follow-up of an incident; services are the
// services of the notification.
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
	wg.Wait()
	return f
}

// rootAlert picks the alert that describes a root: its earliest symptom
// alert, else its earliest alert.
func rootAlert(alerts []triage.Alert, svc string) (string, time.Time) {
	var best *triage.Alert
	for i := range alerts {
		a := &alerts[i]
		if a.Service != svc {
			continue
		}
		better := best == nil ||
			(rules.IsSymptom(a.Name) && !rules.IsSymptom(best.Name)) ||
			(rules.IsSymptom(a.Name) == rules.IsSymptom(best.Name) && a.StartsAt.Before(best.StartsAt))
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

// description finds the system by the kamerton_system label (the name of
// the description).
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

// systemAlerts returns the system's alerts (muted ones included) for the
// incident grouping, with the alerts of the notification added when
// Alertmanager could not be asked or does not have them yet.
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

// Recipients returns the receivers that got the notification about these
// alerts: the receivers of their severity (routing.critical / warning) and
// of the routes listing their service, in the order of the description.
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
