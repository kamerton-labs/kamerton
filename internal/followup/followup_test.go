// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/evidence"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

var t0 = time.Date(2026, 11, 16, 4, 18, 0, 0, time.UTC)

type sentTelegram struct {
	ref    v1alpha1.SecretKeyRef
	chatID int64
	html   string
}

type fakeTelegram struct {
	mu   sync.Mutex
	sent []sentTelegram
	err  error
}

func (f *fakeTelegram) SendFollowUp(_ context.Context, ref v1alpha1.SecretKeyRef, chatID int64, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentTelegram{ref, chatID, html})
	return nil
}

type fakeEmail struct {
	mu      sync.Mutex
	to      [][]string
	subject []string
	text    []string
}

func (f *fakeEmail) SendFollowUp(_ context.Context, rcv *v1alpha1.EmailReceiver, subject, text, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.to = append(f.to, rcv.To)
	f.subject = append(f.subject, subject)
	f.text = append(f.text, text)
	return nil
}

type fakeProm struct{ value model.Value }

func (f fakeProm) Query(context.Context, string, time.Time) (model.Value, error) { return f.value, nil }
func (f fakeProm) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (model.Matrix, error) {
	return nil, nil
}

func tgRef() v1alpha1.SecretKeyRef {
	return v1alpha1.SecretKeyRef{Name: "kamerton-telegram", Key: "bot-token"}
}

func description(lang string, followUp *bool) *v1alpha1.SystemDescription {
	return &v1alpha1.SystemDescription{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "kamerton"},
		Spec: v1alpha1.SystemDescriptionSpec{
			Services: []v1alpha1.Service{
				{Name: "frontend", Type: v1alpha1.ServiceTypeHTTP, Critical: true, DependsOn: []string{"checkout"}},
				{Name: "checkout", Type: v1alpha1.ServiceTypeGRPC, Critical: true, DependsOn: []string{"payment"}},
				{Name: "payment", Type: v1alpha1.ServiceTypeGRPC, Critical: true},
				{Name: "ad", Type: v1alpha1.ServiceTypeGRPC},
			},
			Notifications: &v1alpha1.Notifications{
				Language: lang, TimeZone: "Asia/Almaty", EvidenceFollowUp: followUp,
				Receivers: []v1alpha1.Receiver{
					{Name: "oncall", Telegram: &v1alpha1.TelegramReceiver{ChatID: -1001, BotTokenSecret: tgRef()}},
					{Name: "oncall-copy", Telegram: &v1alpha1.TelegramReceiver{ChatID: -1001, BotTokenSecret: tgRef()}},
					{Name: "team-ad", Telegram: &v1alpha1.TelegramReceiver{ChatID: -1002, BotTokenSecret: tgRef()}},
					{Name: "mail", Email: &v1alpha1.EmailReceiver{To: []string{"oncall@example.com"}, From: "kamerton@example.com", Smarthost: "smtp.example.com:465"}},
					{Name: "warnings", Telegram: &v1alpha1.TelegramReceiver{ChatID: -1003, BotTokenSecret: tgRef()}},
				},
				Routing: &v1alpha1.Routing{
					Critical: []string{"oncall", "oncall-copy", "mail"},
					Warning:  []string{"warnings"},
					Routes:   []v1alpha1.Route{{Receiver: "team-ad", Services: []string{"ad"}}},
				},
			},
		},
	}
}

func amAlert(svc, name string, minute int, inhibited bool) alertmanager.Alert {
	a := alertmanager.Alert{
		Fingerprint: svc + "/" + name,
		Labels: map[string]string{"alertname": name, rules.LabelService: svc, rules.LabelSeverity: "critical",
			rules.LabelManaged: "true", "kamerton_system": "shop"},
		Annotations: map[string]string{rules.AnnotationTitle: "title of " + name},
		StartsAt:    t0.Add(time.Duration(minute) * time.Minute),
	}
	if inhibited {
		a.Status.State = "suppressed"
		a.Status.InhibitedBy = []string{"x"}
	}
	return a
}

func hookFor(alerts ...alertmanager.Alert) *alertmanager.Webhook {
	h := &alertmanager.Webhook{Version: "4", Status: "firing", CommonLabels: map[string]string{"kamerton_system": "shop"}}
	for _, a := range alerts {
		h.Alerts = append(h.Alerts, alertmanager.WebhookAlert{Status: "firing", Labels: a.Labels, Annotations: a.Annotations, StartsAt: a.StartsAt, Fingerprint: a.Fingerprint})
	}
	return h
}

type harness struct {
	svc    *Service
	tg     *fakeTelegram
	mail   *fakeEmail
	slept  []time.Duration
	active []alertmanager.Alert
}

func newHarness(sd *v1alpha1.SystemDescription) *harness {
	h := &harness{tg: &fakeTelegram{}, mail: &fakeEmail{}}
	store := &descstore.Memory{}
	store.Set(sd, nil, "test")
	h.svc = &Service{
		Descriptions: store,
		Alerts: func(context.Context, *v1alpha1.SystemDescription) ([]alertmanager.Alert, error) {
			return h.active, nil
		},
		Sources: func(context.Context, *v1alpha1.SystemDescription) evidence.Sources {
			return evidence.Sources{Prom: fakeProm{value: model.Vector{{Metric: model.Metric{"span_name": "oteldemo.PaymentService/Charge"}, Value: 42}}}}
		},
		Collector: &evidence.Collector{Now: func() time.Time { return t0.Add(5 * time.Minute) }},
		Telegram:  h.tg,
		Email:     h.mail,
		Now:       func() time.Time { return t0.Add(5 * time.Minute) },
		Sleep:     func(_ context.Context, d time.Duration) { h.slept = append(h.slept, d) },
	}
	return h
}

func TestFollowUpSentOnceToTheSameRecipients(t *testing.T) {
	h := newHarness(description("", nil))
	h.active = []alertmanager.Alert{
		amAlert("payment", rules.AlertRequestsStopped, 0, false),
		amAlert("checkout", rules.AlertErrorRate, 1, true),
		amAlert("frontend", rules.AlertErrorRate, 1, true),
	}
	received := t0.Add(5 * time.Minute)
	res := h.svc.Process(context.Background(), hookFor(h.active[0]), received)
	if len(res) != 1 || res[0].Skipped != "" || res[0].Telegram != 1 || res[0].Email != 1 {
		t.Fatalf("result = %+v", res)
	}
	// One chat even though two receivers name it; not the warning chat nor
	// the route of another service.
	if len(h.tg.sent) != 1 || h.tg.sent[0].chatID != -1001 || h.tg.sent[0].ref != tgRef() {
		t.Errorf("telegram = %+v", h.tg.sent)
	}
	msg := h.tg.sent[0].html
	for _, want := range []string{
		"🔎 <b>Probable cause</b> · shop\n",
		"Probable cause: <b>payment</b> (payment: title of KamertonRequestsStopped since 09:18 UTC+5).",
		"Also affected: checkout, frontend.",
		"Most errors: operation <b>oteldemo.PaymentService/Charge</b> (42 failed calls in 30 min).",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if len(h.mail.to) != 1 || h.mail.to[0][0] != "oncall@example.com" || h.mail.subject[0] != "Probable cause: shop / payment" {
		t.Errorf("email = %+v %+v", h.mail.to, h.mail.subject)
	}
	if len(h.slept) != 1 || h.slept[0] != DefaultDelay {
		t.Errorf("waited %v, want %s after the notification", h.slept, DefaultDelay)
	}

	// Alertmanager repeats the group, and the muted caller's group arrives
	// later (grouping off for a moment, or a resource alert): same incident,
	// nothing new is sent.
	res = h.svc.Process(context.Background(), hookFor(h.active[0]), received.Add(4*time.Hour))
	res2 := h.svc.Process(context.Background(), hookFor(h.active[1]), received.Add(time.Minute))
	if len(res) != 1 || res[0].Skipped != "already sent" || len(res2) != 1 || res2[0].Skipped != "already sent" || len(h.tg.sent) != 1 {
		t.Errorf("repeat: %+v %+v, sent %d", res, res2, len(h.tg.sent))
	}

	// A new episode (the root fired again later) is a new incident.
	h.active[0] = amAlert("payment", rules.AlertRequestsStopped, 90, false)
	res = h.svc.Process(context.Background(), hookFor(h.active[0]), received.Add(2*time.Hour))
	if len(res) != 1 || res[0].Skipped != "" || len(h.tg.sent) != 2 {
		t.Errorf("new episode: %+v", res)
	}
}

func TestFollowUpRussianAndCallerNotification(t *testing.T) {
	h := newHarness(description("ru", nil))
	// Grouping by cause is off: the caller's own alert is delivered and the
	// follow-up names the cause.
	h.active = []alertmanager.Alert{
		amAlert("payment", rules.AlertErrorRate, 0, false),
		amAlert("checkout", rules.AlertErrorRate, 1, false),
	}
	res := h.svc.Process(context.Background(), hookFor(h.active[1]), t0)
	if len(res) != 1 || res[0].Telegram != 1 {
		t.Fatalf("result = %+v", res)
	}
	msg := h.tg.sent[0].html
	if !strings.Contains(msg, "<b>checkout</b>: вероятная причина в <b>payment</b> (payment: title of KamertonErrorRateHigh с 09:18 UTC+5).") {
		t.Errorf("message:\n%s", msg)
	}
	if strings.Contains(msg, "Также затронуты") {
		t.Errorf("the alerting service is listed as also affected:\n%s", msg)
	}
}

func TestFollowUpNotSent(t *testing.T) {
	off := false
	cases := []struct {
		name   string
		sd     *v1alpha1.SystemDescription
		hook   *alertmanager.Webhook
		active []alertmanager.Alert
		prom   model.Value
		reason string
	}{
		{
			name: "nothing worth sending", sd: description("", nil),
			hook:   hookFor(amAlert("payment", rules.AlertErrorRate, 0, false)),
			active: []alertmanager.Alert{amAlert("payment", rules.AlertErrorRate, 0, false)},
			prom:   model.Vector{}, reason: "nothing worth sending",
		},
		{
			name: "no recipients (warning without receivers of that service)", sd: func() *v1alpha1.SystemDescription {
				sd := description("", nil)
				sd.Spec.Notifications.Routing.Warning = nil
				return sd
			}(),
			hook: func() *alertmanager.Webhook {
				a := amAlert("ad", rules.AlertErrorRate, 0, false)
				a.Labels[rules.LabelService] = "frontend"
				a.Labels[rules.LabelSeverity] = "warning"
				return hookFor(a)
			}(),
			reason: "no recipients",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(tc.sd)
			h.active = tc.active
			if tc.prom != nil {
				h.svc.Sources = func(context.Context, *v1alpha1.SystemDescription) evidence.Sources {
					return evidence.Sources{Prom: fakeProm{value: tc.prom}}
				}
			}
			res := h.svc.Process(context.Background(), tc.hook, t0)
			if len(res) != 1 || res[0].Skipped != tc.reason || len(h.tg.sent)+len(h.mail.to) != 0 {
				t.Errorf("result = %+v, sent %d", res, len(h.tg.sent))
			}
		})
	}

	// Turned off in the description, resolved notifications, unknown
	// systems: nothing at all.
	h := newHarness(description("", &off))
	if res := h.svc.Process(context.Background(), hookFor(amAlert("payment", rules.AlertErrorRate, 0, false)), t0); res != nil {
		t.Errorf("evidenceFollowUp false: %+v", res)
	}
	h = newHarness(description("", nil))
	resolved := hookFor(amAlert("payment", rules.AlertErrorRate, 0, false))
	resolved.Status = "resolved"
	unknown := hookFor(amAlert("payment", rules.AlertErrorRate, 0, false))
	unknown.CommonLabels["kamerton_system"] = "nope"
	if r1, r2 := h.svc.Process(context.Background(), resolved, t0), h.svc.Process(context.Background(), unknown, t0); r1 != nil || r2 != nil || len(h.tg.sent) != 0 {
		t.Errorf("resolved %+v, unknown %+v", r1, r2)
	}
}

func TestFollowUpWithoutAlertmanager(t *testing.T) {
	// Alertmanager cannot be asked: the alerts of the notification are used.
	h := newHarness(description("", nil))
	h.svc.Alerts = func(context.Context, *v1alpha1.SystemDescription) ([]alertmanager.Alert, error) {
		return nil, errors.New("connection refused")
	}
	res := h.svc.Process(context.Background(), hookFor(amAlert("payment", rules.AlertErrorRate, 0, false)), t0)
	if len(res) != 1 || res[0].Telegram != 1 {
		t.Errorf("result = %+v", res)
	}
	// A failing channel is logged and does not stop the other one.
	h = newHarness(description("", nil))
	h.tg.err = errors.New("chat not found")
	res = h.svc.Process(context.Background(), hookFor(amAlert("payment", rules.AlertErrorRate, 0, false)), t0)
	if len(res) != 1 || res[0].Telegram != 0 || res[0].Email != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestHandleRunsInBackground(t *testing.T) {
	h := newHarness(description("", nil))
	h.svc.Handle(hookFor(amAlert("payment", rules.AlertErrorRate, 0, false)))
	h.svc.Wait()
	if len(h.tg.sent) != 1 {
		t.Errorf("sent %d", len(h.tg.sent))
	}
}

func TestRecipients(t *testing.T) {
	n := description("", nil).Spec.Notifications
	names := func(rs []v1alpha1.Receiver) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Name)
		}
		return strings.Join(out, ",")
	}
	ad := hookFor(amAlert("ad", rules.AlertErrorRate, 0, false)).Alerts
	if got := names(Recipients(n, ad)); got != "oncall,oncall-copy,team-ad,mail" {
		t.Errorf("critical ad: %s", got)
	}
	ad[0].Labels[rules.LabelSeverity] = "warning"
	if got := names(Recipients(n, ad)); got != "team-ad,warnings" {
		t.Errorf("warning ad: %s", got)
	}
	n.Routing.Routes[0].Severities = []v1alpha1.Severity{v1alpha1.SeverityCritical}
	if got := names(Recipients(n, ad)); got != "warnings" {
		t.Errorf("warning ad, route for critical only: %s", got)
	}
	if Recipients(nil, ad) != nil {
		t.Error("no notifications")
	}
}

func TestNewToken(t *testing.T) {
	a, err := NewToken()
	b, _ := NewToken()
	if err != nil || len(a) != 64 || a == b {
		t.Errorf("tokens %q %q %v", a, b, err)
	}
}
