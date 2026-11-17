// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
)

// ---- Secret references in descriptions saved through the API ----
//
// Alertmanager sends the value of a referenced Secret to the receiver's
// endpoint (the SMTP password to the smarthost). A description saved through
// the API may therefore reference only Kamerton's own Secrets where Kamerton
// controls the endpoint, or keep a reference the stored version already had
// (written by a cluster admin with kubectl). Everything else is rejected.
// The Telegram and email services write their receivers through the store,
// not through these handlers, and are not checked here.

// secretPolicy is what a description saved through the API may reference.
type secretPolicy struct {
	// telegram is Kamerton's bot token Secret; nil when Telegram setup is
	// unavailable or the description lives outside Kamerton's namespace.
	telegram *v1alpha1.SecretKeyRef
	// email is Kamerton's SMTP password Secret with the configured server;
	// nil when no mail server is configured or the namespace differs.
	email                *v1alpha1.SecretKeyRef
	emailHost, emailUser string
	// otherNamespace is set when the description lives outside Kamerton's
	// namespace (cluster mode), where Kamerton's Secrets cannot be read.
	otherNamespace string
	// stored are the receivers of the stored version (nil on create).
	stored  []v1alpha1.Receiver
	created bool
}

// secretPolicyFor builds the policy for a description that lives (or will
// be created) in namespace; stored is the current version or nil.
func (s *Server) secretPolicyFor(ctx context.Context, stored *v1alpha1.SystemDescription, namespace string) *secretPolicy {
	p := &secretPolicy{created: stored == nil}
	if stored != nil && stored.Spec.Notifications != nil {
		p.stored = stored.Spec.Notifications.Receivers
	}
	if s.opts.Descriptions.Mode() == descstore.ModeCluster && s.opts.Namespace != "" && namespace != "" && namespace != s.opts.Namespace {
		p.otherNamespace = namespace
	}
	if tg := s.opts.Telegram; tg != nil && tg.Tokens != nil {
		name, key := tg.Tokens.Ref()
		p.telegram = &v1alpha1.SecretKeyRef{Name: name, Key: key}
	}
	if em := s.opts.Email; em != nil && em.Passwords != nil && em.State != nil {
		// A failed read leaves managed email unavailable: rejecting is safe.
		if st, err := em.State.Get(ctx); err == nil && st.Server != nil {
			name, key := em.Passwords.Ref()
			p.email = &v1alpha1.SecretKeyRef{Name: name, Key: key}
			p.emailHost, p.emailUser = st.Server.Smarthost, st.Server.Username
		}
	}
	return p
}

func sameRef(a, b *v1alpha1.SecretKeyRef) bool {
	return a != nil && b != nil && a.Name == b.Name && a.Key == b.Key
}

// storedTelegram reports whether the stored version has a Telegram
// receiver with the same token reference.
func (p *secretPolicy) storedTelegram(ref *v1alpha1.SecretKeyRef) bool {
	for _, r := range p.stored {
		if r.Telegram != nil && sameRef(&r.Telegram.BotTokenSecret, ref) {
			return true
		}
	}
	return false
}

// storedEmail reports whether the stored version has an email receiver
// with the same password reference, smarthost and username.
func (p *secretPolicy) storedEmail(e *v1alpha1.EmailReceiver) bool {
	for _, r := range p.stored {
		if r.Email != nil && sameRef(r.Email.AuthPasswordSecret, e.AuthPasswordSecret) &&
			r.Email.Smarthost == e.Smarthost && r.Email.AuthUsername == e.AuthUsername {
			return true
		}
	}
	return false
}

// keepClause is the "unchanged reference" option of the messages.
func (p *secretPolicy) keepClause(what string) string {
	if p.created {
		return ""
	}
	return " or keep " + what + " already in the saved description of this system"
}

const adminHint = " Only a cluster administrator can set another Secret (kubectl apply)."

func (p *secretPolicy) telegramIssue(i int, r *v1alpha1.Receiver) *description.Issue {
	ref := &r.Telegram.BotTokenSecret
	if p.storedTelegram(ref) {
		return nil
	}
	if p.telegram != nil && p.otherNamespace == "" && sameRef(ref, p.telegram) {
		return nil
	}
	var allowed string
	switch {
	case p.telegram != nil && p.otherNamespace != "":
		allowed = fmt.Sprintf("The Kamerton bot (Secret %s) is available only to descriptions in the Kamerton namespace, and this description is in %s.", p.telegram.Name, p.otherNamespace)
		if !p.created {
			allowed += " Here you can only keep the reference already in the saved description."
		}
	case p.telegram != nil:
		allowed = fmt.Sprintf("You can only use the Kamerton bot (Secret %s, key %s)%s.", p.telegram.Name, p.telegram.Key, p.keepClause("the reference and key"))
	case p.created:
		allowed = "Telegram is not set up from the UI here, so a new description cannot reference a Secret."
	default:
		allowed = "Telegram is not set up from the UI here: you can only keep the reference already in the saved description."
	}
	return &description.Issue{
		Path:     fmt.Sprintf("/spec/notifications/receivers/%d/telegram/botTokenSecret", i),
		Severity: description.SeverityError,
		Message:  fmt.Sprintf("receiver %q: the UI cannot set a reference to Secret %s (key %s) for the bot token. %s%s", r.Name, ref.Name, ref.Key, allowed, adminHint),
	}
}

func (p *secretPolicy) emailIssue(i int, r *v1alpha1.Receiver) *description.Issue {
	e := r.Email
	ref := e.AuthPasswordSecret
	if ref == nil || p.storedEmail(e) {
		return nil
	}
	managedRef := p.email != nil && sameRef(ref, p.email)
	if managedRef && p.otherNamespace == "" && e.Smarthost == p.emailHost && e.AuthUsername == p.emailUser {
		return nil
	}
	var msg string
	if managedRef && p.otherNamespace == "" {
		msg = fmt.Sprintf("receiver %q: the password from Secret %s is sent only to the Kamerton mail server (%s, user %q), and the description names server %s, user %q.",
			r.Name, ref.Name, p.emailHost, p.emailUser, e.Smarthost, e.AuthUsername)
	} else {
		msg = fmt.Sprintf("receiver %q: the UI cannot set an SMTP password from Secret %s (key %s) for server %s, user %q.", r.Name, ref.Name, ref.Key, e.Smarthost, e.AuthUsername)
	}
	var allowed string
	switch {
	case p.email != nil && p.otherNamespace != "":
		allowed = fmt.Sprintf(" The Kamerton mail server is available only to descriptions in the Kamerton namespace, and this description is in %s. You can send email without a password (no authPasswordSecret)%s.", p.otherNamespace, p.keepClause("the reference, server and user"))
	case p.email != nil:
		allowed = fmt.Sprintf(" You can send through the Kamerton mail server (Secret %s, key %s, server %s, user %q), without a password (no authPasswordSecret)%s.", p.email.Name, p.email.Key, p.emailHost, p.emailUser, p.keepClause("the reference, server and user"))
	default:
		allowed = fmt.Sprintf(" The Kamerton mail server is not set up: you can send email without a password (no authPasswordSecret)%s.", p.keepClause("the reference, server and user"))
	}
	return &description.Issue{
		Path:     fmt.Sprintf("/spec/notifications/receivers/%d/email/authPasswordSecret", i),
		Severity: description.SeverityError,
		Message:  msg + allowed + adminHint,
	}
}

// issues lists the receivers of doc whose Secret references are not allowed.
func (p *secretPolicy) issues(doc *v1alpha1.SystemDescription) []description.Issue {
	if doc.Spec.Notifications == nil {
		return nil
	}
	var out []description.Issue
	for i := range doc.Spec.Notifications.Receivers {
		r := &doc.Spec.Notifications.Receivers[i]
		if r.Telegram != nil {
			if is := p.telegramIssue(i, r); is != nil {
				out = append(out, *is)
			}
		}
		if r.Email != nil {
			if is := p.emailIssue(i, r); is != nil {
				out = append(out, *is)
			}
		}
	}
	return out
}

// storedDescription is the stored version of a system for the comparison,
// read leniently when it does not validate (a file edited by hand).
func storedDescription(snap *descstore.Snapshot) *v1alpha1.SystemDescription {
	if snap == nil {
		return nil
	}
	if snap.SD != nil {
		return snap.SD
	}
	var sd v1alpha1.SystemDescription
	if err := yaml.Unmarshal(snap.Raw, &sd); err != nil {
		return &v1alpha1.SystemDescription{}
	}
	return &sd
}

// secretIssues checks the Secret references of doc, to be saved over stored
// (nil: a new system).
func (s *Server) secretIssues(ctx context.Context, doc *v1alpha1.SystemDescription, stored *descstore.Snapshot) []description.Issue {
	prev := storedDescription(stored)
	ns := doc.Namespace
	switch {
	case prev != nil:
		// Saving keeps the namespace of the stored resource.
		ns = prev.Namespace
	case ns == "":
		ns = s.opts.Namespace
	}
	return s.secretPolicyFor(ctx, prev, ns).issues(doc)
}

// checkDraftSecrets adds the Secret reference problems of a draft to its
// validation result, so the editor shows them before saving. Without
// storage nothing can be saved and nothing is checked.
func (s *Server) checkDraftSecrets(ctx context.Context, res *description.Result, stored *descstore.Snapshot) {
	if res.Document == nil || s.opts.Descriptions.Mode() == descstore.ModeNone {
		return
	}
	if is := s.secretIssues(ctx, res.Document, stored); len(is) > 0 {
		res.Errors = append(res.Errors, is...)
		res.Valid = false
	}
}

// storedByName is the system a legacy endpoint would replace (the one with
// the document's name), or nil.
func (s *Server) storedByName(ctx context.Context, doc *v1alpha1.SystemDescription) *descstore.Snapshot {
	if doc == nil {
		return nil
	}
	snap, err := s.opts.Descriptions.Get(ctx, doc.Name)
	if err != nil {
		return nil
	}
	return snap
}

// allowSave checks a description about to be saved through the API over
// stored (nil: created). It writes a 400 and returns false when the
// document references Secrets it may not or is created outside Kamerton's
// namespace. Documents that do not validate are left to the store.
func (s *Server) allowSave(ctx context.Context, w http.ResponseWriter, body []byte, stored *descstore.Snapshot) bool {
	if s.opts.Descriptions.Mode() == descstore.ModeNone {
		// Nothing can be saved; the store says so.
		return true
	}
	res := description.Validate(body)
	if res.Document == nil {
		return true
	}
	if stored == nil {
		if p := s.createNamespaceProblem(res.Document); p != "" {
			writeError(w, http.StatusBadRequest, "namespace_not_allowed", p)
			return false
		}
	}
	issues := s.secretIssues(ctx, res.Document, stored)
	if len(issues) == 0 {
		return true
	}
	msgs := make([]string, len(issues))
	for i, is := range issues {
		msgs[i] = is.Message
	}
	s.log.Warn("description rejected: Secret reference not allowed", "system", res.Document.Name, "problems", len(issues))
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": strings.Join(msgs, "\n"), "code": "secret_not_allowed", "valid": false,
		"errors": issues, "warnings": []description.Issue{},
	})
	return false
}

// createNamespaceProblem explains why the API may not create doc: in a
// cluster new descriptions go only to Kamerton's namespace (or the one
// namespace Kamerton watches).
func (s *Server) createNamespaceProblem(doc *v1alpha1.SystemDescription) string {
	if s.opts.Descriptions.Mode() != descstore.ModeCluster || doc.Namespace == "" || s.opts.Namespace == "" || doc.Namespace == s.opts.Namespace {
		return ""
	}
	if c, ok := s.opts.Descriptions.(*descstore.Cluster); ok && c.WatchNamespace != "" && doc.Namespace == c.WatchNamespace {
		return ""
	}
	return fmt.Sprintf("the UI can create a description only in the Kamerton namespace (%s), and this one names %s. Remove metadata.namespace or ask a cluster administrator to create the description (kubectl apply).", s.opts.Namespace, doc.Namespace)
}
