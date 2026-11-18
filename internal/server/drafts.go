// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/gitsync"
)

// draftSource marks a system that exists only as a saved change: made in the UI, not pushed yet.
const draftSource = "git:not pushed"

// draftStore shows the systems created in the UI and not pushed yet next to the real ones, so a
// new system can be opened, drawn and corrected before it goes to the repository. A draft runs
// nowhere: it has no rules in the cluster until its file is on the followed branch.
type draftStore struct {
	descstore.Store
	git func() *gitsync.Service
}

func isDraft(s *descstore.Snapshot) bool { return s != nil && s.Source == draftSource }

func (d *draftStore) drafts(ctx context.Context) []*descstore.Snapshot {
	g := d.git()
	if g == nil {
		return nil
	}
	list, err := g.Changes(ctx)
	if err != nil {
		return nil
	}
	var out []*descstore.Snapshot
	for _, c := range list {
		if c.Kind != gitsync.KindNew {
			continue
		}
		snap := &descstore.Snapshot{ID: c.System, Raw: c.Content, Source: draftSource}
		res := description.Validate(c.Content)
		if res.Valid && res.Document != nil {
			snap.SD = res.Document
		} else {
			snap.Invalid = &res
		}
		out = append(out, snap)
	}
	return out
}

func (d *draftStore) List(ctx context.Context) ([]*descstore.Snapshot, error) {
	list, err := d.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, s := range list {
		have[s.Name()] = true
	}
	for _, s := range d.drafts(ctx) {
		if !have[s.ID] {
			list = append(list, s)
		}
	}
	return list, nil
}

func (d *draftStore) draft(ctx context.Context, id string) *descstore.Snapshot {
	for _, s := range d.drafts(ctx) {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (d *draftStore) Get(ctx context.Context, id string) (*descstore.Snapshot, error) {
	snap, err := d.Store.Get(ctx, id)
	if errors.Is(err, descstore.ErrNoDescription) {
		if s := d.draft(ctx, id); s != nil {
			return s, nil
		}
	}
	return snap, err
}

// Save of a draft replaces its saved content; it stays a change to push.
func (d *draftStore) Save(ctx context.Context, id string, raw []byte) (*descstore.Snapshot, description.Result, error) {
	if _, err := d.Store.Get(ctx, id); !errors.Is(err, descstore.ErrNoDescription) || d.draft(ctx, id) == nil {
		return d.Store.Save(ctx, id, raw)
	}
	res := description.Validate(raw)
	if !res.Valid || res.Document == nil {
		return nil, res, &descstore.InvalidError{Result: res}
	}
	if res.Document.Name != id {
		return nil, res, descstore.ErrRename
	}
	if err := d.git().Stage(ctx, descstore.GitChange{System: id, Content: raw, New: true}); err != nil {
		return nil, res, err
	}
	return nil, res, descstore.ErrStaged
}

// Delete of a draft forgets it: nothing of it ever reached the repository.
func (d *draftStore) Delete(ctx context.Context, id string) error {
	if _, err := d.Store.Get(ctx, id); errors.Is(err, descstore.ErrNoDescription) && d.draft(ctx, id) != nil {
		return d.git().Discard(ctx, id)
	}
	return d.Store.Delete(ctx, id)
}

func (d *draftStore) Update(ctx context.Context, id string, mutate func(*v1alpha1.SystemDescription) error) (*descstore.Snapshot, error) {
	if _, err := d.Store.Get(ctx, id); errors.Is(err, descstore.ErrNoDescription) && d.draft(ctx, id) != nil {
		return nil, errors.New("this system is not pushed yet; push it first")
	}
	return d.Store.Update(ctx, id, mutate)
}
