// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kamerton-labs/kamerton/internal/descstore"
)

// Kinds of a saved change.
const (
	KindChange = "change"
	KindNew    = "new"
	KindDelete = "delete"
	// KindMove: a system made by hand whose description goes to the repository as it is.
	KindMove = "move"
)

// maxPending keeps the saved changes well under the 1 MiB of a ConfigMap.
const maxPending = 600 << 10

// Change is a change saved in the UI and not pushed yet. Saving does not touch the repository
// or the cluster: changes wait here until someone pushes them, together, with one message.
type Change struct {
	System string `json:"system"`
	Kind   string `json:"kind"`
	// Path of the file; empty for a new system until it is pushed.
	Path    string    `json:"path,omitempty"`
	Content []byte    `json:"content,omitempty"`
	SavedAt time.Time `json:"savedAt"`
	Author  string    `json:"author,omitempty"`
}

// PendingStore keeps the saved changes by system; Update re-reads and retries on conflicts.
type PendingStore interface {
	Update(ctx context.Context, mutate func(map[string]Change) error) (map[string]Change, error)
}

// MemoryPending keeps saved changes in memory: they are lost with the process.
type MemoryPending struct {
	mu sync.Mutex
	m  map[string]Change
}

func (p *MemoryPending) Update(_ context.Context, mutate func(map[string]Change) error) (map[string]Change, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := clonePending(p.m)
	if mutate != nil {
		if err := mutate(cp); err != nil {
			return nil, err
		}
		p.m = clonePending(cp)
	}
	return cp, nil
}

// ConfigMapPending keeps saved changes in a ConfigMap, so they survive a restart.
type ConfigMapPending struct {
	Client          client.Client
	Namespace, Name string
}

const pendingKey = "pending.json"

func (p *ConfigMapPending) Update(ctx context.Context, mutate func(map[string]Change) error) (map[string]Change, error) {
	var out map[string]Change
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cm corev1.ConfigMap
		err := p.Client.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Name}, &cm)
		missing := apierrors.IsNotFound(err)
		if err != nil && !missing {
			return err
		}
		m := map[string]Change{}
		if raw := cm.Data[pendingKey]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				return fmt.Errorf("saved changes in ConfigMap %s are unreadable: %w", p.Name, err)
			}
		}
		out = m
		if mutate == nil {
			return nil
		}
		before, _ := json.Marshal(m)
		if err := mutate(m); err != nil {
			return err
		}
		after, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if bytes.Equal(before, after) {
			return nil
		}
		if len(after) > maxPending {
			return errors.New("too many unpushed changes; push or discard some first")
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[pendingKey] = string(after)
		if missing {
			cm.Namespace, cm.Name = p.Namespace, p.Name
			return p.Client.Create(ctx, &cm)
		}
		return p.Client.Update(ctx, &cm)
	})
	return out, err
}

func clonePending(m map[string]Change) map[string]Change {
	cp := make(map[string]Change, len(m))
	for k, v := range m {
		v.Content = append([]byte(nil), v.Content...)
		cp[k] = v
	}
	return cp
}

func (s *Service) pending() PendingStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Pending == nil {
		s.Pending = &MemoryPending{}
	}
	return s.Pending
}

// Stage implements descstore.GitWriter: the change is saved, not pushed.
func (s *Service) Stage(ctx context.Context, c descstore.GitChange) error {
	if !s.WriteEnabled() {
		return descstore.ErrFromGit
	}
	kind := KindChange
	switch {
	case c.Move:
		kind = KindMove
	case c.New:
		kind = KindNew
	case c.Content == nil:
		kind = KindDelete
	}
	_, err := s.pending().Update(ctx, func(m map[string]Change) error {
		old, had := m[c.System]
		switch {
		case kind == KindDelete && had && (old.Kind == KindNew || old.Kind == KindMove):
			// A system that never reached the repository: deleting it is forgetting it.
			delete(m, c.System)
			return nil
		case kind == KindChange && had && (old.Kind == KindNew || old.Kind == KindMove):
			// Still new: only its content changes.
			kind = old.Kind
		case kind == KindChange:
			if raw, ok := s.Raw(c.Path); ok && bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(c.Content)) {
				// Back to what the repository has: nothing to push.
				delete(m, c.System)
				return nil
			}
		}
		m[c.System] = Change{System: c.System, Kind: kind, Path: c.Path, Content: c.Content, SavedAt: s.now().UTC(), Author: c.Author}
		return nil
	})
	return err
}

// Changes returns the saved changes, oldest first.
func (s *Service) Changes(ctx context.Context) ([]Change, error) {
	m, err := s.pending().Update(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Change, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].SavedAt.Equal(out[j].SavedAt) {
			return out[i].SavedAt.Before(out[j].SavedAt)
		}
		return out[i].System < out[j].System
	})
	return out, nil
}

// Saved returns the saved change of a system, if there is one.
func (s *Service) Saved(ctx context.Context, system string) (Change, bool) {
	m, err := s.pending().Update(ctx, nil)
	if err != nil {
		return Change{}, false
	}
	c, ok := m[system]
	return c, ok
}

// Discard forgets the saved change of a system.
func (s *Service) Discard(ctx context.Context, system string) error {
	_, err := s.pending().Update(ctx, func(m map[string]Change) error {
		delete(m, system)
		return nil
	})
	return err
}

// Push writes saved changes to the repository in one commit and forgets them. systems empty:
// all of them.
func (s *Service) Push(ctx context.Context, systems []string, message, author string) (*descstore.GitProposal, error) {
	all, err := s.Changes(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range systems {
		want[n] = true
	}
	var batch []descstore.GitChange
	for _, c := range all {
		if len(want) > 0 && !want[c.System] {
			continue
		}
		batch = append(batch, descstore.GitChange{
			System: c.System, Path: c.Path, Content: c.Content,
			New: c.Kind == KindNew || c.Kind == KindMove, Move: c.Kind == KindMove,
		})
	}
	if len(batch) == 0 {
		return nil, errors.New("nothing to push")
	}
	p, err := s.proposeAll(ctx, batch, message, author)
	if err != nil {
		return nil, err
	}
	_, err = s.pending().Update(ctx, func(m map[string]Change) error {
		for _, c := range batch {
			delete(m, c.System)
		}
		return nil
	})
	if err != nil {
		s.log().Warn("git: pushed, but the saved changes were not cleared", "error", err)
	}
	return p, nil
}
