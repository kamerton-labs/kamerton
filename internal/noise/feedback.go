// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/telegram"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// maxRulesPerMessage bounds the root alerts one press counts for.
const maxRulesPerMessage = 5

var ErrBadVerdict = errors.New(`verdict must be "useful" or "noise"`)

// Feedback keeps the verdicts of people, from the API and from the buttons
// under follow-up messages in Telegram.
type Feedback struct {
	// State nil: memory, for file and local mode.
	State        StateStore
	Descriptions descstore.Store
	// Receiver reports whether a chat gets the alerts of sd through
	// Kamerton's bot; nil accepts no press.
	Receiver func(sd *v1alpha1.SystemDescription, chatID int64) bool
	Now      func() time.Time
	Logger   *slog.Logger

	memory MemoryStateStore
}

func (f *Feedback) store() StateStore {
	if f.State != nil {
		return f.State
	}
	return &f.memory
}

func (f *Feedback) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Feedback) log() *slog.Logger {
	if f.Logger != nil {
		return f.Logger
	}
	return slog.Default()
}

func (f *Feedback) Record(ctx context.Context, system string, k Key, verdict string) (Counts, error) {
	du, dn := 0, 0
	switch verdict {
	case VerdictUseful:
		du = 1
	case VerdictNoise:
		dn = 1
	default:
		return Counts{}, ErrBadVerdict
	}
	var c Counts
	_, err := f.store().Update(ctx, func(s *State) error {
		c = s.add(system, k, du, dn, f.now())
		return nil
	})
	return c, err
}

// Counts returns the verdicts about the rules of one system.
func (f *Feedback) Counts(ctx context.Context, system string) (map[Key]Counts, error) {
	s, err := f.store().Update(ctx, func(*State) error { return ErrNoChange })
	if err != nil {
		return nil, err
	}
	out := map[Key]Counts{}
	for _, r := range s.Rules {
		if r.System == system && (r.Useful > 0 || r.Noise > 0) {
			out[r.Key] = r.Counts
		}
	}
	return out, nil
}

// Callback data is "kn:<verdict><before>:<ref>" (u or n; before: the verdict
// this chat gave earlier, or -). Nothing in it is secret; a forged one only
// counts from a chat that receives the system's alerts.
const callbackPrefix = "kn"

var refPattern = regexp.MustCompile(`^[a-z2-7]{16}$`)

func newRef() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.EncodeToString(b)), nil
}

func callbackData(verdict, before byte, ref string) string {
	return callbackPrefix + ":" + string([]byte{verdict, before}) + ":" + ref
}

func parseCallback(data string) (verdict, before byte, ref string, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != callbackPrefix || len(parts[1]) != 2 || !refPattern.MatchString(parts[2]) {
		return 0, 0, "", false
	}
	verdict, before = parts[1][0], parts[1][1]
	if (verdict != 'u' && verdict != 'n') || (before != 'u' && before != 'n' && before != '-') {
		return 0, 0, "", false
	}
	return verdict, before, parts[2], true
}

type words struct {
	useful, noise                                   string
	recordedUseful, recordedNoise, already, foreign string
	expired, unknown                                string
}

var texts = map[string]words{
	rules.LanguageEnglish: {
		useful: "Useful", noise: "Noise",
		recordedUseful: "Thanks, marked as useful", recordedNoise: "Thanks, marked as noise",
		already: "Already marked", foreign: "This chat does not get the alerts of this system",
		expired: "These buttons have expired", unknown: "Unknown button",
	},
	rules.LanguageRussian: {
		useful: "По делу", noise: "Шум",
		recordedUseful: "Спасибо, отмечено: по делу", recordedNoise: "Спасибо, отмечено: шум",
		already: "Уже отмечено", foreign: "Этот чат не получает оповещения этой системы",
		expired: "Кнопки устарели", unknown: "Неизвестная кнопка",
	},
}

func language(sd *v1alpha1.SystemDescription) words {
	if sd != nil && sd.Spec.Notifications != nil {
		if w, ok := texts[sd.Spec.Notifications.Language]; ok {
			return w
		}
	}
	return texts[rules.LanguageEnglish]
}

// Keyboard is the row of buttons under a follow-up; chosen ('u', 'n' or '-')
// is marked with a check.
func Keyboard(sd *v1alpha1.SystemDescription, ref string, chosen byte) telegram.Keyboard {
	w := language(sd)
	label := func(v byte, text string) string {
		if v == chosen {
			return "✓ " + text
		}
		return text
	}
	return telegram.Keyboard{{
		{Text: label('u', w.useful), CallbackData: callbackData('u', chosen, ref)},
		{Text: label('n', w.noise), CallbackData: callbackData('n', chosen, ref)},
	}}
}

// Buttons remembers the root alerts of an incident under a new reference
// and returns the buttons for its follow-up; nil when there is nothing to
// ask about or the state cannot be written.
func (f *Feedback) Buttons(ctx context.Context, sd *v1alpha1.SystemDescription, alerts []triage.Alert) telegram.Keyboard {
	var keys []Key
	seen := map[Key]bool{}
	for _, a := range alerts {
		if a.Name == "" || a.Service == "" || len(keys) == maxRulesPerMessage {
			continue
		}
		k := KeyOf(sd, a.Name, a.Service, a.Operation)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	ref, err := newRef()
	if err != nil {
		return nil
	}
	now := f.now()
	if _, err := f.store().Update(ctx, func(s *State) error {
		s.pruneMessages(now)
		s.Messages[ref] = Message{System: sd.Name, Rules: keys, At: now}
		return nil
	}); err != nil {
		f.log().Warn("noise: follow-up goes without buttons, state unavailable", "system", sd.Name, "error", err)
		return nil
	}
	return Keyboard(sd, ref, '-')
}

// HandleCallback records a press. A press repeating the verdict this chat
// already gave changes nothing; a press of the other button moves the vote.
func (f *Feedback) HandleCallback(ctx context.Context, chatID int64, data string) telegram.CallbackResult {
	en := texts[rules.LanguageEnglish]
	verdict, before, ref, ok := parseCallback(data)
	if !ok {
		return telegram.CallbackResult{Text: en.unknown}
	}
	s, err := f.store().Update(ctx, func(*State) error { return ErrNoChange })
	if err != nil {
		f.log().Warn("noise: cannot read state for a button press", "error", err)
		return telegram.CallbackResult{}
	}
	msg, ok := s.Messages[ref]
	if !ok || f.now().Sub(msg.At) > messageTTL {
		return telegram.CallbackResult{Text: en.expired}
	}
	sd := f.description(ctx, msg.System)
	if sd == nil {
		return telegram.CallbackResult{Text: en.expired}
	}
	w := language(sd)
	if f.Receiver == nil || !f.Receiver(sd, chatID) {
		f.log().Info("noise: button press from a chat that does not get the alerts of the system", "system", sd.Name)
		return telegram.CallbackResult{Text: w.foreign}
	}
	if verdict == before {
		return telegram.CallbackResult{Text: w.already}
	}
	delta := func(v byte) (int, int) {
		if v == 'u' {
			return 1, 0
		}
		return 0, 1
	}
	now := f.now()
	if _, err := f.store().Update(ctx, func(s *State) error {
		for _, k := range msg.Rules {
			du, dn := delta(verdict)
			if before != '-' {
				bu, bn := delta(before)
				du, dn = du-bu, dn-bn
			}
			s.add(msg.System, k, du, dn, now)
		}
		return nil
	}); err != nil {
		f.log().Warn("noise: cannot record a button press", "system", sd.Name, "error", err)
		return telegram.CallbackResult{}
	}
	text := w.recordedNoise
	if verdict == 'u' {
		text = w.recordedUseful
	}
	return telegram.CallbackResult{Text: text, Keyboard: Keyboard(sd, ref, verdict)}
}

func (f *Feedback) description(ctx context.Context, system string) *v1alpha1.SystemDescription {
	if f.Descriptions == nil {
		return nil
	}
	list, err := f.Descriptions.List(ctx)
	if err != nil {
		return nil
	}
	for _, snap := range list {
		if snap.SD != nil && snap.SD.Name == system {
			return snap.SD
		}
	}
	return nil
}
