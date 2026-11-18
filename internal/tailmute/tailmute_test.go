// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package tailmute

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/descstore"
)

type fakeAM struct {
	alerts []alertmanager.Alert
	set    []alertmanager.Silence
}

func (f *fakeAM) AlertsWithInhibited(context.Context, map[string]string) ([]alertmanager.Alert, error) {
	return f.alerts, nil
}

func (f *fakeAM) SetSilence(_ context.Context, s alertmanager.Silence, _ time.Time) (string, error) {
	f.set = append(f.set, s)
	if s.ID != "" {
		return s.ID, nil
	}
	return "sil-" + s.Labels["alertname"], nil
}

func alert(name, service string, inhibited bool) alertmanager.Alert {
	a := alertmanager.Alert{Fingerprint: name + "@" + service, Labels: map[string]string{"alertname": name, "service": service, "kamerton_system": "otel-demo"}}
	if inhibited {
		a.Status.InhibitedBy = []string{"root"}
	}
	return a
}

func TestPassSilencesOnlyInhibitedAlertsAndLetsThemRunOut(t *testing.T) {
	example, err := os.ReadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "otel-demo.yaml"), example, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := descstore.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	am := &fakeAM{alerts: []alertmanager.Alert{
		alert("KamertonNoReadyPods", "payment", false),
		alert("KamertonErrorRateHigh", "checkout", true),
	}}
	s := &Service{
		Descriptions: store, Grace: 10 * time.Minute, Now: func() time.Time { return now },
		For: func(context.Context, *v1alpha1.SystemDescription) Alertmanager { return am },
	}

	s.Pass(context.Background())
	if len(am.set) != 1 || am.set[0].ID != "" || am.set[0].Labels["service"] != "checkout" || !am.set[0].EndsAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("first pass: %+v", am.set)
	}

	// The next pass moves the end of the same silence.
	now = now.Add(Interval)
	s.Pass(context.Background())
	if len(am.set) != 2 || am.set[1].ID != "sil-KamertonErrorRateHigh" || !am.set[1].EndsAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("second pass: %+v", am.set)
	}

	// The cause is gone: the alert is no longer inhibited, so nothing is prolonged and the
	// silence runs out by itself.
	am.alerts = []alertmanager.Alert{alert("KamertonErrorRateHigh", "checkout", false)}
	now = now.Add(Interval)
	s.Pass(context.Background())
	if len(am.set) != 2 {
		t.Fatalf("after the cause is gone nothing is set: %+v", am.set[2:])
	}

	// A new incident after the silence ran out starts a new one.
	now = now.Add(11 * time.Minute)
	am.alerts = []alertmanager.Alert{alert("KamertonErrorRateHigh", "checkout", true)}
	s.Pass(context.Background())
	if len(am.set) != 3 || am.set[2].ID != "" {
		t.Fatalf("after expiry: %+v", am.set)
	}
}

func TestPassOffWithoutGrace(t *testing.T) {
	am := &fakeAM{alerts: []alertmanager.Alert{alert("KamertonErrorRateHigh", "checkout", true)}}
	s := &Service{For: func(context.Context, *v1alpha1.SystemDescription) Alertmanager { return am }}
	s.Pass(context.Background())
	if len(am.set) != 0 {
		t.Fatalf("set without a grace: %+v", am.set)
	}
}
