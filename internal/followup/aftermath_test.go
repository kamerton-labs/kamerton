// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/history"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// The live test: payment scaled to zero at 19:21:59 and restored at 19:34:47.
func at(hms string) time.Time {
	t, err := time.Parse("15:04:05", hms)
	if err != nil {
		panic(err)
	}
	return time.Date(2026, 11, 17, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

type liveClock struct{ t time.Time }

func (c *liveClock) now() time.Time { return c.t }

func alertAt(svc, name, severity string, starts time.Time) alertmanager.Alert {
	a := amAlert(svc, name, 0, false)
	a.Labels[rules.LabelSeverity] = severity
	a.StartsAt = starts
	return a
}

type liveHarness struct {
	*harness
	clock *liveClock
	hist  *history.Service
}

func newLive(t *testing.T) *liveHarness {
	t.Helper()
	h := newHarness(description("", nil))
	c := &liveClock{}
	h.svc.Now = c.now
	h.svc.Collector.Now = c.now
	hist := withHistory(h)
	hist.Now = c.now
	return &liveHarness{harness: h, clock: c, hist: hist}
}

// deliver handles a notification as Handle does, synchronously, at the given time.
func (l *liveHarness) deliver(at time.Time, hook *alertmanager.Webhook) []Result {
	l.clock.t = at
	ctx := context.Background()
	l.svc.observeHook(ctx, hook)
	res := l.svc.Process(ctx, hook, at)
	l.svc.Wait()
	return res
}

func (l *liveHarness) sweep(at time.Time) {
	l.clock.t = at
	l.svc.Sweep(context.Background())
	l.svc.Wait()
}

var (
	frontendOpErr = alertAt("frontend", rules.AlertOperationErrorRate, "critical", at("19:23:33"))
	checkoutErr   = alertAt("checkout", rules.AlertErrorRate, "critical", at("19:23:36"))
	checkoutOpErr = alertAt("checkout", rules.AlertOperationErrorRate, "critical", at("19:23:36"))
	checkoutFast  = alertAt("checkout", rules.AlertSLOBurnFast, "critical", at("19:23:50"))
	checkoutSlow  = alertAt("checkout", rules.AlertSLOBurnSlow, "warning", at("19:30:00"))
	paymentNoPods = alertAt("payment", rules.AlertNoReadyPods, "critical", at("19:24:10"))
	paymentStop   = alertAt("payment", rules.AlertRequestsStopped, "critical", at("19:26:50"))
)

// outage replays the webhooks of the live test up to the moment payment
// came back, and returns the history record.
func (l *liveHarness) outage(t *testing.T) history.Record {
	t.Helper()
	// The callers' alerts come first: frontend alone, then checkout.
	l.active = []alertmanager.Alert{frontendOpErr}
	l.svc.observeHook(context.Background(), hookFor(frontendOpErr))
	l.clock.t = at("19:24:03")
	l.svc.observeHook(context.Background(), hookFor(frontendOpErr))
	l.clock.t = at("19:24:06")
	l.active = []alertmanager.Alert{frontendOpErr, checkoutErr, checkoutOpErr, checkoutFast}
	l.svc.observeHook(context.Background(), hookFor(checkoutErr, checkoutOpErr, checkoutFast))
	// No ready pods of payment: the callers are now muted consequences.
	l.active = []alertmanager.Alert{paymentNoPods, frontendOpErr, checkoutErr, checkoutOpErr, checkoutFast}
	for i := 1; i < len(l.active); i++ {
		l.active[i].Status.State, l.active[i].Status.InhibitedBy = "suppressed", []string{"x"}
	}
	if res := l.deliver(at("19:24:40"), hookFor(paymentNoPods)); len(res) != 1 || res[0].Incident != "shop:payment" || res[0].Telegram != 1 {
		t.Fatalf("follow-up of payment %+v", res)
	}
	// The frontend group's own notification is the same incident.
	if res := l.deliver(at("19:24:50"), hookFor(frontendOpErr)); len(res) != 1 || res[0].Skipped != "already sent" {
		t.Fatalf("frontend notification %+v", res)
	}
	l.active = append(l.active, paymentStop, checkoutSlow)
	l.active[len(l.active)-1].Status.State, l.active[len(l.active)-1].Status.InhibitedBy = "suppressed", []string{"x"}
	l.deliver(at("19:27:20"), hookFor(paymentStop))
	l.sweep(at("19:30:00"))
	list := records(t, l.hist)
	if len(list) != 1 {
		t.Fatalf("records during the outage %+v", list)
	}
	return list[0]
}

func TestLiveSequenceOneIncidentWithATail(t *testing.T) {
	l := newLive(t)
	r := l.outage(t)
	if r.Incident != "shop:payment" || strings.Join(r.Roots, ",") != "payment" || r.StartedAt != at("19:23:33") ||
		strings.Join(r.Affected, ",") != "checkout,frontend" {
		t.Errorf("open record %+v", r)
	}
	for _, a := range r.RootAlerts {
		if a.Service != "payment" {
			t.Errorf("root alert of another service: %+v", a)
		}
	}
	if len(r.RootAlerts) != 2 || r.RootAlerts[0].Name != rules.AlertNoReadyPods {
		t.Errorf("root alerts %+v", r.RootAlerts)
	}

	// Payment is back at 19:34:47. Its alerts resolve; checkout's errors are
	// unmuted for a moment and its slow SLO burn keeps firing.
	l.active = []alertmanager.Alert{checkoutErr, checkoutOpErr, checkoutSlow}
	l.deliver(at("19:35:30"), resolvedHook(at("19:35:10"), paymentNoPods, paymentStop))
	list := records(t, l.hist)
	if len(list) != 1 {
		t.Fatalf("the tail opened a record: %+v", list)
	}
	r = list[0]
	if r.Open || r.EndedAt != at("19:35:10") || r.Incident != "shop:payment" || len(r.Aftermath) != 1 || r.Aftermath[0].Service != "checkout" {
		t.Errorf("closed record %+v", r)
	}
	if len(l.tg.sent) != 2 {
		t.Fatalf("telegram %+v", l.tg.sent)
	}
	over := l.tg.sent[1].html
	for _, w := range []string{
		"✅ <b>Incident over</b> · shop: <b>payment</b> (title of KamertonNoReadyPods), lasted 12 min.\n",
		"Still firing after it: checkout (Too many failed requests; ",
		"SLO error budget is burning). This is expected for a while after an outage.\n",
	} {
		if !strings.Contains(over, w) {
			t.Errorf("message lacks %q:\n%s", w, over)
		}
	}

	// Alertmanager notifies checkout's group now that it is not muted: no
	// follow-up, no record.
	res := l.deliver(at("19:36:30"), hookFor(checkoutErr, checkoutSlow))
	if len(res) != 1 || res[0].Skipped != "aftermath of an earlier incident" {
		t.Errorf("checkout notification %+v", res)
	}
	l.active = []alertmanager.Alert{checkoutSlow}
	l.sweep(at("19:41:30"))
	l.active = nil
	l.sweep(at("19:58:00"))
	list = records(t, l.hist)
	if len(list) != 1 || len(l.tg.sent) != 2 || l.svc.Evidence("shop:checkout") != nil {
		t.Errorf("records %+v, telegram %d", list, len(l.tg.sent))
	}
	if a := list[0].Aftermath[0]; a.LastSeen != at("19:41:30") || len(a.Alerts) != 3 {
		t.Errorf("aftermath %+v", a)
	}
}

func TestAftermathOutlivesTheGracePeriod(t *testing.T) {
	l := newLive(t)
	l.outage(t)
	l.active = []alertmanager.Alert{checkoutSlow}
	l.deliver(at("19:35:30"), resolvedHook(at("19:35:10"), paymentNoPods, paymentStop))
	l.sweep(at("19:40:00"))
	if list := records(t, l.hist); len(list) != 1 {
		t.Fatalf("records within the grace period %+v", list)
	}
	// 19:35:10 + 35 minutes: from then on it is a problem of its own.
	l.sweep(at("20:12:00"))
	list := records(t, l.hist)
	if len(list) != 2 {
		t.Fatalf("records %+v", list)
	}
	own := list[0]
	if !own.Open || !own.Promoted || own.Incident != "shop:checkout" || own.StartedAt != at("20:10:10") || len(own.FollowUpTo) != 1 || own.FollowUpTo[0] != "warnings" {
		t.Errorf("own record %+v", own)
	}
	if last := l.tg.sent[len(l.tg.sent)-1]; last.chatID != -1003 || !strings.Contains(last.html, "Probable cause") {
		t.Errorf("follow-up of the lasting tail %+v", last)
	}
	sent := len(l.tg.sent)
	l.sweep(at("20:17:00"))
	if len(l.tg.sent) != sent {
		t.Error("the promoted incident got a second follow-up")
	}
	// It ends like any other incident.
	l.active = nil
	l.sweep(at("20:22:00"))
	if list := records(t, l.hist); list[0].Open || list[0].EndedAt != at("20:22:00") {
		t.Errorf("promoted record %+v", list[0])
	}
}

func TestOwnFailureDuringTheGracePeriod(t *testing.T) {
	l := newLive(t)
	l.outage(t)
	l.active = []alertmanager.Alert{checkoutSlow}
	l.deliver(at("19:35:30"), resolvedHook(at("19:35:10"), paymentNoPods, paymentStop))
	sent := len(l.tg.sent)
	checkoutNoPods := alertAt("checkout", rules.AlertNoReadyPods, "critical", at("19:39:00"))
	l.active = []alertmanager.Alert{checkoutSlow, checkoutNoPods}
	res := l.deliver(at("19:39:40"), hookFor(checkoutNoPods))
	if len(res) != 1 || res[0].Incident != "shop:checkout" || res[0].Skipped != "" || len(l.tg.sent) != sent+1 {
		t.Fatalf("own failure %+v", res)
	}
	list := records(t, l.hist)
	if len(list) != 2 || !list[0].Open || list[0].StartedAt != at("19:30:00") || list[0].Promoted || list[0].RootAlerts[0].Name != rules.AlertNoReadyPods {
		t.Errorf("records %+v", list)
	}
}

func TestLateAttributionFollowUpNotSent(t *testing.T) {
	l := newLive(t)
	// checkout's notification arrives first; by the time of its follow-up the
	// cause's alert is there too.
	l.active = []alertmanager.Alert{checkoutErr}
	l.clock.t = at("19:23:40")
	l.svc.observeHook(context.Background(), hookFor(checkoutErr))
	l.active = []alertmanager.Alert{paymentNoPods, checkoutErr}
	l.active[1].Status.State, l.active[1].Status.InhibitedBy = "suppressed", []string{"x"}
	res := l.deliver(at("19:24:25"), hookFor(checkoutErr))
	if len(res) != 1 || res[0].Incident != "shop:payment" || res[0].Telegram != 1 {
		t.Fatalf("follow-up %+v", res)
	}
	if res := l.deliver(at("19:24:40"), hookFor(paymentNoPods)); len(res) != 1 || res[0].Skipped != "already sent" {
		t.Errorf("second notification %+v", res)
	}
	list := records(t, l.hist)
	if len(list) != 1 || list[0].Roots[0] != "payment" || list[0].StartedAt != at("19:23:36") || len(l.tg.sent) != 1 {
		t.Errorf("records %+v, telegram %d", list, len(l.tg.sent))
	}
}

func TestLateAttributionFollowUpAlreadySent(t *testing.T) {
	l := newLive(t)
	l.active = []alertmanager.Alert{checkoutErr}
	if res := l.deliver(at("19:24:20"), hookFor(checkoutErr)); len(res) != 1 || res[0].Incident != "shop:checkout" || res[0].Telegram != 1 {
		t.Fatalf("checkout follow-up %+v", res)
	}
	l.active = []alertmanager.Alert{paymentNoPods, checkoutErr}
	l.active[1].Status.State, l.active[1].Status.InhibitedBy = "suppressed", []string{"x"}
	if res := l.deliver(at("19:25:00"), hookFor(paymentNoPods)); len(res) != 1 || res[0].Incident != "shop:payment" || res[0].Telegram != 1 {
		t.Fatalf("payment follow-up %+v", res)
	}
	if !strings.Contains(l.tg.sent[1].html, "This explains the earlier alert about <b>checkout</b>") {
		t.Errorf("payment follow-up does not explain checkout:\n%s", l.tg.sent[1].html)
	}
	list := records(t, l.hist)
	if len(list) != 1 || list[0].Roots[0] != "payment" || list[0].StartedAt != at("19:23:36") || strings.Join(list[0].Affected, ",") != "checkout" {
		t.Errorf("records %+v", list)
	}
	// One "incident is over" message, to the chat that got both follow-ups.
	l.active = nil
	l.deliver(at("19:40:00"), resolvedHook(at("19:39:30"), paymentNoPods, checkoutErr))
	if len(l.tg.sent) != 3 || !strings.Contains(l.tg.sent[2].html, "Incident over") {
		t.Errorf("telegram %+v", l.tg.sent)
	}
}
