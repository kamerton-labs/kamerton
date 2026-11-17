// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/internal/telegram"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

var errorRateKey = Key{Alert: "KamertonErrorRateHigh", Service: "checkout"}

func TestRecordAndCounts(t *testing.T) {
	ctx := context.Background()
	f := &Feedback{}
	if _, err := f.Record(ctx, "shop", errorRateKey, "maybe"); err != ErrBadVerdict {
		t.Errorf("bad verdict: %v", err)
	}
	_, _ = f.Record(ctx, "shop", errorRateKey, VerdictNoise)
	c, err := f.Record(ctx, "shop", errorRateKey, VerdictNoise)
	if err != nil || c != (Counts{Noise: 2}) {
		t.Fatalf("counts %+v, %v", c, err)
	}
	_, _ = f.Record(ctx, "shop", errorRateKey, VerdictUseful)
	_, _ = f.Record(ctx, "billing", errorRateKey, VerdictUseful)
	got, _ := f.Counts(ctx, "shop")
	if len(got) != 1 || got[errorRateKey] != (Counts{Useful: 1, Noise: 2}) {
		t.Errorf("shop counts %+v", got)
	}
}

func TestCountersAreBounded(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	s, _ := cloneState(&State{})
	for i := 0; i < maxRules+50; i++ {
		s.add("shop", Key{Alert: fmt.Sprintf("KamertonCustomRule%d", i), Service: "checkout"}, 0, 1, now.Add(time.Duration(i)*time.Second))
	}
	if len(s.Rules) != maxRules {
		t.Fatalf("rules = %d", len(s.Rules))
	}
	if s.find("shop", Key{Alert: "KamertonCustomRule0", Service: "checkout"}) >= 0 {
		t.Error("the least recently changed rule should be dropped")
	}
	c := s.add("shop", errorRateKey, maxCount+10, -5, now)
	if c.Useful != maxCount || c.Noise != 0 {
		t.Errorf("counts not clamped: %+v", c)
	}

	for i := 0; i < maxMessages+20; i++ {
		s.Messages[fmt.Sprintf("ref%d", i)] = Message{System: "shop", At: now.Add(time.Duration(i) * time.Second)}
	}
	s.Messages["old"] = Message{System: "shop", At: now.Add(-messageTTL - time.Hour)}
	s.pruneMessages(now.Add(time.Hour))
	if _, ok := s.Messages["old"]; ok || len(s.Messages) != maxMessages {
		t.Errorf("messages = %d", len(s.Messages))
	}
	if _, ok := s.Messages["ref0"]; ok {
		t.Error("the oldest message should be dropped")
	}
}

func TestConcurrentRecords(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	stores := map[string]StateStore{
		"memory":    &MemoryStateStore{},
		"configmap": &ConfigMapStateStore{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Namespace: "kamerton", Name: "kamerton-noise-state"},
	}
	for name, st := range stores {
		f := &Feedback{State: st}
		const n = 8
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				v := VerdictNoise
				if i%2 == 0 {
					v = VerdictUseful
				}
				if _, err := f.Record(ctx, "shop", errorRateKey, v); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}(i)
		}
		wg.Wait()
		got, err := f.Counts(ctx, "shop")
		if err != nil || got[errorRateKey] != (Counts{Useful: n / 2, Noise: n / 2}) {
			t.Errorf("%s: counts %+v, %v", name, got, err)
		}
	}
}

func TestCallbackData(t *testing.T) {
	sd := shop(t, shopYAML)
	f := &Feedback{}
	kb := f.Buttons(context.Background(), sd, []triage.Alert{
		{Name: "KamertonErrorRateHigh", Service: "checkout"},
		{Name: "KamertonErrorRateHigh", Service: "checkout"},
		{Name: "KamertonTelemetryMissing"},
	})
	if len(kb) != 1 || len(kb[0]) != 2 {
		t.Fatalf("keyboard %+v", kb)
	}
	for _, b := range kb[0] {
		if len(b.CallbackData) > telegram.MaxCallbackData {
			t.Errorf("callback data %q is longer than %d bytes", b.CallbackData, telegram.MaxCallbackData)
		}
		v, before, ref, ok := parseCallback(b.CallbackData)
		if !ok || before != '-' || (v != 'u' && v != 'n') || len(ref) != 16 {
			t.Errorf("callback data %q", b.CallbackData)
		}
	}
	s, _ := f.store().Update(context.Background(), func(*State) error { return ErrNoChange })
	for _, m := range s.Messages {
		if len(m.Rules) != 1 || m.Rules[0] != errorRateKey {
			t.Errorf("message rules %+v", m.Rules)
		}
	}
	for _, bad := range []string{"", "kn", "kn:ux:abcdefghijklmnop", "kn:u-:short", "xx:u-:abcdefghijklmnop", "kn:u-:abcdefghijklmnop:extra", strings.Repeat("a", 64)} {
		if _, _, _, ok := parseCallback(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if kb := f.Buttons(context.Background(), sd, []triage.Alert{{Name: "KamertonTelemetryMissing"}}); kb != nil {
		t.Errorf("no buttons without a rule of a service: %+v", kb)
	}
}

func TestButtonLanguages(t *testing.T) {
	en := Keyboard(shop(t, shopYAML), "abcdefghijklmnop", '-')
	if en[0][0].Text != "Useful" || en[0][1].Text != "Noise" {
		t.Errorf("english buttons %+v", en)
	}
	ru := shop(t, shopYAML+"  notifications:\n    language: ru\n")
	kb := Keyboard(ru, "abcdefghijklmnop", 'n')
	if kb[0][0].Text != "По делу" || kb[0][1].Text != "✓ Шум" {
		t.Errorf("russian buttons %+v", kb)
	}
	if kb[0][0].CallbackData != "kn:un:abcdefghijklmnop" || kb[0][1].CallbackData != "kn:nn:abcdefghijklmnop" {
		t.Errorf("callback data after a choice: %+v", kb)
	}
}
