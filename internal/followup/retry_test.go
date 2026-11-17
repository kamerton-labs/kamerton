// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/evidence"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// quietHarness has no evidence at first: the root alert came alone and the
// workload says nothing, as with a Deployment scaled to zero before.
func quietHarness() (*harness, *bytes.Buffer) {
	h := newHarness(description("", nil))
	h.svc.Sources = func(context.Context, *v1alpha1.SystemDescription) evidence.Sources {
		return evidence.Sources{Prom: fakeProm{value: model.Vector{}}}
	}
	var logs bytes.Buffer
	h.svc.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	h.active = []alertmanager.Alert{amAlert("payment", rules.AlertNoReadyPods, 0, false)}
	return h, &logs
}

func TestFollowUpSentOnTimedRecheck(t *testing.T) {
	h, _ := quietHarness()
	h.svc.Sleep = func(_ context.Context, d time.Duration) {
		h.slept = append(h.slept, d)
		if len(h.slept) == 2 { // the first re-check: the caller's alert has fired and is muted
			h.active = append(h.active, amAlert("checkout", rules.AlertErrorRate, 1, true))
		}
	}
	res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0.Add(5*time.Minute))
	if len(res) != 1 || res[0].Skipped != "" || res[0].Telegram != 1 || len(h.tg.sent) != 1 {
		t.Fatalf("result = %+v, sent %d", res, len(h.tg.sent))
	}
	if !strings.Contains(h.tg.sent[0].html, "Also affected: checkout.") {
		t.Errorf("message:\n%s", h.tg.sent[0].html)
	}
	if len(h.slept) != 2 || h.slept[0] != DefaultDelay || h.slept[1] != time.Minute {
		t.Errorf("waits %v", h.slept)
	}
}

func TestFollowUpSentOnLaterWebhook(t *testing.T) {
	h, _ := quietHarness()
	inner := false
	h.svc.Sleep = func(_ context.Context, d time.Duration) {
		h.slept = append(h.slept, d)
		if len(h.slept) == 2 && !inner {
			// Between the timed checks Alertmanager notifies the caller's group.
			inner = true
			caller := amAlert("checkout", rules.AlertErrorRate, 1, true)
			h.active = append(h.active, caller)
			res := h.svc.Process(context.Background(), hookFor(caller), t0.Add(6*time.Minute))
			if len(res) != 1 || res[0].Incident != "shop:payment" || res[0].Telegram != 1 {
				t.Errorf("later webhook: %+v", res)
			}
		}
	}
	res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0.Add(5*time.Minute))
	if len(res) != 1 || res[0].Skipped != "already sent" || len(h.tg.sent) != 1 {
		t.Errorf("first notification ends with %+v, sent %d", res, len(h.tg.sent))
	}
}

func TestFollowUpNothingEverWorthSending(t *testing.T) {
	h, logs := quietHarness()
	store := &MemoryStateStore{}
	h.svc.State = store
	res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0.Add(5*time.Minute))
	if len(res) != 1 || res[0].Skipped != "nothing worth sending" || len(h.tg.sent)+len(h.mail.to) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(h.slept) != 3 || h.slept[1] != time.Minute || h.slept[2] != 2*time.Minute {
		t.Errorf("waits %v, want the delay and two re-checks", h.slept)
	}
	st, _ := store.Update(context.Background(), func(*State) error { return ErrNoChange })
	rec := st.Records["shop:payment@"+itoa(t0.Unix())]
	if !rec.Done || rec.Sent || rec.Attempts != 3 || rec.Reason != "nothing worth sending" || !rec.LeaseUntil.IsZero() {
		t.Errorf("record = %+v", rec)
	}
	// Later notifications of the same incident do nothing more, and the reason is logged once.
	for range 3 {
		if res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0.Add(10*time.Minute)); len(res) != 1 || res[0].Skipped != "given up" {
			t.Errorf("after giving up: %+v", res)
		}
	}
	if n := strings.Count(logs.String(), `msg="follow-up not sent"`); n != 1 || !strings.Contains(logs.String(), `reason="nothing worth sending"`) {
		t.Errorf("%d info lines:\n%s", n, logs.String())
	}
	if len(h.tg.sent) != 0 {
		t.Error("sent after giving up")
	}
}

func TestFollowUpAttemptsAreBounded(t *testing.T) {
	h, _ := quietHarness()
	h.svc.Rechecks = []time.Duration{} // every attempt is final: one per notification
	h.svc.Process(context.Background(), hookFor(h.active[0]), t0)
	if res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0); res[0].Skipped != "given up" {
		t.Errorf("result = %+v", res)
	}

	// The process stops after its first attempt each time (a restart loses the
	// timed checks): later webhooks retry, up to maxAttempts.
	h, _ = quietHarness()
	var cancel context.CancelFunc
	h.svc.Sleep = func(_ context.Context, d time.Duration) {
		if d == time.Minute {
			cancel()
		}
	}
	for i := range maxAttempts {
		ctx, stop := context.WithCancel(context.Background())
		cancel = stop
		res := h.svc.Process(ctx, hookFor(h.active[0]), t0)
		stop()
		if want := "nothing worth sending"; res[0].Skipped != want {
			t.Errorf("attempt %d: %+v", i+1, res)
		}
	}
	if res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0); res[0].Skipped != "given up" {
		t.Errorf("after %d attempts: %+v", maxAttempts, res)
	}
}

func TestExactlyOneFollowUpUnderRepeatedWebhooks(t *testing.T) {
	h := newHarness(description("", nil))
	h.svc.Sleep = func(context.Context, time.Duration) {}
	h.active = []alertmanager.Alert{amAlert("payment", rules.AlertRequestsStopped, 0, false), amAlert("checkout", rules.AlertErrorRate, 1, true)}
	for range 8 {
		h.svc.Handle(hookFor(h.active[0]))
		h.svc.Handle(hookFor(h.active[1]))
	}
	h.svc.Wait()
	if len(h.tg.sent) != 1 || len(h.mail.to) != 1 {
		t.Errorf("telegram %d, email %d, want one each", len(h.tg.sent), len(h.mail.to))
	}
}

func TestLeaseKeepsAttemptsApart(t *testing.T) {
	h := newHarness(description("", nil))
	now := t0
	h.svc.Now = func() time.Time { return now }
	rec := Record{At: now, System: "shop"}
	if c, _ := h.svc.claim(context.Background(), "k", rec); c != claimed {
		t.Fatalf("first claim %v", c)
	}
	if c, _ := h.svc.claim(context.Background(), "k", rec); c != claimBusy {
		t.Errorf("second claim during the lease %v", c)
	}
	now = now.Add(lease + time.Second) // the replica holding it died
	if c, _ := h.svc.claim(context.Background(), "k", rec); c != claimed {
		t.Errorf("claim after the lease %v", c)
	}
	h.svc.finish(context.Background(), "k", true, nil, "", false)
	if c, _ := h.svc.claim(context.Background(), "k", rec); c != claimSent {
		t.Errorf("claim after sending %v", c)
	}
}
