// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package email implements email setup from the UI, mirroring the Telegram
// setup: the mail server (the SMTP password is stored only in a Kubernetes
// Secret, never in the description, an API response or a log), named
// recipients shared by all systems, and test messages. What each system
// sends to a recipient is a receiver in that system's description
// (notifications.receivers[].email), written by the system editor.
package email

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
)

// Errors returned to the API.
var (
	ErrNoServer         = errors.New("the mail server is not configured")
	ErrUnknownRecipient = errors.New("unknown recipient")
	ErrInvalidInput     = errors.New("invalid input")
	// ErrCheckFailed wraps a failed connection or login to the SMTP server.
	ErrCheckFailed = errors.New("smtp check failed")
)

// ReceiverPrefix starts the names of receivers written for recipients:
// email-<recipient ID>[-N].
const ReceiverPrefix = "email-"

// Service implements email setup. It is stateless apart from the stores, so
// every replica can serve it.
type Service struct {
	Passwords    PasswordStore
	State        StateStore
	Descriptions descstore.Store
	Sender       Sender
	// Namespace is Kamerton's namespace, where the password Secret lives.
	Namespace string
	Logger    *slog.Logger
	Now       func() time.Time

	once sync.Once
}

func (s *Service) init() {
	s.once.Do(func() {
		if s.Logger == nil {
			s.Logger = slog.Default()
		}
		if s.Sender == nil {
			s.Sender = &SMTPSender{}
		}
		if s.Now == nil {
			s.Now = time.Now
		}
	})
}

// ---- Views (never contain the password) ----

// Status is the state shown in the UI.
type Status struct {
	Server     ServerStatus    `json:"server"`
	Recipients []RecipientView `json:"recipients"`
}

// ServerStatus says how mail is sent, never the password.
type ServerStatus struct {
	Configured  bool   `json:"configured"`
	Smarthost   string `json:"smarthost,omitempty"`
	From        string `json:"from,omitempty"`
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"hasPassword"`
	Problem     string `json:"problem,omitempty"`
	SecretName  string `json:"secretName"`
	SecretKey   string `json:"secretKey"`
	Namespace   string `json:"namespace"`
}

// RecipientView is a recipient with the systems that write to it.
type RecipientView struct {
	Recipient
	// Receiver is the receiver name to use in a description.
	Receiver string `json:"receiver"`
	UsedIn   []Use  `json:"usedIn"`
}

// Use is what one system sends to a recipient.
type Use struct {
	System     string   `json:"system"`
	Severities []string `json:"severities"`
	Services   []string `json:"services"`
	Custom     bool     `json:"custom,omitempty"`
}

// Status assembles the UI state.
func (s *Service) Status(ctx context.Context) (*Status, error) {
	s.init()
	name, key := s.Passwords.Ref()
	out := &Status{Server: ServerStatus{SecretName: name, SecretKey: key, Namespace: s.Namespace}, Recipients: []RecipientView{}}
	st, err := s.State.Get(ctx)
	if err != nil {
		return nil, err
	}
	if srv := st.Server; srv != nil {
		out.Server.Configured = true
		out.Server.Smarthost, out.Server.From, out.Server.Username = srv.Smarthost, srv.From, srv.Username
		out.Server.HasPassword = srv.HasPassword
		if srv.HasPassword {
			pw, err := s.Passwords.Get(ctx)
			switch {
			case err != nil:
				out.Server.Problem = "cannot read the Secret: " + err.Error()
			case pw == "":
				out.Server.Problem = fmt.Sprintf("the password is missing from Secret %s/%s: enter it again", s.Namespace, name)
			}
		}
	}
	uses := s.usesByRecipient(ctx)
	for _, r := range st.SortedRecipients() {
		u := uses[r.ID]
		if u == nil {
			u = []Use{}
		}
		out.Recipients = append(out.Recipients, RecipientView{Recipient: *r, Receiver: ReceiverPrefix + r.ID, UsedIn: u})
	}
	return out, nil
}

// RecipientID returns the recipient ID of a receiver written by the UI.
func RecipientID(receiverName string) (string, bool) {
	rest, ok := strings.CutPrefix(receiverName, ReceiverPrefix)
	if !ok || rest == "" {
		return "", false
	}
	id, _, _ := strings.Cut(rest, "-")
	return id, true
}

// managed reports the recipient ID of an email receiver written for a known recipient.
func managed(r *v1alpha1.Receiver, st *State) (string, bool) {
	if r.Email == nil {
		return "", false
	}
	id, ok := RecipientID(r.Name)
	if !ok || st.Recipients[id] == nil {
		return "", false
	}
	return id, true
}

func (s *Service) usesByRecipient(ctx context.Context) map[string][]Use {
	out := map[string][]Use{}
	st, err := s.State.Get(ctx)
	if err != nil {
		return out
	}
	list, err := s.Descriptions.List(ctx)
	if err != nil {
		return out
	}
	for _, snap := range list {
		if snap.SD == nil || snap.SD.Spec.Notifications == nil {
			continue
		}
		n := snap.SD.Spec.Notifications
		seen := map[string]bool{}
		for i := range n.Receivers {
			id, ok := managed(&n.Receivers[i], st)
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			u := routingOf(n, n.Receivers[i].Name)
			if len(u.Severities) == 0 {
				continue
			}
			u.System = snap.ID
			out[id] = append(out[id], u)
		}
	}
	return out
}

// routingOf reads what a receiver gets (same model as the UI and the
// Telegram setup: routing.critical/warning for all services, routes for some).
func routingOf(n *v1alpha1.Notifications, name string) Use {
	u := Use{Severities: []string{}, Services: []string{}}
	r := n.Routing
	if r == nil {
		return u
	}
	sevs := map[string]bool{}
	all := false
	if slices.Contains(r.Critical, name) {
		sevs["critical"], all = true, true
	}
	if slices.Contains(r.Warning, name) {
		sevs["warning"], all = true, true
	}
	svcs := map[string]bool{}
	first := ""
	routes := 0
	for _, rt := range r.Routes {
		if rt.Receiver != name {
			continue
		}
		key := strings.Join(sortedCopy(rt.Services), ",")
		if routes == 0 {
			first = key
		} else if key != first {
			u.Custom = true
		}
		routes++
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
	if routes > 0 && all {
		u.Custom = true
	}
	for _, k := range []string{"critical", "warning"} {
		if sevs[k] {
			u.Severities = append(u.Severities, k)
		}
	}
	if !all {
		for k := range svcs {
			u.Services = append(u.Services, k)
		}
		sort.Strings(u.Services)
	}
	return u
}

// ---- Server ----

// ServerInput is the mail server form. An empty Password keeps the stored
// one when the login is unchanged.
type ServerInput struct {
	Smarthost string `json:"smarthost"`
	From      string `json:"from"`
	Username  string `json:"username"`
	Password  string `json:"password"`
}

// SetServer validates the settings, checks the connection and login, stores
// the password in the Secret and the rest in the state, and updates the
// receivers of every system that send through Kamerton's mail server.
func (s *Service) SetServer(ctx context.Context, in ServerInput) (*ServerStatus, error) {
	s.init()
	in.Smarthost = strings.TrimSpace(in.Smarthost)
	in.Username = strings.TrimSpace(in.Username)
	in.Password = strings.TrimSpace(in.Password)
	if err := ValidSmarthost(in.Smarthost); err != nil {
		return nil, err
	}
	from, err := NormalizeAddress(in.From)
	if err != nil {
		return nil, fmt.Errorf("%w (sender address)", err)
	}
	if strings.ContainsAny(in.Username, "\r\n") || len(in.Username) > 256 {
		return nil, fmt.Errorf("%w: the user name is too long or contains a line break", ErrInvalidInput)
	}
	if strings.ContainsAny(in.Password, "\r\n") || len(in.Password) > 1024 {
		return nil, fmt.Errorf("%w: the password is too long or contains a line break", ErrInvalidInput)
	}
	st, err := s.State.Get(ctx)
	if err != nil {
		return nil, err
	}
	password := in.Password
	if password == "" && in.Username != "" {
		// Keep the stored password when only the server or sender changes.
		if st.Server == nil || !st.Server.HasPassword || st.Server.Username != in.Username {
			return nil, fmt.Errorf("%w: enter the password", ErrInvalidInput)
		}
		if password, err = s.Passwords.Get(ctx); err != nil {
			return nil, err
		}
	}
	if in.Username == "" {
		password = ""
	}
	cfg := ServerSettings{Smarthost: in.Smarthost, From: from, Username: in.Username, HasPassword: password != "", UpdatedAt: s.Now().UTC()}
	if err := s.Sender.Check(ctx, cfg, password); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCheckFailed, describe(scrub(err, password)))
	}
	if password != "" {
		if err := s.Passwords.Set(ctx, password); err != nil {
			return nil, err
		}
	} else if err := s.Passwords.Delete(ctx); err != nil {
		return nil, err
	}
	st, err = s.State.Update(ctx, func(st *State) error {
		st.Server = &cfg
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.updateAll(ctx, func(sd *v1alpha1.SystemDescription) bool { return s.rewriteReceivers(sd, st) }); err != nil {
		return nil, fmt.Errorf("update email receivers in the descriptions: %w", err)
	}
	secret, _ := s.Passwords.Ref()
	s.Logger.Info("mail server stored", "smarthost", cfg.Smarthost, "secret", s.Namespace+"/"+secret)
	full, err := s.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &full.Server, nil
}

// DeleteServer removes the receivers of all recipients from every system,
// then the password, the server settings and the recipients.
func (s *Service) DeleteServer(ctx context.Context) error {
	s.init()
	st, err := s.State.Get(ctx)
	if err != nil {
		return err
	}
	if err := s.updateAll(ctx, func(sd *v1alpha1.SystemDescription) bool {
		return removeReceivers(sd, func(r *v1alpha1.Receiver) bool { _, ok := managed(r, st); return ok })
	}); err != nil {
		return fmt.Errorf("remove email receivers from the descriptions: %w", err)
	}
	if err := s.Passwords.Delete(ctx); err != nil {
		return err
	}
	if _, err := s.State.Update(ctx, func(st *State) error {
		*st = State{Recipients: map[string]*Recipient{}}
		return nil
	}); err != nil {
		return err
	}
	s.Logger.Info("mail server removed")
	return nil
}

// ---- Recipients ----

// RecipientInput is the recipient form.
type RecipientInput struct {
	Name string   `json:"name"`
	To   []string `json:"to"`
}

func validRecipient(in RecipientInput) (RecipientInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 64 || strings.ContainsAny(in.Name, "\r\n") {
		return in, fmt.Errorf("%w: the name must be 1 to 64 characters", ErrInvalidInput)
	}
	if len(in.To) == 0 || len(in.To) > 20 {
		return in, fmt.Errorf("%w: enter 1 to 20 addresses", ErrInvalidInput)
	}
	seen := map[string]bool{}
	var to []string
	for _, a := range in.To {
		addr, err := NormalizeAddress(a)
		if err != nil {
			return in, err
		}
		if !seen[strings.ToLower(addr)] {
			seen[strings.ToLower(addr)] = true
			to = append(to, addr)
		}
	}
	in.To = to
	return in, nil
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddRecipient remembers a new recipient.
func (s *Service) AddRecipient(ctx context.Context, in RecipientInput) (*Recipient, error) {
	s.init()
	in, err := validRecipient(in)
	if err != nil {
		return nil, err
	}
	var out *Recipient
	_, err = s.State.Update(ctx, func(st *State) error {
		if st.Server == nil {
			return ErrNoServer
		}
		id := newID()
		for st.Recipients[id] != nil {
			id = newID()
		}
		r := &Recipient{ID: id, Name: in.Name, To: in.To, CreatedAt: s.Now().UTC()}
		st.Recipients[id] = r
		cp := *r
		out = &cp
		return nil
	})
	return out, err
}

// UpdateRecipient changes the name and addresses of a recipient and the
// addresses in every system that writes to it.
func (s *Service) UpdateRecipient(ctx context.Context, id string, in RecipientInput) (*Recipient, error) {
	s.init()
	in, err := validRecipient(in)
	if err != nil {
		return nil, err
	}
	var out *Recipient
	st, err := s.State.Update(ctx, func(st *State) error {
		r := st.Recipients[id]
		if r == nil {
			return ErrUnknownRecipient
		}
		r.Name, r.To = in.Name, in.To
		cp := *r
		out = &cp
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.updateAll(ctx, func(sd *v1alpha1.SystemDescription) bool { return s.rewriteReceivers(sd, st) }); err != nil {
		return nil, fmt.Errorf("update the recipient in the descriptions: %w", err)
	}
	return out, nil
}

// DeleteRecipient removes the recipient's receivers from every system and
// forgets it.
func (s *Service) DeleteRecipient(ctx context.Context, id string) error {
	s.init()
	st, err := s.State.Get(ctx)
	if err != nil {
		return err
	}
	if st.Recipients[id] == nil {
		return ErrUnknownRecipient
	}
	if err := s.updateAll(ctx, func(sd *v1alpha1.SystemDescription) bool {
		return removeReceivers(sd, func(r *v1alpha1.Receiver) bool { rid, ok := managed(r, st); return ok && rid == id })
	}); err != nil {
		return fmt.Errorf("remove the recipient from the descriptions: %w", err)
	}
	_, err = s.State.Update(ctx, func(st *State) error {
		if st.Recipients[id] == nil {
			return ErrNoChange
		}
		delete(st.Recipients, id)
		return nil
	})
	return err
}

// TestResult is the outcome of a test email.
type TestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// testWords is the text of a test email in one language.
type testWords struct {
	subject, anyAddress, systems string
}

// testTexts are the test emails by notification language (see
// spec.notifications.language).
var testTexts = map[string]testWords{
	languageEnglish: {
		subject:    "Kamerton: test email",
		anyAddress: "Alerts can be delivered to this address.",
		systems:    "This address receives alerts of: %s.",
	},
	languageRussian: {
		subject:    "Kamerton: тестовое письмо",
		anyAddress: "Оповещения могут приходить на этот адрес.",
		systems:    "Сюда приходят оповещения систем: %s.",
	},
}

const (
	languageEnglish = "en"
	languageRussian = "ru"
)

// language is the language of a test email for the given systems: the
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

// SendTest sends a test email to a recipient.
func (s *Service) SendTest(ctx context.Context, id string) (*TestResult, error) {
	s.init()
	st, err := s.State.Get(ctx)
	if err != nil {
		return nil, err
	}
	if st.Server == nil {
		return nil, ErrNoServer
	}
	r := st.Recipients[id]
	if r == nil {
		return nil, ErrUnknownRecipient
	}
	password := ""
	if st.Server.HasPassword {
		if password, err = s.Passwords.Get(ctx); err != nil {
			return nil, err
		}
	}
	var systems []string
	for _, u := range s.usesByRecipient(ctx)[id] {
		systems = append(systems, u.System)
	}
	w := testTexts[s.language(ctx, systems)]
	body := w.subject + ".\n" + w.anyAddress
	if len(systems) > 0 {
		body = w.subject + ".\n" + fmt.Sprintf(w.systems, strings.Join(systems, ", "))
	}
	if err := s.Sender.Send(ctx, *st.Server, password, r.To, w.subject, body); err != nil {
		return &TestResult{Error: describe(scrub(err, password))}, nil
	}
	return &TestResult{OK: true}, nil
}

// ---- Description editing ----

// rewriteReceivers brings the email settings of managed receivers in line
// with the server and recipient state. It reports whether anything changed.
func (s *Service) rewriteReceivers(sd *v1alpha1.SystemDescription, st *State) bool {
	n := sd.Spec.Notifications
	if n == nil || st.Server == nil {
		return false
	}
	changed := false
	for i := range n.Receivers {
		r := &n.Receivers[i]
		id, ok := managed(r, st)
		if !ok {
			continue
		}
		want := s.ReceiverFor(st.Server, st.Recipients[id])
		if !sameEmail(r.Email, want) {
			r.Email = want
			changed = true
		}
	}
	return changed
}

// ReceiverFor is the email receiver of a recipient.
func (s *Service) ReceiverFor(srv *ServerSettings, r *Recipient) *v1alpha1.EmailReceiver {
	e := &v1alpha1.EmailReceiver{To: append([]string(nil), r.To...), From: srv.From, Smarthost: srv.Smarthost, AuthUsername: srv.Username}
	if srv.HasPassword {
		name, key := s.Passwords.Ref()
		e.AuthPasswordSecret = &v1alpha1.SecretKeyRef{Name: name, Key: key}
	}
	return e
}

func sameEmail(a, b *v1alpha1.EmailReceiver) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.From != b.From || a.Smarthost != b.Smarthost || a.AuthUsername != b.AuthUsername || strings.Join(a.To, ",") != strings.Join(b.To, ",") {
		return false
	}
	if (a.AuthPasswordSecret == nil) != (b.AuthPasswordSecret == nil) {
		return false
	}
	return a.AuthPasswordSecret == nil || *a.AuthPasswordSecret == *b.AuthPasswordSecret
}

func (s *Service) updateAll(ctx context.Context, mutate func(*v1alpha1.SystemDescription) bool) error {
	list, err := s.Descriptions.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, snap := range list {
		if snap.SD == nil {
			continue
		}
		if !mutate(snap.SD.DeepCopy()) {
			continue
		}
		if _, err := s.Descriptions.Update(ctx, snap.ID, func(sd *v1alpha1.SystemDescription) error {
			mutate(sd)
			return nil
		}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", snap.ID, err))
		}
	}
	return errors.Join(errs...)
}

// removeReceivers deletes the receivers matching match and every reference to them.
func removeReceivers(sd *v1alpha1.SystemDescription, match func(*v1alpha1.Receiver) bool) bool {
	n := sd.Spec.Notifications
	if n == nil {
		return false
	}
	removed := map[string]bool{}
	kept := n.Receivers[:0:0]
	for i := range n.Receivers {
		if match(&n.Receivers[i]) {
			removed[n.Receivers[i].Name] = true
			continue
		}
		kept = append(kept, n.Receivers[i])
	}
	if len(removed) == 0 {
		return false
	}
	n.Receivers = kept
	if r := n.Routing; r != nil {
		filter := func(list []string) []string {
			var out []string
			for _, x := range list {
				if !removed[x] {
					out = append(out, x)
				}
			}
			return out
		}
		r.Critical, r.Warning = filter(r.Critical), filter(r.Warning)
		var routes []v1alpha1.Route
		for _, rt := range r.Routes {
			if !removed[rt.Receiver] {
				routes = append(routes, rt)
			}
		}
		r.Routes = routes
		if len(r.Critical) == 0 && len(r.Warning) == 0 && len(r.Routes) == 0 {
			n.Routing = nil
		}
	}
	if len(n.Receivers) == 0 {
		n.Receivers = nil
	}
	return true
}

// describe turns an SMTP or network error into one line in plain words.
func describe(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "535") || strings.Contains(low, "authentication") || strings.Contains(low, "auth"):
		return "the server rejected the user name or password (" + msg + "). Many mail services require a separate app password."
	case strings.Contains(low, "no such host"):
		return "server not found: check the address (" + msg + ")"
	case strings.Contains(low, "connection refused"), strings.Contains(low, "i/o timeout"), strings.Contains(low, "deadline exceeded"):
		return "cannot connect to the server: check the address and port (" + msg + ")"
	case strings.Contains(low, "certificate"), strings.Contains(low, "tls"):
		return "TLS error (" + msg + ")"
	case strings.Contains(low, "unencrypted"):
		return "the server does not offer encryption, and the password is never sent without it: try port 465 or 587"
	}
	return msg
}

// DescribeError is describe for the API.
func DescribeError(err error) string { return describe(err) }

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// ErrNotKamertonServer is returned for a receiver that does not send through
// the mail server configured in Kamerton.
var ErrNotKamertonServer = errors.New("the receiver does not use the mail server configured in Kamerton")

// SendFollowUp sends a message to the addresses of an email receiver,
// through the mail server configured in Kamerton only: the receiver must
// name that server and login, and its password Secret (if any) must be
// Kamerton's. The HTML body is used when the sender can send HTML.
func (s *Service) SendFollowUp(ctx context.Context, rcv *v1alpha1.EmailReceiver, subject, text, htmlBody string) error {
	s.init()
	st, err := s.State.Get(ctx)
	if err != nil {
		return err
	}
	srv := st.Server
	if srv == nil {
		return ErrNoServer
	}
	if rcv == nil || rcv.Smarthost != srv.Smarthost || rcv.AuthUsername != srv.Username {
		return ErrNotKamertonServer
	}
	password := ""
	if rcv.AuthPasswordSecret != nil {
		name, key := s.Passwords.Ref()
		if rcv.AuthPasswordSecret.Name != name || rcv.AuthPasswordSecret.Key != key {
			return ErrNotKamertonServer
		}
		if password, err = s.Passwords.Get(ctx); err != nil {
			return err
		}
	} else if srv.Username != "" {
		return ErrNotKamertonServer
	}
	cfg := *srv
	if rcv.From != "" {
		cfg.From = rcv.From
	}
	if hs, ok := s.Sender.(HTMLSender); ok && htmlBody != "" {
		return scrub(hs.SendHTML(ctx, cfg, password, rcv.To, subject, text, htmlBody), password)
	}
	return scrub(s.Sender.Send(ctx, cfg, password, rcv.To, subject, text), password)
}
