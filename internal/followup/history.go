// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

import (
	"context"
	"time"

	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/history"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// SweepInterval: open records are checked against Alertmanager this often,
// so a resolve Kamerton missed closes the record within it.
const SweepInterval = 5 * time.Minute

func (s *Service) observe(ctx context.Context, o history.Observation) {
	if s.History == nil {
		return
	}
	if _, err := s.History.Observe(ctx, o); err != nil {
		s.log().Warn("incident history unavailable", "system", o.System, "error", err)
	}
}

// observeHook brings the history up to date at once on every notification,
// firing or resolved; the follow-up itself waits.
func (s *Service) observeHook(ctx context.Context, hook *alertmanager.Webhook) {
	if s.History == nil || hook == nil {
		return
	}
	var firing []alertmanager.WebhookAlert
	resolved := map[string]bool{}
	ended := map[string]time.Time{}
	system := hook.CommonLabels[generator.LabelSystem]
	for _, a := range hook.Alerts {
		if a.Labels[rules.LabelManaged] != "true" {
			continue
		}
		if system == "" {
			system = a.Labels[generator.LabelSystem]
		}
		switch a.Status {
		case "firing":
			firing = append(firing, a)
		case "resolved":
			resolved[a.Fingerprint] = true
			end := a.EndsAt
			if end.IsZero() {
				end = s.now()
			}
			ended[history.EndKey(a.Labels["alertname"], a.Labels[rules.LabelService])] = end
		}
	}
	sd := s.description(ctx, system)
	if sd == nil {
		return
	}
	alerts, complete := s.systemAlerts(ctx, sd, firing, resolved)
	s.observe(ctx, history.Observation{
		System: sd.Name, Incidents: triage.NewGraph(&sd.Spec).Incidents(sd.Name, alerts), Complete: complete, Ended: ended,
	})
}

// NeedLeaderElection: one replica sweeps.
func (s *Service) NeedLeaderElection() bool { return true }

// Start sweeps the open incident records until ctx ends.
func (s *Service) Start(ctx context.Context) error {
	t := time.NewTicker(SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep checks the systems with open records against Alertmanager. A record
// of a deleted system ends now.
func (s *Service) Sweep(ctx context.Context) {
	if s.History == nil {
		return
	}
	st, err := s.History.Snapshot(ctx)
	if err != nil {
		s.log().Warn("incident history unavailable", "error", err)
		return
	}
	for _, system := range st.OpenSystems() {
		sd := s.description(ctx, system)
		if sd == nil {
			if s.Descriptions != nil {
				if _, err := s.Descriptions.List(ctx); err == nil {
					s.observe(ctx, history.Observation{System: system, Complete: true})
				}
			}
			continue
		}
		if s.Alerts == nil {
			continue
		}
		alerts, complete := s.systemAlerts(ctx, sd, nil, nil)
		if !complete {
			// Still applies the bound on records nobody could confirm.
			s.observe(ctx, history.Observation{System: system})
			continue
		}
		s.observe(ctx, history.Observation{System: system, Incidents: triage.NewGraph(&sd.Spec).Incidents(sd.Name, alerts), Complete: true})
	}
}

// RecordsClosed sends the "incident is over" messages in the background; it
// is the OnClose of the history.
func (s *Service) RecordsClosed(_ context.Context, closed []history.Record) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, r := range closed {
			s.askWhatHelped(ctx, r)
		}
	}()
}

// askWhatHelped goes only to the Telegram receivers that got the follow-up,
// and only after an incident whose end was seen and that lasted long enough.
func (s *Service) askWhatHelped(ctx context.Context, r history.Record) {
	if s.History == nil || s.Telegram == nil || len(r.FollowUpTo) == 0 || r.AskSent || r.EndEstimated || r.Duration() < history.AskAfter {
		return
	}
	sd := s.description(ctx, r.System)
	if sd == nil || !sd.Spec.Notifications.AsksWhatHelped() {
		return
	}
	if !s.History.ClaimAsk(ctx, r.ID) {
		return
	}
	n := sd.Spec.Notifications
	lang := n.Language
	if lang == "" {
		lang = rules.LanguageEnglish
	}
	msg := generator.RenderIncidentOver(generator.IncidentOver{
		System: sd.Name, Environment: sd.Spec.Environment, Roots: r.Roots, Title: overTitle(r), Duration: r.Duration(),
	}, lang)
	var asked []history.Asked
	seen := map[int64]bool{}
	for i := range n.Receivers {
		rcv := &n.Receivers[i]
		if rcv.Telegram == nil || seen[rcv.Telegram.ChatID] || !contains(r.FollowUpTo, rcv.Name) {
			continue
		}
		seen[rcv.Telegram.ChatID] = true
		id, err := s.Telegram.SendTracked(ctx, rcv.Telegram.BotTokenSecret, rcv.Telegram.ChatID, msg)
		if err != nil {
			s.log().Warn("\"incident is over\" message to Telegram failed", "system", sd.Name, "receiver", rcv.Name, "error", err)
			continue
		}
		asked = append(asked, history.Asked{Chat: rcv.Telegram.ChatID, Message: id})
	}
	if err := s.History.RecordAsked(ctx, r.ID, asked); err != nil {
		s.log().Warn("incident history: cannot record the \"incident is over\" messages", "system", sd.Name, "error", err)
	}
	s.log().Info("\"incident is over\" message sent", "system", sd.Name, "incident", r.ID, "telegram", len(asked))
}

// overTitle is the title of the first root's alert that explains others most.
func overTitle(r history.Record) string {
	if len(r.Roots) == 0 {
		return ""
	}
	title := ""
	for _, a := range r.RootAlerts {
		if a.Service != r.Roots[0] {
			continue
		}
		if rules.IsSymptomSource(a.Name) {
			return a.Title
		}
		if title == "" {
			title = a.Title
		}
	}
	return title
}
