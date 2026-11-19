// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package descstore

import (
	"context"
	"sync"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
)

// Switch stands for the store of the project that is open, and lets another project be opened
// while Kamerton runs. With nothing open it holds nothing and saves nowhere.
type Switch struct {
	mu  sync.RWMutex
	cur Store
}

// Set opens a store; nil closes the one in use.
func (s *Switch) Set(st Store) {
	s.mu.Lock()
	s.cur = st
	s.mu.Unlock()
}

// Current is the store in use, or nil.
func (s *Switch) Current() Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

func (s *Switch) Mode() string {
	if c := s.Current(); c != nil {
		return c.Mode()
	}
	return ModeNone
}

func (s *Switch) List(ctx context.Context) ([]*Snapshot, error) {
	if c := s.Current(); c != nil {
		return c.List(ctx)
	}
	return nil, nil
}

func (s *Switch) Get(ctx context.Context, id string) (*Snapshot, error) {
	if c := s.Current(); c != nil {
		return c.Get(ctx, id)
	}
	return nil, ErrNoDescription
}

func (s *Switch) Create(ctx context.Context, raw []byte) (*Snapshot, description.Result, error) {
	if c := s.Current(); c != nil {
		return c.Create(ctx, raw)
	}
	return nil, description.Result{}, ErrNotWritable
}

func (s *Switch) Save(ctx context.Context, id string, raw []byte) (*Snapshot, description.Result, error) {
	if c := s.Current(); c != nil {
		return c.Save(ctx, id, raw)
	}
	return nil, description.Result{}, ErrNotWritable
}

func (s *Switch) Update(ctx context.Context, id string, mutate func(*v1alpha1.SystemDescription) error) (*Snapshot, error) {
	if c := s.Current(); c != nil {
		return c.Update(ctx, id, mutate)
	}
	return nil, ErrNotWritable
}

func (s *Switch) Delete(ctx context.Context, id string) error {
	if c := s.Current(); c != nil {
		return c.Delete(ctx, id)
	}
	return ErrNotWritable
}
