// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package history

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/telegram"
)

type replyWords struct{ saved, tooLong string }

var replyTexts = map[string]replyWords{
	rules.LanguageEnglish: {saved: "Saved: what helped.", tooLong: "Too long: at most %d characters. Nothing was saved."},
	rules.LanguageRussian: {saved: "Сохранено: что помогло.", tooLong: "Слишком длинно: не больше %d символов. Ничего не сохранено."},
}

// HandleReply stores a reply to an "incident is over" message as the "what
// helped" note of that incident; a later reply replaces it. Replies to other
// messages and from chats that are not receivers of the system are ignored.
func (s *Service) HandleReply(ctx context.Context, chatID, replyTo int64, text string) telegram.ReplyResult {
	st, err := s.Snapshot(ctx)
	if err != nil {
		s.log().Warn("incident history unavailable for a Telegram reply", "error", err)
		return telegram.ReplyResult{}
	}
	var rec *Record
	for i := range st.Records {
		for _, a := range st.Records[i].Asked {
			if a.Chat == chatID && a.Message == replyTo {
				rec = &st.Records[i]
			}
		}
	}
	if rec == nil {
		return telegram.ReplyResult{}
	}
	sd := s.description(ctx, rec.System)
	if sd == nil || s.Receiver == nil || !s.Receiver(sd, chatID) {
		s.log().Info("incident history: reply from a chat that does not get the alerts of the system", "system", rec.System)
		return telegram.ReplyResult{}
	}
	w := replyTexts[rules.LanguageEnglish]
	if n := sd.Spec.Notifications; n != nil {
		if x, ok := replyTexts[n.Language]; ok {
			w = x
		}
	}
	in := NoteInput{Fix: &text}
	if err := CheckNote(&in); err != nil {
		return telegram.ReplyResult{Text: fmt.Sprintf(w.tooLong, MaxFix)}
	}
	if *in.Fix == "" {
		return telegram.ReplyResult{}
	}
	if _, err := s.SetNote(ctx, rec.ID, in, SourceTelegram); err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.log().Warn("incident history: cannot store a Telegram reply", "system", rec.System, "error", err)
		}
		return telegram.ReplyResult{}
	}
	return telegram.ReplyResult{React: true, Text: w.saved}
}

func (s *Service) description(ctx context.Context, system string) *v1alpha1.SystemDescription {
	if s.Descriptions == nil {
		return nil
	}
	list, err := s.Descriptions.List(ctx)
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
