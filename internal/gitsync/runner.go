// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kamerton-labs/kamerton/internal/descstore"
)

// Settings is the connection to a repository as a person sets it in the UI. The token is not
// part of it: it lives in a Secret.
type Settings struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path,omitempty"`
	// Write: empty, pullRequest or commit.
	Write        string `json:"write,omitempty"`
	Provider     string `json:"provider,omitempty"`
	APIURL       string `json:"apiURL,omitempty"`
	CommitStatus bool   `json:"commitStatus,omitempty"`
}

// TokenStore keeps the access token; a Secret in the cluster.
type TokenStore interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, token string) error
	Delete(ctx context.Context) error
}

// SettingsStore keeps Settings; nil Settings means no repository.
type SettingsStore interface {
	Load(ctx context.Context) (*Settings, error)
	Save(ctx context.Context, s *Settings) error
}

// ConfigMapSettings keeps the settings in a ConfigMap.
type ConfigMapSettings struct {
	Client          client.Client
	Namespace, Name string
}

const settingsKey = "settings.json"

func (c *ConfigMapSettings) Load(ctx context.Context) (*Settings, error) {
	var cm corev1.ConfigMap
	err := c.Client.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw := cm.Data[settingsKey]
	if raw == "" {
		return nil, nil
	}
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("git settings in ConfigMap %s are unreadable: %w", c.Name, err)
	}
	if s.URL == "" {
		return nil, nil
	}
	return &s, nil
}

func (c *ConfigMapSettings) Save(ctx context.Context, s *Settings) error {
	raw := ""
	if s != nil {
		b, err := json.Marshal(s)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	var cm corev1.ConfigMap
	err := c.Client.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, &cm)
	if apierrors.IsNotFound(err) {
		if s == nil {
			return nil
		}
		cm.Namespace, cm.Name = c.Namespace, c.Name
		cm.Data = map[string]string{settingsKey: raw}
		return c.Client.Create(ctx, &cm)
	}
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[settingsKey] = raw
	return c.Client.Update(ctx, &cm)
}

// Runner holds the repository in use and lets it be changed while Kamerton runs: connected,
// edited or disconnected from the UI, without a restart.
type Runner struct {
	// Base carries what does not change with the settings: Target, Generate, Pending, Logger.
	Base Service
	// Locked is set when the repository comes from the installation's settings; the UI then
	// shows it and cannot change it.
	Locked   *Settings
	Store    SettingsStore
	Tokens   TokenStore
	Interval time.Duration
	Logger   *slog.Logger
	// Release makes the systems of a repository ordinary again when it is disconnected.
	Release func(ctx context.Context, repoKey string) error

	mu  sync.Mutex
	cur *Service
	// loaded: the stored settings were read once.
	loaded bool
}

// NeedLeaderElection: one replica writes.
func (r *Runner) NeedLeaderElection() bool { return true }

// Start follows the repository in use until ctx ends.
func (r *Runner) Start(ctx context.Context) error {
	every := r.Interval
	if every <= 0 {
		every = DefaultInterval
	}
	if s := r.Current(ctx); s != nil {
		s.Sync(ctx)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if s := r.Current(ctx); s != nil {
				s.Sync(ctx)
			}
		}
	}
}

// Current returns the repository in use, or nil.
func (r *Runner) Current(ctx context.Context) *Service {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.loaded {
		set := r.Locked
		if set == nil && r.Store != nil {
			loaded, err := r.Store.Load(ctx)
			if err != nil {
				r.log().Warn("git: settings unavailable", "error", err)
				return nil
			}
			set = loaded
		}
		r.loaded = true
		if set != nil {
			r.cur = r.build(*set)
			r.log().Info("system descriptions are read from git", "repo", r.cur.Repo(), "branch", r.cur.branch(), "path", set.Path, "write", r.cur.WriteMode())
		}
	}
	return r.cur
}

func (r *Runner) build(set Settings) *Service {
	b := &r.Base
	s := &Service{
		Target: b.Target, Generate: b.Generate, GeneratedPath: b.GeneratedPath, Pending: b.Pending, Logger: b.Logger, Now: b.Now, Token: b.Token,
		URL: strings.TrimSpace(set.URL), Branch: strings.TrimSpace(set.Branch), Path: set.Path,
		Write: ParseWrite(set.Write), Provider: set.Provider, APIURL: set.APIURL, CommitStatus: set.CommitStatus,
		Interval: r.Interval,
	}
	if r.Tokens != nil {
		s.Token = r.Tokens.Get
	}
	return s
}

// View is what the UI shows of the connection.
type View struct {
	// Available is false where a repository cannot be connected from the UI at all.
	Available bool `json:"available"`
	// Locked: set by the installation, shown but not editable.
	Locked    bool `json:"locked"`
	Connected bool `json:"connected"`
	HasToken  bool `json:"hasToken"`
	Settings
}

func (r *Runner) View(ctx context.Context) View {
	v := View{Available: r.Store != nil || r.Locked != nil, Locked: r.Locked != nil}
	if s := r.Current(ctx); s != nil {
		v.Connected = true
		v.Settings = Settings{URL: s.Repo(), Branch: s.branch(), Path: cleanPath(s.Path), Write: s.WriteMode(), Provider: s.Provider, APIURL: s.APIURL, CommitStatus: s.CommitStatus}
	}
	if r.Tokens != nil {
		tok, err := r.Tokens.Get(ctx)
		v.HasToken = err == nil && strings.TrimSpace(tok) != ""
	}
	return v
}

// Check is the outcome of trying a repository before connecting it.
type Check struct {
	OK bool `json:"ok"`
	// Empty: the repository has no commits; the first system starts it.
	Empty bool   `json:"empty,omitempty"`
	Error string `json:"error,omitempty"`
	// Files is how many description files were found.
	Files int `json:"files"`
}

// Try reads the repository with the given settings and token without changing anything. An
// empty token means the stored one.
func (r *Runner) Try(ctx context.Context, set Settings, token string) Check {
	if err := validSettings(&set); err != nil {
		return Check{Error: err.Error()}
	}
	s := r.build(set)
	s.Target = nil
	if token = strings.TrimSpace(token); token != "" {
		s.Token = func(context.Context) (string, error) { return token, nil }
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	auth, err := s.auth(ctx)
	if err != nil {
		return Check{Error: err.Error()}
	}
	if _, err := s.remoteHead(ctx, auth); errors.Is(err, errEmptyRepo) {
		return Check{OK: true, Empty: true}
	} else if err != nil {
		return Check{Error: err.Error()}
	}
	files, _, err := s.read(ctx, auth)
	if err != nil && !errors.Is(err, errNoFolder) {
		return Check{Error: err.Error()}
	}
	return Check{OK: true, Files: len(files)}
}

// Connect stores the settings and the token and switches to the repository at once.
func (r *Runner) Connect(ctx context.Context, set Settings, token string) error {
	if r.Locked != nil {
		return errors.New("the repository is set by the installation (git.url); change it there")
	}
	if r.Store == nil {
		return errors.New("a repository can be connected only in a cluster")
	}
	if err := validSettings(&set); err != nil {
		return err
	}
	if token = strings.TrimSpace(token); token != "" {
		if r.Tokens == nil {
			return errors.New("there is nowhere to keep the token")
		}
		if err := r.Tokens.Set(ctx, token); err != nil {
			return fmt.Errorf("store the token: %w", err)
		}
	}
	if err := r.Store.Save(ctx, &set); err != nil {
		return fmt.Errorf("store the settings: %w", err)
	}
	next := r.build(set)
	r.mu.Lock()
	old := r.cur
	r.cur, r.loaded = next, true
	r.mu.Unlock()
	if old != nil && old.repoKey() != next.repoKey() && r.Release != nil {
		// Another repository: the systems of the old one become ordinary systems.
		if err := r.Release(ctx, old.repoKey()); err != nil {
			r.log().Warn("git: systems of the previous repository were not released", "error", err)
		}
	}
	r.log().Info("git: repository connected", "repo", next.Repo(), "branch", next.branch(), "write", next.WriteMode())
	go next.Sync(context.WithoutCancel(ctx))
	return nil
}

// Disconnect stops following the repository. Its systems stay in the cluster as ordinary,
// editable systems; nothing is deleted. The token is removed.
func (r *Runner) Disconnect(ctx context.Context) error {
	if r.Locked != nil {
		return errors.New("the repository is set by the installation (git.url); change it there")
	}
	if r.Store == nil {
		return nil
	}
	if err := r.Store.Save(ctx, nil); err != nil {
		return err
	}
	r.mu.Lock()
	old := r.cur
	r.cur, r.loaded = nil, true
	r.mu.Unlock()
	if old != nil && r.Release != nil {
		if err := r.Release(ctx, old.repoKey()); err != nil {
			return fmt.Errorf("release the systems: %w", err)
		}
	}
	if r.Tokens != nil {
		if err := r.Tokens.Delete(ctx); err != nil {
			r.log().Warn("git: the token was not removed", "error", err)
		}
	}
	r.log().Info("git: repository disconnected")
	return nil
}

func validSettings(set *Settings) error {
	set.URL = strings.TrimSpace(set.URL)
	if set.URL == "" {
		return errors.New("the address of the repository is required")
	}
	u, err := url.Parse(set.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "git") {
		return errors.New("the address must look like https://host/team/repository.git")
	}
	if u.User != nil {
		return errors.New("put the token into its own field, not into the address")
	}
	if set.Write != "" && ParseWrite(set.Write) == WriteOff {
		return errors.New("push mode must be empty, pullRequest or commit")
	}
	return nil
}

// The methods below let a Runner stand where a repository is expected, delegating to the one
// in use: descstore.GitWriter.

func (r *Runner) WriteEnabled() bool {
	s := r.Current(context.Background())
	return s != nil && s.WriteEnabled()
}

func (r *Runner) RepoKey() string {
	if s := r.Current(context.Background()); s != nil {
		return s.RepoKey()
	}
	return ""
}

func (r *Runner) Propose(ctx context.Context, c descstore.GitChange) (*descstore.GitProposal, error) {
	if s := r.Current(ctx); s != nil {
		return s.Propose(ctx, c)
	}
	return nil, descstore.ErrFromGit
}

func (r *Runner) Stage(ctx context.Context, c descstore.GitChange) error {
	if s := r.Current(ctx); s != nil {
		return s.Stage(ctx, c)
	}
	return descstore.ErrFromGit
}

func (r *Runner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
