// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/history"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

func withHistory(h *harness) *history.Service {
	hist := &history.Service{Descriptions: h.svc.Descriptions, Now: h.svc.Now}
	hist.OnClose = h.svc.RecordsClosed
	h.svc.History = hist
	return hist
}

func resolvedHook(end time.Time, alerts ...alertmanager.Alert) *alertmanager.Webhook {
	hook := hookFor(alerts...)
	hook.Status = "resolved"
	for i := range hook.Alerts {
		hook.Alerts[i].Status = "resolved"
		hook.Alerts[i].EndsAt = end
	}
	return hook
}

func records(t *testing.T, hist *history.Service) []history.Record {
	t.Helper()
	list, err := hist.List(context.Background(), history.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestHistoryRecordAndAskWhatHelped(t *testing.T) {
	for _, lang := range []string{"", rules.LanguageRussian} {
		h := newHarness(description(lang, nil))
		hist := withHistory(h)
		ctx := context.Background()
		h.active = []alertmanager.Alert{
			amAlert("payment", rules.AlertRequestsStopped, 0, false),
			amAlert("checkout", rules.AlertErrorRate, 1, true),
		}
		hook := hookFor(h.active[0])
		// The record exists as soon as the notification arrives.
		h.svc.observeHook(ctx, hook)
		list := records(t, hist)
		if len(list) != 1 || !list[0].Open || list[0].Roots[0] != "payment" || list[0].Affected[0] != "checkout" {
			t.Fatalf("records %+v", list)
		}
		res := h.svc.Process(ctx, hook, t0.Add(5*time.Minute))
		if len(res) != 1 || res[0].Telegram != 1 {
			t.Fatalf("follow-up %+v", res)
		}
		list = records(t, hist)
		if r := list[0]; len(r.FollowUpTo) != 1 || r.FollowUpTo[0] != "oncall" || r.Evidence == nil || r.Evidence.Operation == "" {
			t.Errorf("record after the follow-up %+v", r)
		}

		// Resolved 12 minutes after the start: the chat that got the
		// follow-up is asked what helped; email is not.
		h.active = nil
		h.svc.observeHook(ctx, resolvedHook(t0.Add(12*time.Minute), amAlert("payment", rules.AlertRequestsStopped, 0, false)))
		h.svc.Wait()
		list = records(t, hist)
		if r := list[0]; r.Open || r.Duration() != 12*time.Minute || !r.AskSent || len(r.Asked) != 1 || r.Asked[0].Chat != -1001 || r.Asked[0].Message == 0 || len(r.FollowUpTo) != 0 {
			t.Errorf("closed record %+v", r)
		}
		if len(h.tg.sent) != 2 || h.tg.sent[1].chatID != -1001 || len(h.mail.to) != 1 {
			t.Fatalf("sent %+v, mail %d", h.tg.sent, len(h.mail.to))
		}
		want := "✅ <b>Incident over</b> · shop: <b>payment</b> (title of KamertonRequestsStopped), lasted 12 min.\nWhat helped? Reply to this message"
		if lang == rules.LanguageRussian {
			want = "✅ <b>Инцидент закончился</b> · shop: <b>payment</b> (title of KamertonRequestsStopped), длился 12 мин.\nЧто помогло? Ответьте на это сообщение"
		}
		if !strings.HasPrefix(h.tg.sent[1].html, want) {
			t.Errorf("%s: message %q", lang, h.tg.sent[1].html)
		}
		// Once only, also when the resolve repeats.
		h.svc.observeHook(ctx, resolvedHook(t0.Add(12*time.Minute), amAlert("payment", rules.AlertRequestsStopped, 0, false)))
		h.svc.Wait()
		if len(h.tg.sent) != 2 {
			t.Errorf("asked twice: %d messages", len(h.tg.sent))
		}
	}
}

func TestNoAskWhatHelped(t *testing.T) {
	off := false
	for name, tc := range map[string]struct {
		sd         *v1alpha1.SystemDescription
		end        time.Duration
		noFollowUp bool
	}{
		"short": {sd: description("", nil), end: time.Minute},
		"switch off": {sd: func() *v1alpha1.SystemDescription {
			d := description("", nil)
			d.Spec.Notifications.AskWhatHelped = &off
			return d
		}(), end: 20 * time.Minute},
		"no followup": {sd: description("", nil), end: 20 * time.Minute, noFollowUp: true},
	} {
		h := newHarness(tc.sd)
		hist := withHistory(h)
		ctx := context.Background()
		h.active = []alertmanager.Alert{amAlert("payment", rules.AlertRequestsStopped, 0, false), amAlert("checkout", rules.AlertErrorRate, 1, true)}
		if tc.noFollowUp {
			h.svc.observeHook(ctx, hookFor(h.active[0]))
		} else if res := h.svc.Process(ctx, hookFor(h.active[0]), t0.Add(5*time.Minute)); len(res) != 1 || res[0].Telegram != 1 {
			t.Fatalf("%s: follow-up %+v", name, res)
		}
		sent := len(h.tg.sent)
		h.active = nil
		h.svc.observeHook(ctx, resolvedHook(t0.Add(tc.end), amAlert("payment", rules.AlertRequestsStopped, 0, false)))
		h.svc.Wait()
		if list := records(t, hist); len(list) != 1 || list[0].Open {
			t.Errorf("%s: records %+v", name, list)
		}
		if len(h.tg.sent) != sent {
			t.Errorf("%s: asked anyway: %+v", name, h.tg.sent[sent:])
		}
	}
}

// pastIncident puts a closed incident of payment with a note into the history.
func pastIncident(t *testing.T, hist *history.Service, kind string, start time.Time, fix string) {
	t.Helper()
	ctx := context.Background()
	inc := triage.Incident{ID: "shop:payment", System: "shop", Roots: []string{"payment"}, StartsAt: start,
		RootAlerts: []triage.Alert{{Name: kind, Service: "payment", StartsAt: start}}}
	st, err := hist.Observe(ctx, history.Observation{System: "shop", Incidents: []triage.Incident{inc}})
	if err != nil {
		t.Fatal(err)
	}
	id := st.OpenRecord("shop", "shop:payment").ID
	if _, err := hist.Observe(ctx, history.Observation{System: "shop", Ended: map[string]time.Time{history.EndKey(kind, "payment"): start.Add(9 * time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	if fix != "" {
		if _, err := hist.SetNote(ctx, id, history.NoteInput{Fix: &fix}, history.SourceTelegram); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFollowUpRecallsEarlierIncident(t *testing.T) {
	h := newHarness(description("", nil))
	hist := withHistory(h)
	pastIncident(t, hist, rules.AlertRequestsStopped, t0.Add(-10*24*time.Hour), "rolled back to v1.4.2")
	h.active = []alertmanager.Alert{amAlert("payment", rules.AlertRequestsStopped, 0, false)}
	// Nothing collected: the alert alone says as much, but the note is worth it.
	h.svc.Sources, h.svc.Collector = nil, nil
	res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0.Add(5*time.Minute))
	if len(res) != 1 || res[0].Skipped != "" {
		t.Fatalf("result %+v", res)
	}
	msg := h.tg.sent[0].html
	for _, w := range []string{"🕘 <b>Happened before</b>: 09:18 06.11 UTC+5, lasted 9 min.\n", "What helped: rolled back to v1.4.2\n", "Times in the last 30 days: 1.\n"} {
		if !strings.Contains(msg, w) {
			t.Errorf("message lacks %q:\n%s", w, msg)
		}
	}
	if !strings.Contains(h.mail.text[0], "What helped: rolled back to v1.4.2") {
		t.Errorf("email lacks the recall:\n%s", h.mail.text[0])
	}
}

func TestFollowUpWithoutNoteNotWorthByRecall(t *testing.T) {
	h := newHarness(description("", nil))
	hist := withHistory(h)
	pastIncident(t, hist, rules.AlertRequestsStopped, t0.Add(-3*24*time.Hour), "")
	h.active = []alertmanager.Alert{amAlert("payment", rules.AlertRequestsStopped, 0, false)}
	h.svc.Sources, h.svc.Collector = nil, nil
	h.svc.Rechecks = []time.Duration{}
	res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0.Add(5*time.Minute))
	if len(res) != 1 || res[0].Skipped != "nothing worth sending" || len(h.tg.sent) != 0 {
		t.Errorf("result %+v, sent %d", res, len(h.tg.sent))
	}
}

func TestSweepClosesMissedResolve(t *testing.T) {
	h := newHarness(description("", nil))
	hist := withHistory(h)
	ctx := context.Background()
	h.active = []alertmanager.Alert{amAlert("payment", rules.AlertRequestsStopped, 0, false)}
	h.svc.observeHook(ctx, hookFor(h.active[0]))
	h.svc.Sweep(ctx)
	if list := records(t, hist); !list[0].Open {
		t.Fatal("closed while still firing")
	}
	h.active = nil
	h.svc.Sweep(ctx)
	h.svc.Wait()
	if list := records(t, hist); list[0].Open || list[0].EndedAt != t0.Add(5*time.Minute) {
		t.Errorf("swept %+v", list[0])
	}
}
