// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package tailmute keeps the consequences of an incident quiet for a while after its cause is
// gone. Rules that look back over a window keep firing for minutes after the fix, and
// Alertmanager stops inhibiting them the moment the cause resolves, so they would arrive as if
// something new broke.
package tailmute

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// Interval between passes. A silence is prolonged on every pass while its alert is inhibited, so
// when the cause resolves at least Grace minus Interval of it is left: nothing has to react to
// the resolve in time.
const Interval = 30 * time.Second

// DefaultGrace covers error-rate windows and most of a fast error-budget burn.
const DefaultGrace = 10 * time.Minute

// Alertmanager is the part of the client this package needs.
type Alertmanager interface {
	AlertsWithInhibited(ctx context.Context, filter map[string]string) ([]alertmanager.Alert, error)
	SetSilence(ctx context.Context, sil alertmanager.Silence, now time.Time) (string, error)
}

type Service struct {
	Descriptions descstore.Store
	// For returns the Alertmanager of the system's set of programs; nil when there is none or it
	// cannot take silences.
	For    func(ctx context.Context, sd *v1alpha1.SystemDescription) Alertmanager
	Grace  time.Duration
	Logger *slog.Logger
	Now    func() time.Time

	mu sync.Mutex
	// silences: alert fingerprint to the silence that covers it.
	silences map[string]held
}

type held struct {
	id   string
	ends time.Time
}

// NeedLeaderElection: one replica writes silences.
func (s *Service) NeedLeaderElection() bool { return true }

func (s *Service) Start(ctx context.Context) error {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.Pass(ctx)
		}
	}
}

// Pass prolongs the silence of every alert that an inhibit rule is muting right now.
func (s *Service) Pass(ctx context.Context) {
	if s.Descriptions == nil || s.For == nil || s.Grace <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, Interval)
	defer cancel()
	list, err := s.Descriptions.List(ctx)
	if err != nil {
		s.log().Warn("tail mute: descriptions unavailable", "error", err)
		return
	}
	now := s.now()
	for _, snap := range list {
		if snap.SD == nil {
			continue
		}
		am := s.For(ctx, snap.SD)
		if am == nil {
			continue
		}
		alerts, err := am.AlertsWithInhibited(ctx, map[string]string{rules.LabelManaged: "true", generator.LabelSystem: snap.SD.Name})
		if err != nil {
			s.log().Warn("tail mute: alerts unavailable", "system", snap.SD.Name, "error", err)
			continue
		}
		for _, a := range alerts {
			if !a.Inhibited() || a.Fingerprint == "" {
				continue
			}
			s.prolong(ctx, am, snap.SD.Name, a, now)
		}
	}
	s.forget(now)
}

func (s *Service) prolong(ctx context.Context, am Alertmanager, system string, a alertmanager.Alert, now time.Time) {
	s.mu.Lock()
	h := s.silences[a.Fingerprint]
	s.mu.Unlock()
	if !h.ends.After(now) {
		// An expired silence cannot be prolonged.
		h = held{}
	}
	ends := now.Add(s.Grace)
	id, err := am.SetSilence(ctx, alertmanager.Silence{
		ID: h.id, Labels: a.Labels, EndsAt: ends,
		Comment: fmt.Sprintf("Kamerton: a consequence of an incident in %s, kept quiet for %s after its cause is gone", system, rules.FormatDuration(s.Grace)),
	}, now)
	if err != nil {
		s.log().Warn("tail mute: silence not set", "system", system, "alert", a.Labels["alertname"], "service", a.Labels[rules.LabelService], "error", err)
		return
	}
	if h.id == "" {
		s.log().Info("tail mute: silence set", "system", system, "alert", a.Labels["alertname"], "service", a.Labels[rules.LabelService])
	}
	s.mu.Lock()
	if s.silences == nil {
		s.silences = map[string]held{}
	}
	s.silences[a.Fingerprint] = held{id: id, ends: ends}
	s.mu.Unlock()
}

func (s *Service) forget(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for fp, h := range s.silences {
		if !h.ends.After(now) {
			delete(s.silences, fp)
		}
	}
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
