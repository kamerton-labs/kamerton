// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
)

// Errors returned to the API.
var (
	ErrNoToken      = errors.New("the bot token is not configured")
	ErrNotBot       = errors.New("the token belongs to an account that is not a bot")
	ErrNamespace    = errors.New("namespace mismatch")
	ErrInvalidInput = errors.New("invalid input")
)

// Service implements Telegram setup. One Service runs in every replica; only
// the leader runs the polling loop (Start), the others serve the UI from the
// shared state store.
type Service struct {
	API          *Client
	Tokens       TokenStore
	State        StateStore
	Descriptions descstore.Store
	// Namespace is Kamerton's namespace, where the token Secret lives.
	Namespace string
	// Identity names this replica in the poll status (the pod name).
	Identity string
	Logger   *slog.Logger
	// PollTimeout is the getUpdates long-poll timeout. Default 25s.
	PollTimeout time.Duration
	// Now is replaceable in tests.
	Now func() time.Time

	once sync.Once
	wake chan struct{}

	mu sync.Mutex
	// lastCheck limits getMe calls made from the status endpoint.
	lastCheck    time.Time
	lastCheckBot int64
	lastCheckErr string
	probedForBot int64
}

func (s *Service) init() {
	s.once.Do(func() {
		s.wake = make(chan struct{}, 1)
		if s.Logger == nil {
			s.Logger = slog.Default()
		}
		if s.API == nil {
			s.API = &Client{}
		}
		if s.PollTimeout == 0 {
			s.PollTimeout = 25 * time.Second
		}
		if s.Now == nil {
			s.Now = time.Now
		}
	})
}

// Wake makes a sleeping polling loop re-read the token now.
func (s *Service) Wake() {
	s.init()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ---- Views returned by the API (never contain the token) ----

// Status is the state shown in the UI.
type Status struct {
	Token       TokenStatus     `json:"token"`
	Polling     PollingStatus   `json:"polling"`
	Chats       []ChatView      `json:"chats"`
	Description DescriptionInfo `json:"description"`
}

// TokenStatus says whether a token is configured, never what it is.
type TokenStatus struct {
	Configured bool     `json:"configured"`
	Bot        *BotInfo `json:"bot,omitempty"`
	Problem    string   `json:"problem,omitempty"`
	SecretName string   `json:"secretName"`
	SecretKey  string   `json:"secretKey"`
	Namespace  string   `json:"namespace"`
}

// PollingStatus is the health of chat discovery.
type PollingStatus struct {
	PollStatus
	// Active means an update poll succeeded within the last two minutes.
	Active bool `json:"active"`
}

// ChatView is a chat with its routing from the description.
type ChatView struct {
	KnownChat
	// Verified is false for chats known only from the description.
	Verified bool        `json:"verified"`
	Receiver string      `json:"receiver,omitempty"`
	Routing  ChatRouting `json:"routing"`
	// UsedIn lists the systems that send notifications to this chat.
	UsedIn []ChatUse `json:"usedIn"`
}

// ChatUse is what one system sends to a chat.
type ChatUse struct {
	System string `json:"system"`
	ChatRouting
}

// system returns one system, or the default system for "".
func (s *Service) system(ctx context.Context, id string) (*descstore.Snapshot, error) {
	if id == "" {
		return descstore.Default(ctx, s.Descriptions)
	}
	snap, err := s.Descriptions.Get(ctx, id)
	if err == nil && snap.SD == nil {
		return nil, &descstore.InvalidError{Result: *snap.Invalid}
	}
	return snap, err
}

// usesByChat collects, for every chat, the systems routing alerts to it.
func (s *Service) usesByChat(ctx context.Context) map[int64][]ChatUse {
	out := map[int64][]ChatUse{}
	list, err := s.Descriptions.List(ctx)
	if err != nil {
		return out
	}
	for _, snap := range list {
		if snap.SD == nil {
			continue
		}
		for id, r := range s.routingByChat(snap.SD) {
			if len(r.routing.Severities) == 0 {
				continue
			}
			out[id] = append(out[id], ChatUse{System: snap.ID, ChatRouting: r.routing})
		}
	}
	return out
}

// ChatRouting is what the chat receives.
type ChatRouting struct {
	Severities []string `json:"severities"`
	// Services limits delivery to these services; empty means all services.
	Services []string `json:"services"`
	// Custom is true when the description routes this chat in a way the UI
	// cannot show exactly (edited by hand); saving replaces it.
	Custom bool `json:"custom,omitempty"`
}

// DescriptionInfo tells the UI what can be routed.
type DescriptionInfo struct {
	Loaded    bool     `json:"loaded"`
	Mode      string   `json:"mode"`
	Source    string   `json:"source,omitempty"`
	Name      string   `json:"name,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Services  []string `json:"services"`
	Writable  bool     `json:"writable"`
	// Problem explains why routing cannot be saved.
	Problem string `json:"problem,omitempty"`
}

// Status assembles the UI state for the default system.
func (s *Service) Status(ctx context.Context) (*Status, error) { return s.StatusFor(ctx, "") }

// StatusFor assembles the UI state: the bot and the chats (shared by all
// systems) with the routing of one system ("" means the default system)
// and, per chat, every system that uses it.
func (s *Service) StatusFor(ctx context.Context, systemID string) (*Status, error) {
	s.init()
	name, key := s.Tokens.Ref()
	out := &Status{Token: TokenStatus{SecretName: name, SecretKey: key, Namespace: s.Namespace}, Chats: []ChatView{}}
	token, err := s.Tokens.Get(ctx)
	if err != nil {
		out.Token.Problem = "cannot read the Secret: " + err.Error()
	}
	st, err := s.State.Get(ctx)
	if err != nil {
		return nil, err
	}
	if token != "" {
		out.Token.Configured = true
		botID, ok := BotIDFromToken(token)
		switch {
		case !ok:
			out.Token.Problem = "the value in the Secret does not look like a bot token"
		case st.Bot == nil || st.Bot.ID != botID:
			// The Secret was created outside the UI or replaced: check it once.
			if p := s.checkBot(ctx, token, botID); p != "" {
				out.Token.Problem = p
			}
			if st, err = s.State.Get(ctx); err != nil {
				return nil, err
			}
		}
		if st.Bot != nil && ok && st.Bot.ID == botID {
			out.Token.Bot = st.Bot
		}
		out.Polling = PollingStatus{PollStatus: st.Poll, Active: !st.Poll.LastPollAt.IsZero() && s.Now().Sub(st.Poll.LastPollAt) < 2*time.Minute}
	}

	snap, derr := s.system(ctx, systemID)
	out.Description = DescriptionInfo{Mode: s.Descriptions.Mode(), Services: []string{}, Writable: s.Descriptions.Mode() != descstore.ModeNone}
	if derr == nil {
		d := &out.Description
		d.Loaded, d.Source, d.Name, d.Namespace = true, snap.Source, snap.SD.Name, snap.SD.Namespace
		for _, svc := range snap.SD.Spec.Services {
			d.Services = append(d.Services, svc.Name)
		}
		if p := s.namespaceProblem(snap.SD); p != "" {
			d.Problem = p
		}
	} else if !errors.Is(derr, descstore.ErrNoDescription) {
		out.Description.Problem = derr.Error()
	} else {
		out.Description.Problem = "the system description is not saved yet"
	}

	if !out.Token.Configured {
		return out, nil
	}
	var sd *v1alpha1.SystemDescription
	if snap != nil {
		sd = snap.SD
	}
	routing := s.routingByChat(sd)
	uses := s.usesByChat(ctx)
	seen := map[int64]bool{}
	for _, c := range st.SortedChats() {
		v := ChatView{KnownChat: *c, Verified: true, UsedIn: uses[c.ID]}
		if r, ok := routing[c.ID]; ok {
			v.Receiver, v.Routing = r.receiver, r.routing
		}
		out.Chats = append(out.Chats, v)
		seen[c.ID] = true
	}
	ids := make([]int64, 0, len(routing))
	for id := range routing {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if seen[id] {
			continue
		}
		r := routing[id]
		out.Chats = append(out.Chats, ChatView{
			KnownChat: KnownChat{ID: id, Title: "", Type: "", Source: "description", Problem: "the chat is in the description, but the bot has not seen it yet"},
			Receiver:  r.receiver, Routing: r.routing, UsedIn: uses[id],
		})
	}
	return out, nil
}

// namespaceProblem explains when the description cannot use the token
// Secret: AlertmanagerConfig is created in the namespace of the description
// and Secret references cannot cross namespaces.
func (s *Service) namespaceProblem(sd *v1alpha1.SystemDescription) string {
	if s.Descriptions.Mode() != descstore.ModeCluster || s.Namespace == "" || sd.Namespace == "" || sd.Namespace == s.Namespace {
		return ""
	}
	secret, _ := s.Tokens.Ref()
	return fmt.Sprintf("description %s/%s is in another namespace than Kamerton (%s); Alertmanager cannot read the token from Secret %s/%s. Move the description to %s.",
		sd.Namespace, sd.Name, s.Namespace, s.Namespace, secret, s.Namespace)
}

// checkBot validates a token found in the store and records the bot. It
// calls Telegram at most once a minute per bot.
func (s *Service) checkBot(ctx context.Context, token string, botID int64) string {
	s.mu.Lock()
	if s.lastCheckBot == botID && s.Now().Sub(s.lastCheck) < time.Minute {
		p := s.lastCheckErr
		s.mu.Unlock()
		return p
	}
	s.lastCheck, s.lastCheckBot = s.Now(), botID
	s.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := s.recordBot(cctx, token)
	p := ""
	if err != nil {
		p = describeErr(err)
	}
	s.mu.Lock()
	s.lastCheckErr = p
	s.mu.Unlock()
	return p
}

// recordBot calls getMe and stores the bot. A different bot than before
// resets discovered chats: they belonged to the old bot.
func (s *Service) recordBot(ctx context.Context, token string) (*BotInfo, error) {
	me, err := s.API.GetMe(ctx, token)
	if err != nil {
		return nil, err
	}
	if !me.IsBot {
		return nil, ErrNotBot
	}
	info := &BotInfo{ID: me.ID, Username: me.Username, FirstName: me.FirstName, CheckedAt: s.Now().UTC()}
	_, err = s.State.Update(ctx, func(st *State) error {
		if st.Bot != nil && st.Bot.ID != me.ID {
			st.Chats = map[int64]*KnownChat{}
			st.Offset = 0
			st.Poll = PollStatus{}
		}
		st.Bot = info
		return nil
	})
	return info, err
}

// SetToken validates a token with getMe and stores it.
func (s *Service) SetToken(ctx context.Context, token string) (*BotInfo, error) {
	s.init()
	token = strings.TrimSpace(token)
	if !ValidTokenFormat(token) {
		return nil, ErrMalformedToken
	}
	me, err := s.API.GetMe(ctx, token)
	if err != nil {
		return nil, err
	}
	if !me.IsBot {
		return nil, ErrNotBot
	}
	if err := s.Tokens.Set(ctx, token); err != nil {
		return nil, err
	}
	info, err := s.recordBot(ctx, token)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.lastCheckBot, s.lastCheckErr, s.probedForBot = 0, "", 0
	s.mu.Unlock()
	s.Wake()
	secret, _ := s.Tokens.Ref()
	s.Logger.Info("telegram bot token stored", "bot", info.Username, "secret", s.Namespace+"/"+secret)
	return info, nil
}

// DeleteToken removes the Telegram receivers that use the token from the
// description, then the token, then the discovered chats.
func (s *Service) DeleteToken(ctx context.Context) error {
	s.init()
	if err := s.updateAll(ctx, func(sd *v1alpha1.SystemDescription) (bool, error) {
		return s.removeReceivers(sd, func(*v1alpha1.TelegramReceiver) bool { return true }), nil
	}); err != nil {
		return fmt.Errorf("remove Telegram receivers from the description: %w", err)
	}
	if err := s.Tokens.Delete(ctx); err != nil {
		return err
	}
	if _, err := s.State.Update(ctx, func(st *State) error {
		*st = State{Chats: map[int64]*KnownChat{}}
		return nil
	}); err != nil {
		return err
	}
	s.Wake()
	s.Logger.Info("telegram bot token removed")
	return nil
}

// AddChat checks a chat by ID (for chats the bot joined before polling
// started, whose updates are gone) and remembers it.
func (s *Service) AddChat(ctx context.Context, chatID int64) (*KnownChat, error) {
	s.init()
	token, botID, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	kc, err := s.checkChat(ctx, token, botID, chatID, "check")
	if err != nil {
		return nil, err
	}
	return kc, nil
}

// checkChat calls getChat/getChatMember and stores the result. It follows a
// group-to-supergroup migration reported by Telegram.
func (s *Service) checkChat(ctx context.Context, token string, botID, chatID int64, source string) (*KnownChat, error) {
	ch, err := s.API.GetChat(ctx, token, chatID)
	var ae *APIError
	if errors.As(err, &ae) && ae.MigrateToChatID != 0 {
		if merr := s.migrate(ctx, chatID, ae.MigrateToChatID); merr != nil {
			return nil, merr
		}
		chatID = ae.MigrateToChatID
		ch, err = s.API.GetChat(ctx, token, chatID)
	}
	if err != nil {
		return nil, err
	}
	status, present := "member", true
	if m, merr := s.API.GetChatMember(ctx, token, chatID, botID); merr == nil {
		status, present = m.Status, m.Present()
	}
	var out *KnownChat
	_, err = s.State.Update(ctx, func(st *State) error {
		kc := st.Chats[ch.ID]
		if kc == nil {
			kc = &KnownChat{ID: ch.ID, Source: source}
			st.Chats[ch.ID] = kc
		}
		kc.Title, kc.Type, kc.Status, kc.Available, kc.UpdatedAt = ch.DisplayTitle(), ch.Type, status, present, s.Now().UTC()
		kc.Problem = ""
		if !present {
			kc.Problem = "the bot is not a member of this chat"
		}
		cp := *kc
		out = &cp
		return nil
	})
	return out, err
}

// DeleteChat removes the chat's receivers from the description and forgets
// the chat. The bot stays in the chat until someone removes it there.
func (s *Service) DeleteChat(ctx context.Context, chatID int64) error {
	s.init()
	if err := s.updateAll(ctx, func(sd *v1alpha1.SystemDescription) (bool, error) {
		return s.removeReceivers(sd, func(t *v1alpha1.TelegramReceiver) bool { return t.ChatID == chatID }), nil
	}); err != nil {
		return fmt.Errorf("remove the chat from the description: %w", err)
	}
	_, err := s.State.Update(ctx, func(st *State) error {
		if _, ok := st.Chats[chatID]; !ok {
			return ErrNoChange
		}
		delete(st.Chats, chatID)
		return nil
	})
	return err
}

// TestResult is the outcome of a test message.
type TestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// MigratedTo is set when the group became a supergroup with a new ID.
	MigratedTo int64 `json:"migratedTo,omitempty"`
}

// testWords is the text of a test message in one language.
type testWords struct {
	head, anyChat, oneSystem, systems string
}

// testTexts are the test messages by notification language (see
// spec.notifications.language).
var testTexts = map[string]testWords{
	languageEnglish: {
		head:      "✅ <b>Kamerton</b>: test message.\n",
		anyChat:   "Alerts can be delivered to this chat.",
		oneSystem: "Alerts of system %s can be delivered to this chat.",
		systems:   "This chat receives alerts of: %s.",
	},
	languageRussian: {
		head:      "✅ <b>Kamerton</b>: тестовое сообщение.\n",
		anyChat:   "Оповещения могут приходить в этот чат.",
		oneSystem: "Оповещения системы %s могут приходить в этот чат.",
		systems:   "Сюда приходят оповещения: %s.",
	},
}

const (
	languageEnglish = "en"
	languageRussian = "ru"
)

// language is the language of a test message for the given systems: the
// notification language they share, English when they differ or none is set.
func (s *Service) language(ctx context.Context, systems []string) string {
	if len(systems) == 0 {
		return languageEnglish
	}
	list, err := s.Descriptions.List(ctx)
	if err != nil {
		return languageEnglish
	}
	want := map[string]bool{}
	for _, id := range systems {
		want[id] = true
	}
	lang := ""
	for _, snap := range list {
		if snap.SD == nil || !want[snap.ID] {
			continue
		}
		l := languageEnglish
		if n := snap.SD.Spec.Notifications; n != nil && n.Language != "" {
			l = n.Language
		}
		if lang != "" && l != lang {
			return languageEnglish
		}
		lang = l
	}
	if _, ok := testTexts[lang]; !ok {
		return languageEnglish
	}
	return lang
}

// SendTest sends a test message to a chat.
func (s *Service) SendTest(ctx context.Context, chatID int64) (*TestResult, error) {
	s.init()
	token, _, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	var systems, ids []string
	for _, u := range s.usesByChat(ctx)[chatID] {
		systems = append(systems, "<b>"+html.EscapeString(u.System)+"</b>")
		ids = append(ids, u.System)
	}
	w := testTexts[s.language(ctx, ids)]
	text := w.head + w.anyChat
	if len(systems) == 0 {
		if snap, err := descstore.Default(ctx, s.Descriptions); err == nil {
			w = testTexts[s.language(ctx, []string{snap.ID})]
			text = w.head + fmt.Sprintf(w.oneSystem, "<b>"+html.EscapeString(snap.ID)+"</b>")
		}
	} else {
		text = w.head + fmt.Sprintf(w.systems, strings.Join(systems, ", "))
	}
	err = s.API.SendMessage(ctx, token, chatID, text)
	if err == nil {
		_, _ = s.State.Update(ctx, func(st *State) error {
			if kc := st.Chats[chatID]; kc != nil && !kc.Available {
				kc.Available, kc.Problem, kc.UpdatedAt = true, "", s.Now().UTC()
				return nil
			}
			return ErrNoChange
		})
		return &TestResult{OK: true}, nil
	}
	res := &TestResult{Error: describeErr(err)}
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.MigrateToChatID != 0:
			res.MigratedTo = ae.MigrateToChatID
			res.Error = fmt.Sprintf("the group became a supergroup with new ID %d; the description is updated, send the test again", ae.MigrateToChatID)
			if merr := s.migrate(ctx, chatID, ae.MigrateToChatID); merr != nil {
				s.Logger.Warn("telegram migration failed", "from", chatID, "to", ae.MigrateToChatID, "error", merr)
				res.Error = fmt.Sprintf("the group became a supergroup with new ID %d, but the description could not be updated: %s", ae.MigrateToChatID, describeErr(merr))
			}
		case ae.Code == 403 || (ae.Code == 400 && strings.Contains(strings.ToLower(ae.Description), "chat not found")):
			_, _ = s.State.Update(ctx, func(st *State) error {
				kc := st.Chats[chatID]
				if kc == nil {
					return ErrNoChange
				}
				kc.Available, kc.Problem, kc.UpdatedAt = false, res.Error, s.Now().UTC()
				return nil
			})
		}
	}
	return res, nil
}

// RoutingInput is the routing chosen for one chat.
type RoutingInput struct {
	ChatID     int64    `json:"chatID"`
	Severities []string `json:"severities"`
	Services   []string `json:"services"`
}

// SaveRouting is SaveRoutingFor the default system.
func (s *Service) SaveRouting(ctx context.Context, in []RoutingInput) (*descstore.Snapshot, error) {
	return s.SaveRoutingFor(ctx, "", in)
}

// SaveRoutingFor writes receivers and routes for the given chats into the
// description of one system ("" means the default system). Chats not
// listed are left as they are. A chat with no severities loses its receiver.
func (s *Service) SaveRoutingFor(ctx context.Context, systemID string, in []RoutingInput) (*descstore.Snapshot, error) {
	s.init()
	if _, _, err := s.token(ctx); err != nil {
		return nil, err
	}
	for _, r := range in {
		for _, sev := range r.Severities {
			if sev != string(v1alpha1.SeverityCritical) && sev != string(v1alpha1.SeverityWarning) {
				return nil, fmt.Errorf("%w: unknown severity %q", ErrInvalidInput, sev)
			}
		}
		if r.ChatID == 0 {
			return nil, fmt.Errorf("%w: chat ID 0", ErrInvalidInput)
		}
	}
	if systemID == "" {
		systemID = descstore.DefaultID(ctx, s.Descriptions)
	}
	snap, err := s.updateDescription(ctx, systemID, func(sd *v1alpha1.SystemDescription) (bool, error) {
		if p := s.namespaceProblem(sd); p != "" {
			return false, fmt.Errorf("%w: %s", ErrNamespace, p)
		}
		for _, r := range in {
			s.applyRouting(sd, r)
		}
		return true, nil
	})
	if err == nil && snap == nil {
		return nil, descstore.ErrNoDescription
	}
	return snap, err
}

// ---- Description editing ----

func (s *Service) managed(r *v1alpha1.Receiver) bool {
	if r.Telegram == nil {
		return false
	}
	name, key := s.Tokens.Ref()
	return r.Telegram.BotTokenSecret.Name == name && r.Telegram.BotTokenSecret.Key == key
}

func (s *Service) updateDescription(ctx context.Context, id string, mutate func(*v1alpha1.SystemDescription) (bool, error)) (*descstore.Snapshot, error) {
	if id == "" {
		return nil, nil
	}
	cur, err := s.Descriptions.Get(ctx, id)
	if errors.Is(err, descstore.ErrNoDescription) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if cur.SD == nil {
		return nil, &descstore.InvalidError{Result: *cur.Invalid}
	}
	// Dry run first: do not write when nothing changes.
	if changed, err := mutate(cur.SD.DeepCopy()); err != nil || !changed {
		return cur, err
	}
	return s.Descriptions.Update(ctx, id, func(sd *v1alpha1.SystemDescription) error {
		_, err := mutate(sd)
		return err
	})
}

// updateAll applies mutate to every valid system (receivers and chats are
// shared by all systems).
func (s *Service) updateAll(ctx context.Context, mutate func(*v1alpha1.SystemDescription) (bool, error)) error {
	list, err := s.Descriptions.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, snap := range list {
		if snap.SD == nil {
			continue
		}
		if _, err := s.updateDescription(ctx, snap.ID, mutate); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", snap.ID, err))
		}
	}
	return errors.Join(errs...)
}

// removeReceivers deletes managed Telegram receivers matching match, and all
// references to them in routing. It reports whether anything changed.
func (s *Service) removeReceivers(sd *v1alpha1.SystemDescription, match func(*v1alpha1.TelegramReceiver) bool) bool {
	n := sd.Spec.Notifications
	if n == nil {
		return false
	}
	removed := map[string]bool{}
	kept := n.Receivers[:0:0]
	for i := range n.Receivers {
		r := n.Receivers[i]
		if s.managed(&r) && match(r.Telegram) {
			removed[r.Name] = true
			continue
		}
		kept = append(kept, r)
	}
	if len(removed) == 0 {
		return false
	}
	n.Receivers = kept
	dropRefs(n, removed)
	return true
}

func dropRefs(n *v1alpha1.Notifications, names map[string]bool) {
	r := n.Routing
	if r == nil {
		return
	}
	filter := func(list []string) []string {
		out := list[:0:0]
		for _, x := range list {
			if !names[x] {
				out = append(out, x)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	r.Critical, r.Warning = filter(r.Critical), filter(r.Warning)
	routes := r.Routes[:0:0]
	for _, rt := range r.Routes {
		if !names[rt.Receiver] {
			routes = append(routes, rt)
		}
	}
	if len(routes) == 0 {
		routes = nil
	}
	r.Routes = routes
}

// ReceiverName is the receiver name used for a chat added from the UI.
func ReceiverName(chatID int64) string {
	if chatID < 0 {
		return "telegram-n" + strconv.FormatInt(-chatID, 10)
	}
	return "telegram-" + strconv.FormatInt(chatID, 10)
}

func (s *Service) applyRouting(sd *v1alpha1.SystemDescription, in RoutingInput) {
	if sd.Spec.Notifications == nil {
		sd.Spec.Notifications = &v1alpha1.Notifications{}
	}
	n := sd.Spec.Notifications
	var names []string
	for i := range n.Receivers {
		if s.managed(&n.Receivers[i]) && n.Receivers[i].Telegram.ChatID == in.ChatID {
			names = append(names, n.Receivers[i].Name)
		}
	}
	all := map[string]bool{}
	for _, name := range names {
		all[name] = true
	}
	if len(in.Severities) == 0 {
		s.removeReceivers(sd, func(t *v1alpha1.TelegramReceiver) bool { return t.ChatID == in.ChatID })
		return
	}
	// Keep the first existing receiver (so a hand-written name such as
	// "oncall" survives), drop duplicates for the same chat.
	primary := ""
	if len(names) > 0 {
		primary = names[0]
		delete(all, primary)
		if len(all) > 0 {
			kept := n.Receivers[:0:0]
			for _, r := range n.Receivers {
				if !all[r.Name] {
					kept = append(kept, r)
				}
			}
			n.Receivers = kept
			dropRefs(n, all)
		}
	} else {
		primary = ReceiverName(in.ChatID)
		taken := map[string]bool{}
		for _, r := range n.Receivers {
			taken[r.Name] = true
		}
		for i := 2; taken[primary]; i++ {
			primary = ReceiverName(in.ChatID) + "-" + strconv.Itoa(i)
		}
		name, key := s.Tokens.Ref()
		n.Receivers = append(n.Receivers, v1alpha1.Receiver{Name: primary, Telegram: &v1alpha1.TelegramReceiver{
			ChatID: in.ChatID, BotTokenSecret: v1alpha1.SecretKeyRef{Name: name, Key: key},
		}})
	}
	if n.Routing == nil {
		n.Routing = &v1alpha1.Routing{}
	}
	dropRefs(n, map[string]bool{primary: true})
	sevs := uniqueSorted(in.Severities)
	services := uniqueSorted(in.Services)
	if len(services) == 0 {
		for _, sev := range sevs {
			if sev == string(v1alpha1.SeverityCritical) {
				n.Routing.Critical = append(n.Routing.Critical, primary)
			} else {
				n.Routing.Warning = append(n.Routing.Warning, primary)
			}
		}
		return
	}
	rt := v1alpha1.Route{Receiver: primary, Services: services}
	for _, sev := range sevs {
		rt.Severities = append(rt.Severities, v1alpha1.Severity(sev))
	}
	n.Routing.Routes = append(n.Routing.Routes, rt)
}

type chatRouting struct {
	receiver string
	routing  ChatRouting
}

// routingByChat reads what each chat receives from the description.
func (s *Service) routingByChat(sd *v1alpha1.SystemDescription) map[int64]chatRouting {
	out := map[int64]chatRouting{}
	if sd == nil || sd.Spec.Notifications == nil {
		return out
	}
	n := sd.Spec.Notifications
	r := n.Routing
	if r == nil {
		r = &v1alpha1.Routing{}
	}
	for i := range n.Receivers {
		rc := &n.Receivers[i]
		if !s.managed(rc) {
			continue
		}
		id := rc.Telegram.ChatID
		if prev, dup := out[id]; dup {
			prev.routing.Custom = true
			out[id] = prev
			continue
		}
		sevAll := map[string]bool{}
		if slices.Contains(r.Critical, rc.Name) {
			sevAll["critical"] = true
		}
		if slices.Contains(r.Warning, rc.Name) {
			sevAll["warning"] = true
		}
		var routes []v1alpha1.Route
		for _, rt := range r.Routes {
			if rt.Receiver == rc.Name {
				routes = append(routes, rt)
			}
		}
		cr := ChatRouting{Severities: []string{}, Services: []string{}}
		switch {
		case len(routes) == 0:
			cr.Severities = keys(sevAll)
		default:
			sevs := map[string]bool{}
			svcs := map[string]bool{}
			same := true
			first := strings.Join(uniqueSorted(routes[0].Services), ",")
			for _, rt := range routes {
				if strings.Join(uniqueSorted(rt.Services), ",") != first {
					same = false
				}
				if len(rt.Severities) == 0 {
					sevs["critical"], sevs["warning"] = true, true
				}
				for _, sv := range rt.Severities {
					sevs[string(sv)] = true
				}
				for _, x := range rt.Services {
					svcs[x] = true
				}
			}
			cr.Custom = !same || len(sevAll) > 0
			for k := range sevAll {
				sevs[k] = true
			}
			cr.Severities, cr.Services = keys(sevs), keys(svcs)
		}
		out[id] = chatRouting{receiver: rc.Name, routing: cr}
	}
	return out
}

// ---- Polling ----

// NeedLeaderElection makes controller-runtime run Start only on the leader,
// so two replicas never consume getUpdates of the same bot.
func (s *Service) NeedLeaderElection() bool { return true }

// Start runs the polling loop until ctx ends (manager.Runnable).
func (s *Service) Start(ctx context.Context) error {
	s.init()
	s.Logger.Info("telegram chat discovery started", "replica", s.Identity)
	backoff := 5 * time.Second
	var lastHeartbeat time.Time
	for ctx.Err() == nil {
		token, botID, err := s.token(ctx)
		if err != nil {
			if !errors.Is(err, ErrNoToken) {
				s.Logger.Warn("telegram: cannot read token", "error", err)
			}
			s.sleep(ctx, 30*time.Second)
			continue
		}
		st, err := s.State.Get(ctx)
		if err != nil {
			s.Logger.Warn("telegram: cannot read state", "error", err)
			s.sleep(ctx, backoff)
			continue
		}
		if st.Bot == nil || st.Bot.ID != botID {
			if _, err := s.recordBot(ctx, token); err != nil {
				s.recordPollError(ctx, err)
				s.sleep(ctx, time.Minute)
				continue
			}
		}
		s.mu.Lock()
		probe := s.probedForBot != botID
		s.probedForBot = botID
		s.mu.Unlock()
		if probe {
			s.probeDescriptionChats(ctx, token, botID)
		}

		ups, err := s.API.GetUpdates(ctx, token, st.Offset, s.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.recordPollError(ctx, err)
			wait := backoff
			switch {
			case IsConflict(err):
				wait = 30 * time.Second
			case IsUnauthorized(err):
				wait = time.Minute
			default:
				backoff = min(backoff*2, time.Minute)
			}
			s.sleep(ctx, wait)
			continue
		}
		backoff = 5 * time.Second
		if len(ups) == 0 && s.Now().Sub(lastHeartbeat) < time.Minute && st.Poll.LastError == "" {
			continue
		}
		lastHeartbeat = s.Now()
		if err := s.process(ctx, ups, botID); err != nil {
			s.Logger.Warn("telegram: cannot store updates", "error", err)
			s.sleep(ctx, backoff)
		}
	}
	return nil
}

func (s *Service) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-s.wake:
	}
}

func (s *Service) recordPollError(ctx context.Context, err error) {
	msg := describeErr(err)
	s.Logger.Warn("telegram polling failed", "error", msg)
	_, _ = s.State.Update(ctx, func(st *State) error {
		st.Poll.Poller = s.Identity
		st.Poll.LastError, st.Poll.ErrorAt, st.Poll.Conflict = msg, s.Now().UTC(), IsConflict(err)
		return nil
	})
}

// Migration is a group that became a supergroup with a new ID.
type Migration struct{ From, To int64 }

// process applies updates to the state, then migrations to the description.
func (s *Service) process(ctx context.Context, ups []Update, botID int64) error {
	var migs []Migration
	_, err := s.State.Update(ctx, func(st *State) error {
		migs = migs[:0]
		for _, u := range ups {
			migs = append(migs, ApplyUpdate(st, u, botID, s.Now().UTC())...)
			if u.UpdateID >= st.Offset {
				st.Offset = u.UpdateID + 1
			}
		}
		st.Poll = PollStatus{Poller: s.Identity, LastPollAt: s.Now().UTC()}
		return nil
	})
	if err != nil {
		return err
	}
	for _, m := range migs {
		if err := s.migrateDescription(ctx, m.From, m.To); err != nil {
			s.Logger.Warn("telegram: cannot update description after chat migration", "from", m.From, "to", m.To, "error", err)
		}
	}
	return nil
}

// ApplyUpdate changes the state for one update and returns the chat
// migrations it reports. Exported for tests.
func ApplyUpdate(st *State, u Update, botID int64, now time.Time) []Migration {
	if st.Chats == nil {
		st.Chats = map[int64]*KnownChat{}
	}
	var migs []Migration
	if m := u.MyChatMember; m != nil && (m.NewChatMember.User.ID == botID || botID == 0) {
		kc := upsertChat(st, m.Chat, now)
		kc.Status, kc.Available = m.NewChatMember.Status, m.NewChatMember.Present()
		kc.Problem = ""
		switch {
		case kc.Available:
		case m.NewChatMember.Status == "kicked" && m.Chat.Type == "private":
			kc.Problem = "the user blocked the bot"
		case m.NewChatMember.Status == "kicked":
			kc.Problem = "the bot was removed and banned from this chat"
		default:
			kc.Problem = "the bot was removed from this chat"
		}
	}
	for _, msg := range []*Message{u.Message, u.ChannelPost, u.EditedMessage, u.EditedChannelPost} {
		if msg == nil {
			continue
		}
		switch {
		case msg.MigrateToChatID != 0:
			migs = append(migs, Migration{From: msg.Chat.ID, To: msg.MigrateToChatID})
			applyMigration(st, msg.Chat.ID, msg.MigrateToChatID, msg.Chat, now)
			continue
		case msg.MigrateFromChatID != 0:
			migs = append(migs, Migration{From: msg.MigrateFromChatID, To: msg.Chat.ID})
			applyMigration(st, msg.MigrateFromChatID, msg.Chat.ID, msg.Chat, now)
			continue
		case msg.LeftChatMember != nil && msg.LeftChatMember.ID == botID:
			kc := upsertChat(st, msg.Chat, now)
			kc.Status, kc.Available, kc.Problem = "left", false, "the bot was removed from this chat"
			continue
		}
		if _, gone := migratedAway(st, msg.Chat.ID); gone {
			continue
		}
		// Any message the bot receives in a chat means it is a member.
		kc := upsertChat(st, msg.Chat, now)
		if msg.NewChatTitle != "" {
			kc.Title = msg.NewChatTitle
		}
		if !kc.Available && kc.Status != "kicked" && kc.Status != "left" {
			kc.Available, kc.Problem = true, ""
		}
		if kc.Status == "" {
			kc.Status = "member"
			kc.Available = true
		}
	}
	return migs
}

func migratedAway(st *State, id int64) (int64, bool) {
	for _, c := range st.Chats {
		if c.MigratedFrom == id {
			return c.ID, true
		}
	}
	return 0, false
}

func upsertChat(st *State, ch Chat, now time.Time) *KnownChat {
	kc := st.Chats[ch.ID]
	if kc == nil {
		kc = &KnownChat{ID: ch.ID, Source: "update"}
		st.Chats[ch.ID] = kc
	}
	if t := ch.DisplayTitle(); t != "" {
		kc.Title = t
	}
	if ch.Type != "" {
		kc.Type = ch.Type
	}
	kc.UpdatedAt = now
	return kc
}

func applyMigration(st *State, from, to int64, ch Chat, now time.Time) {
	old := st.Chats[from]
	nc := st.Chats[to]
	if nc == nil {
		nc = &KnownChat{ID: to, Source: "update", Status: "member"}
		if old != nil {
			cp := *old
			nc = &cp
			nc.ID = to
		}
		st.Chats[to] = nc
	}
	delete(st.Chats, from)
	nc.Type, nc.MigratedFrom, nc.Available, nc.Problem, nc.UpdatedAt = "supergroup", from, true, "", now
	if nc.Status == "" || nc.Status == "left" {
		nc.Status = "member"
	}
	if t := ch.DisplayTitle(); t != "" && nc.Title == "" {
		nc.Title = t
	}
}

// migrate records a migration found outside polling (getChat/sendMessage).
func (s *Service) migrate(ctx context.Context, from, to int64) error {
	if _, err := s.State.Update(ctx, func(st *State) error {
		applyMigration(st, from, to, Chat{}, s.Now().UTC())
		return nil
	}); err != nil {
		return err
	}
	return s.migrateDescription(ctx, from, to)
}

// migrateDescription points receivers of the old group at the supergroup.
func (s *Service) migrateDescription(ctx context.Context, from, to int64) error {
	err := s.updateAll(ctx, func(sd *v1alpha1.SystemDescription) (bool, error) {
		n := sd.Spec.Notifications
		if n == nil {
			return false, nil
		}
		changed := false
		for i := range n.Receivers {
			if s.managed(&n.Receivers[i]) && n.Receivers[i].Telegram.ChatID == from {
				n.Receivers[i].Telegram.ChatID = to
				changed = true
			}
		}
		return changed, nil
	})
	if err == nil {
		s.Logger.Info("telegram group migrated to a supergroup", "from", from, "to", to)
	}
	return err
}

// probeDescriptionChats checks chats referenced by the description that the
// bot has not seen in updates (for example a description written by hand).
func (s *Service) probeDescriptionChats(ctx context.Context, token string, botID int64) {
	list, err := s.Descriptions.List(ctx)
	if err != nil {
		return
	}
	st, err := s.State.Get(ctx)
	if err != nil {
		return
	}
	ids := map[int64]bool{}
	for _, snap := range list {
		if snap.SD == nil {
			continue
		}
		for id := range s.routingByChat(snap.SD) {
			ids[id] = true
		}
	}
	for id := range ids {
		if _, ok := st.Chats[id]; ok {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := s.checkChat(cctx, token, botID, id, "description")
		cancel()
		if err != nil {
			msg := describeErr(err)
			_, _ = s.State.Update(ctx, func(st *State) error {
				st.Chats[id] = &KnownChat{ID: id, Source: "description", Available: false, Problem: msg, UpdatedAt: s.Now().UTC()}
				return nil
			})
		}
	}
}

func (s *Service) token(ctx context.Context) (string, int64, error) {
	t, err := s.Tokens.Get(ctx)
	if err != nil {
		return "", 0, err
	}
	if t == "" {
		return "", 0, ErrNoToken
	}
	id, ok := BotIDFromToken(t)
	if !ok {
		return "", 0, ErrMalformedToken
	}
	return t, id, nil
}

// describeErr turns an error into a short Russian explanation for the UI.
func describeErr(err error) string {
	var ae *APIError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrMalformedToken):
		return "this does not look like a bot token (expected format 123456789:AA…)"
	case errors.Is(err, ErrNotBot):
		return "the token does not belong to a bot"
	case errors.As(err, &ae):
		switch ae.Code {
		case 401, 404:
			return "Telegram rejected the token (" + ae.Description + ")"
		case 409:
			return "another process already uses getUpdates or the bot has a webhook: a bot can have only one update consumer (" + ae.Description + ")"
		case 429:
			return fmt.Sprintf("Telegram rate limit reached, retry in %d s", ae.RetryAfter)
		case 403:
			return "the bot cannot post to this chat (" + ae.Description + ")"
		default:
			return fmt.Sprintf("Telegram error %d: %s", ae.Code, ae.Description)
		}
	case errors.Is(err, context.DeadlineExceeded):
		return "Telegram did not answer in time"
	default:
		return err.Error()
	}
}

// DescribeError is describeErr for the HTTP layer.
func DescribeError(err error) string { return describeErr(err) }

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func uniqueSorted(in []string) []string {
	m := map[string]bool{}
	for _, x := range in {
		if x = strings.TrimSpace(x); x != "" {
			m[x] = true
		}
	}
	return keys(m)
}

// ErrNotKamertonBot is returned for a receiver whose token Secret is not the
// one Kamerton manages: Kamerton does not read other Secrets.
var ErrNotKamertonBot = errors.New("the receiver uses a bot token Secret Kamerton does not manage")

// SendFollowUp sends an HTML message (parse mode HTML) to a chat of a
// receiver, with Kamerton's bot. ref is the receiver's botTokenSecret: only
// Kamerton's own token Secret is used, so a follow-up goes out only where
// the bot that sent the alert is Kamerton's.
func (s *Service) SendFollowUp(ctx context.Context, ref v1alpha1.SecretKeyRef, chatID int64, htmlText string) error {
	s.init()
	name, key := s.Tokens.Ref()
	if ref.Name != name || ref.Key != key {
		return ErrNotKamertonBot
	}
	token, _, err := s.token(ctx)
	if err != nil {
		return err
	}
	return s.API.SendMessage(ctx, token, chatID, htmlText)
}
