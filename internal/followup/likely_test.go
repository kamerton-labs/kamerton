// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/evidence"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// promFunc answers each query by its text; unknown queries get no data.
type promFunc func(q string) model.Vector

func (f promFunc) Query(_ context.Context, q string, _ time.Time) (model.Value, error) {
	if v := f(q); v != nil {
		return v, nil
	}
	return model.Vector{}, nil
}

func (f promFunc) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (model.Matrix, error) {
	return nil, nil
}

// paymentDown: payment has no ready pods and its "requests stopped" alert is
// pending; checkout's failing operation is reported for the evidence.
func paymentDown(queries *[]string) promFunc {
	var mu sync.Mutex
	return func(q string) model.Vector {
		if queries != nil {
			mu.Lock()
			*queries = append(*queries, q)
			mu.Unlock()
		}
		switch {
		case strings.HasPrefix(q, "ALERTS{"):
			if !strings.Contains(q, `alertstate="pending"`) || !strings.Contains(q, `service=~"payment"`) {
				return nil
			}
			return model.Vector{
				{Metric: model.Metric{"alertname": rules.AlertRequestsStopped, "service": "payment", "alertstate": "pending"}, Value: 1},
				{Metric: model.Metric{"alertname": rules.AlertMemoryNearLimit, "service": "payment", "alertstate": "pending"}, Value: 1},
			}
		case strings.Contains(q, "kube_pod_status_ready") && strings.Contains(q, `pod=~"payment-`):
			return model.Vector{{Metric: model.Metric{"namespace": "shop"}, Value: 0}}
		case strings.HasPrefix(q, "topk(") && strings.Contains(q, `service_name="checkout"`):
			return model.Vector{{Metric: model.Metric{"span_name": "oteldemo.CheckoutService/PlaceOrder"}, Value: 7}}
		}
		return nil
	}
}

func TestFollowUpNamesLikelyCauseThenTheRootExplainsIt(t *testing.T) {
	h := newHarness(description("", nil))
	var queries []string
	h.svc.Sources = func(context.Context, *v1alpha1.SystemDescription) evidence.Sources {
		return evidence.Sources{Prom: paymentDown(&queries)}
	}
	// The caller's SLO alert arrives first; payment has no firing alert yet.
	slo := amAlert("checkout", rules.AlertSLOBurnFast, 1, false)
	h.active = []alertmanager.Alert{slo}
	res := h.svc.Process(context.Background(), hookFor(slo), t0.Add(2*time.Minute))
	if len(res) != 1 || res[0].Incident != "shop:checkout" || res[0].Telegram != 1 {
		t.Fatalf("result = %+v", res)
	}
	msg := h.tg.sent[0].html
	for _, want := range []string{
		"Alert: <b>checkout</b> (checkout: title of KamertonSLOBurnRateFast since 09:19 UTC+5).\n",
		"Likely cause: <b>payment</b>, called by checkout (observed now: No ready pods; Requests stopped arriving (alert pending)). Its own alerts have not fired yet.\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Probable cause: <b>checkout</b>") || strings.Contains(msg, "Memory") {
		t.Errorf("the consequence is named as the cause, or a non-symptom pending alert is listed:\n%s", msg)
	}
	ev := h.svc.Evidence("shop:checkout")
	if ev == nil || !ev.Likely || ev.Cause != "payment: No ready pods; Requests stopped arriving (alert pending)" ||
		ev.Operation != "oteldemo.CheckoutService/PlaceOrder" || ev.OperationFailures != 7 || !ev.CollectedAt.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("evidence = %+v", ev)
	}
	for _, q := range queries {
		if strings.Contains(q, "frontend") && strings.HasPrefix(q, "ALERTS{") {
			t.Errorf("checked a caller instead of the dependencies: %s", q)
		}
	}

	// Payment's own alert fires; checkout's SLO alert is muted by it now.
	stopped := amAlert("payment", rules.AlertRequestsStopped, 3, false)
	h.active = []alertmanager.Alert{stopped, amAlert("checkout", rules.AlertSLOBurnFast, 1, true)}
	res = h.svc.Process(context.Background(), hookFor(stopped), t0.Add(4*time.Minute))
	if len(res) != 1 || res[0].Incident != "shop:payment" || res[0].Telegram != 1 {
		t.Fatalf("root result = %+v", res)
	}
	msg = h.tg.sent[1].html
	for _, want := range []string{
		"Probable cause: <b>payment</b> (payment: title of KamertonRequestsStopped since 09:21 UTC+5).\n",
		"This explains the earlier alert about <b>checkout</b> (since 09:19 UTC+5).\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("root message lacks %q:\n%s", want, msg)
		}
	}
	if ev := h.svc.Evidence("shop:payment"); ev == nil || ev.Likely || len(ev.Explains) != 1 || ev.Explains[0] != "checkout" ||
		ev.Cause != "payment: title of KamertonRequestsStopped since 09:21 UTC+5" {
		t.Errorf("root evidence = %+v", ev)
	}
}

func TestFollowUpExplainsAResolvedEarlierAlert(t *testing.T) {
	h := newHarness(description("", nil))
	h.svc.Sources = func(context.Context, *v1alpha1.SystemDescription) evidence.Sources {
		return evidence.Sources{Prom: paymentDown(nil)}
	}
	slo := amAlert("checkout", rules.AlertSLOBurnFast, 1, false)
	h.active = []alertmanager.Alert{slo}
	h.svc.Process(context.Background(), hookFor(slo), t0.Add(2*time.Minute))
	// checkout's alert is gone by the time payment's fires: the likely cause it named links them.
	stopped := amAlert("payment", rules.AlertRequestsStopped, 3, false)
	h.active = []alertmanager.Alert{stopped}
	h.svc.Process(context.Background(), hookFor(stopped), t0.Add(4*time.Minute))
	if len(h.tg.sent) != 2 || !strings.Contains(h.tg.sent[1].html, "This explains the earlier alert about <b>checkout</b>") {
		t.Errorf("sent %d:\n%v", len(h.tg.sent), h.tg.sent)
	}
}

func TestNoDependencyCheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		alerts []alertmanager.Alert
	}{
		{"the root has a cause of its own", []alertmanager.Alert{
			amAlert("checkout", rules.AlertNoReadyPods, 0, false), amAlert("checkout", rules.AlertErrorRate, 0, false)}},
		{"the root's alert is not something a dependency causes", []alertmanager.Alert{amAlert("checkout", rules.AlertOOMKilled, 0, false)}},
		{"the root has no dependencies", []alertmanager.Alert{amAlert("payment", rules.AlertErrorRate, 0, false)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(description("", nil))
			var queries []string
			h.svc.Sources = func(context.Context, *v1alpha1.SystemDescription) evidence.Sources {
				return evidence.Sources{Prom: paymentDown(&queries)}
			}
			h.active = tc.alerts
			h.svc.Process(context.Background(), hookFor(tc.alerts[0]), t0)
			for _, q := range queries {
				if strings.HasPrefix(q, "ALERTS{") || strings.Contains(q, "kube_pod_status_ready") {
					t.Errorf("dependency checked: %s", q)
				}
			}
			for _, m := range h.tg.sent {
				if strings.Contains(m.html, "Likely cause") {
					t.Errorf("likely cause named:\n%s", m.html)
				}
			}
		})
	}
}

func TestSystemWideAlertGetsNoFollowUp(t *testing.T) {
	h := newHarness(description("", nil))
	a := amAlert("", rules.AlertTelemetryMissing, 0, false)
	delete(a.Labels, rules.LabelService)
	h.active = []alertmanager.Alert{a}
	if res := h.svc.Process(context.Background(), hookFor(a), t0); len(res) != 0 || len(h.tg.sent) != 0 {
		t.Errorf("result %+v, sent %d", res, len(h.tg.sent))
	}
}

func TestEvidenceKeptWithoutRecipientsAndBounded(t *testing.T) {
	sd := description("", nil)
	sd.Spec.Notifications.Routing = &v1alpha1.Routing{}
	h := newHarness(sd)
	h.active = []alertmanager.Alert{amAlert("payment", rules.AlertErrorRate, 0, false)}
	res := h.svc.Process(context.Background(), hookFor(h.active[0]), t0)
	if len(res) != 1 || res[0].Skipped != "no recipients" {
		t.Fatalf("result = %+v", res)
	}
	if ev := h.svc.Evidence("shop:payment"); ev == nil || ev.Operation != "oteldemo.PaymentService/Charge" || ev.OperationFailures != 42 {
		t.Errorf("evidence without recipients = %+v", ev)
	}

	sum := *h.svc.Evidence("shop:payment")
	now := t0
	h.svc.Now = func() time.Time { return now }
	for i := range maxEvidence + 10 {
		now = t0.Add(time.Duration(i) * time.Second)
		h.svc.remember("shop:s"+itoa(int64(i)), sum)
	}
	h.svc.mu.Lock()
	n := len(h.svc.collected)
	h.svc.mu.Unlock()
	if n != maxEvidence {
		t.Errorf("%d entries kept, want %d", n, maxEvidence)
	}
	if h.svc.Evidence("shop:s0") != nil {
		t.Error("the oldest entry survived")
	}
	now = now.Add(DefaultTTL + time.Second)
	if h.svc.Evidence("shop:s209") != nil {
		t.Error("an entry outlived the TTL")
	}
}

func TestDedupSurvivesRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &ConfigMapStateStore{Client: kube, Namespace: "kamerton", Name: "kamerton-followup-state"}
	alerts := []alertmanager.Alert{amAlert("payment", rules.AlertRequestsStopped, 0, false), amAlert("checkout", rules.AlertErrorRate, 1, true)}

	first := newHarness(description("", nil))
	first.svc.State = store
	first.active = alerts
	if res := first.svc.Process(context.Background(), hookFor(alerts[0]), t0); len(res) != 1 || res[0].Telegram != 1 {
		t.Fatalf("first: %+v", res)
	}
	// A new process (restart, or another replica) with the same ConfigMap.
	second := newHarness(description("", nil))
	second.svc.State = store
	second.active = alerts
	if res := second.svc.Process(context.Background(), hookFor(alerts[0]), t0.Add(4*time.Hour)); len(res) != 1 || res[0].Skipped != "already sent" || len(second.tg.sent) != 0 {
		t.Errorf("after restart: %+v, sent %d", res, len(second.tg.sent))
	}
	st, err := store.Update(context.Background(), func(*State) error { return ErrNoChange })
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := st.Records["shop:payment@"+itoa(t0.Unix())]
	if !ok || !rec.Sent || rec.System != "shop" || len(rec.Roots) != 1 || !rec.Since.Equal(t0) {
		t.Errorf("records = %+v", st.Records)
	}
	// Records expire after the TTL.
	second.svc.Now = func() time.Time { return t0.Add(DefaultTTL + time.Hour) }
	if res := second.svc.Process(context.Background(), hookFor(alerts[0]), t0.Add(DefaultTTL+time.Hour)); len(res) != 1 || res[0].Skipped == "already sent" {
		t.Errorf("after the TTL: %+v", res)
	}
}

func TestBrokenStateStoreFallsBackToMemory(t *testing.T) {
	h := newHarness(description("", nil))
	h.svc.State = failingStore{}
	h.active = []alertmanager.Alert{amAlert("payment", rules.AlertRequestsStopped, 0, false), amAlert("checkout", rules.AlertErrorRate, 1, true)}
	r1 := h.svc.Process(context.Background(), hookFor(h.active[0]), t0)
	r2 := h.svc.Process(context.Background(), hookFor(h.active[0]), t0)
	if len(r1) != 1 || r1[0].Telegram != 1 || len(r2) != 1 || r2[0].Skipped != "already sent" {
		t.Errorf("results %+v %+v", r1, r2)
	}
}

func TestStatePrune(t *testing.T) {
	st := &State{Records: map[string]Record{}}
	for i := range maxRecords + 20 {
		st.Records[itoa(int64(i))] = Record{At: t0.Add(time.Duration(i) * time.Minute)}
	}
	st.Records["old"] = Record{At: t0.Add(-2 * DefaultTTL)}
	st.prune(t0.Add(time.Duration(maxRecords+20)*time.Minute), DefaultTTL)
	if len(st.Records) != maxRecords {
		t.Errorf("%d records, want %d", len(st.Records), maxRecords)
	}
	if _, ok := st.Records["old"]; ok {
		t.Error("expired record kept")
	}
	if _, ok := st.Records["0"]; ok {
		t.Error("oldest record kept over the bound")
	}
}

type failingStore struct{}

func (failingStore) Update(context.Context, func(*State) error) (*State, error) {
	return nil, errors.New("configmaps is forbidden")
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
