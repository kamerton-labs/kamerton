// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package descstore stores system descriptions as cluster resources or local YAML files.
package descstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
)

const (
	ModeCluster = "cluster"
	ModeFile    = "file"
	ModeNone    = "none"
)

// AnnotationPrefix marks Kamerton's own annotations, such as the canvas layout, which survive a save from the UI.
const AnnotationPrefix = "kamerton.dev/"

var (
	ErrNoDescription = errors.New("no system description is loaded")
	ErrNotWritable   = errors.New("there is nowhere to save the description: start the core with --description FILE or in a cluster with the controller enabled")
	ErrConflict      = errors.New("the description was changed by someone else; reload and try again")
	ErrExists        = errors.New("a system with this name already exists")
	ErrRename        = errors.New("the name of a system cannot be changed; create a new system instead")
)

type InvalidError struct{ Result description.Result }

func (e *InvalidError) Error() string {
	if len(e.Result.Errors) > 0 {
		return "description is invalid: " + e.Result.Errors[0].Path + ": " + e.Result.Errors[0].Message
	}
	return "description is invalid"
}

type Snapshot struct {
	// ID is the name, or name~namespace when names repeat across namespaces.
	ID string
	// SD is nil when the stored document is invalid (see Invalid).
	SD *v1alpha1.SystemDescription
	// Raw is the YAML text: the file as written, or the resource in file form.
	Raw []byte
	// Source is "file:<path>" or "cluster:<namespace>/<name>".
	Source string
	Note   string
	// Invalid is set when the stored document fails validation; the UI can still open it as YAML.
	Invalid *description.Result
}

func (s *Snapshot) Name() string {
	if s.SD != nil {
		return s.SD.Name
	}
	return s.ID
}

type Store interface {
	Mode() string
	List(ctx context.Context) ([]*Snapshot, error)
	// Get returns ErrNoDescription when the system does not exist.
	Get(ctx context.Context, id string) (*Snapshot, error)
	// Create validates and stores a new system; ErrExists when the name is taken.
	Create(ctx context.Context, raw []byte) (*Snapshot, description.Result, error)
	// Save replaces an existing system; a different name fails with ErrRename.
	Save(ctx context.Context, id string, raw []byte) (*Snapshot, description.Result, error)
	Update(ctx context.Context, id string, mutate func(*v1alpha1.SystemDescription) error) (*Snapshot, error)
	Delete(ctx context.Context, id string) error
}

// Default returns the first valid system, with a note when there are several.
func Default(ctx context.Context, s Store) (*Snapshot, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var first *Snapshot
	for _, snap := range list {
		if snap.SD != nil {
			first = snap
			break
		}
	}
	if first == nil {
		if len(list) > 0 {
			return nil, fmt.Errorf("description %s: %w", list[0].Source, &InvalidError{Result: *list[0].Invalid})
		}
		return nil, ErrNoDescription
	}
	if len(list) > 1 {
		cp := *first
		cp.Note = fmt.Sprintf("found %d systems; showing %s", len(list), first.ID)
		return &cp, nil
	}
	return first, nil
}

func DefaultID(ctx context.Context, s Store) string {
	snap, err := Default(ctx, s)
	if err != nil {
		return ""
	}
	return snap.ID
}

func validate(raw []byte) (*v1alpha1.SystemDescription, description.Result, error) {
	res := description.Validate(raw)
	if !res.Valid {
		return nil, res, &InvalidError{Result: res}
	}
	return res.Document, res, nil
}

func assignIDs(list []*Snapshot, namespace func(*Snapshot) string) {
	count := map[string]int{}
	for _, s := range list {
		count[s.Name()]++
	}
	for _, s := range list {
		s.ID = s.Name()
		if count[s.Name()] > 1 {
			if ns := namespace(s); ns != "" {
				s.ID = s.Name() + "~" + ns
			}
		}
	}
}

func findByID(list []*Snapshot, id string) *Snapshot {
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	// A plain name also matches when it is unambiguous.
	var match *Snapshot
	for _, s := range list {
		if s.Name() == id {
			if match != nil {
				return nil
			}
			match = s
		}
	}
	return match
}

func sortSnapshots(list []*Snapshot, preferNamespace string) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.SD != nil && b.SD != nil && preferNamespace != "" {
			ai, bj := a.SD.Namespace == preferNamespace, b.SD.Namespace == preferNamespace
			if ai != bj {
				return ai
			}
		}
		if a.Name() != b.Name() {
			return a.Name() < b.Name()
		}
		return a.Source < b.Source
	})
}

// keepOwnAnnotations copies Kamerton's annotations from src to dst and drops those absent from src.
func keepOwnAnnotations(dst, src map[string]string) map[string]string {
	for k := range dst {
		if strings.HasPrefix(k, AnnotationPrefix) {
			if _, ok := src[k]; !ok {
				delete(dst, k)
			}
		}
	}
	for k, v := range src {
		if strings.HasPrefix(k, AnnotationPrefix) {
			if dst == nil {
				dst = map[string]string{}
			}
			dst[k] = v
		}
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

// Memory holds one description that cannot be saved: for runs without a file or cluster, and tests.
type Memory struct {
	mu   sync.RWMutex
	snap *Snapshot
}

func (m *Memory) Set(sd *v1alpha1.SystemDescription, raw []byte, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap = &Snapshot{ID: sd.Name, SD: sd, Raw: raw, Source: source}
}

func (m *Memory) Mode() string { return ModeNone }

func (m *Memory) List(context.Context) ([]*Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.snap == nil {
		return nil, nil
	}
	return []*Snapshot{m.snap}, nil
}

func (m *Memory) Get(_ context.Context, id string) (*Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.snap == nil || m.snap.ID != id {
		return nil, ErrNoDescription
	}
	return m.snap, nil
}

func (m *Memory) Create(_ context.Context, raw []byte) (*Snapshot, description.Result, error) {
	if _, res, err := validate(raw); err != nil {
		return nil, res, err
	}
	return nil, description.Result{}, ErrNotWritable
}

func (m *Memory) Save(_ context.Context, _ string, raw []byte) (*Snapshot, description.Result, error) {
	if _, res, err := validate(raw); err != nil {
		return nil, res, err
	}
	return nil, description.Result{}, ErrNotWritable
}

func (m *Memory) Update(context.Context, string, func(*v1alpha1.SystemDescription) error) (*Snapshot, error) {
	return nil, ErrNotWritable
}

func (m *Memory) Delete(context.Context, string) error { return ErrNotWritable }
