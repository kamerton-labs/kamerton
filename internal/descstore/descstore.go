// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package descstore reads and writes system descriptions: SystemDescription
// custom resources in a cluster, or YAML files locally. One description is
// one system. The HTTP API and the Telegram setup both go through it, so the
// UI always shows what the controller reconciles.
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

// Modes of a store.
const (
	ModeCluster = "cluster"
	ModeFile    = "file"
	ModeNone    = "none"
)

// AnnotationPrefix marks annotations owned by Kamerton (for example the
// canvas layout). They are kept when a description is saved from the UI.
const AnnotationPrefix = "kamerton.dev/"

var (
	// ErrNoDescription means the system does not exist (or none exists yet).
	ErrNoDescription = errors.New("no system description is loaded")
	// ErrNotWritable means there is nowhere to save the description.
	ErrNotWritable = errors.New("there is nowhere to save the description: start the core with --description FILE or in a cluster with the controller enabled")
	// ErrConflict means the description changed since it was read.
	ErrConflict = errors.New("the description was changed by someone else; reload and try again")
	// ErrExists means a system with that name already exists.
	ErrExists = errors.New("a system with this name already exists")
	// ErrRename means the document's name differs from the system being saved.
	ErrRename = errors.New("the name of a system cannot be changed; create a new system instead")
)

// InvalidError carries validation results of a rejected description.
type InvalidError struct{ Result description.Result }

func (e *InvalidError) Error() string {
	if len(e.Result.Errors) > 0 {
		return "description is invalid: " + e.Result.Errors[0].Path + ": " + e.Result.Errors[0].Message
	}
	return "description is invalid"
}

// Snapshot is one stored description (one system).
type Snapshot struct {
	// ID identifies the system in the API: the name, or name~namespace
	// when two descriptions in different namespaces share a name.
	ID string
	// SD is nil when the stored document is invalid (see Invalid).
	SD *v1alpha1.SystemDescription
	// Raw is the YAML text: the file as written, or the resource in file form.
	Raw []byte
	// Source is "file:<path>" or "cluster:<namespace>/<name>".
	Source string
	// Note is a remark for the UI, for example when several descriptions exist.
	Note string
	// Invalid is set when the stored document does not pass validation
	// (a file edited by hand). The UI can still open it in YAML mode.
	Invalid *description.Result
}

// Name is the system name.
func (s *Snapshot) Name() string {
	if s.SD != nil {
		return s.SD.Name
	}
	return s.ID
}

// Store reads and writes descriptions.
type Store interface {
	Mode() string
	// List returns all systems, sorted (see Default for the order).
	List(ctx context.Context) ([]*Snapshot, error)
	// Get returns ErrNoDescription when the system does not exist.
	Get(ctx context.Context, id string) (*Snapshot, error)
	// Create validates and stores a new system; ErrExists when the name is taken.
	Create(ctx context.Context, raw []byte) (*Snapshot, description.Result, error)
	// Save validates and replaces an existing system. The document's name
	// must match the system (ErrRename).
	Save(ctx context.Context, id string, raw []byte) (*Snapshot, description.Result, error)
	// Update applies mutate to a system, validates and stores it.
	Update(ctx context.Context, id string, mutate func(*v1alpha1.SystemDescription) error) (*Snapshot, error)
	// Delete removes a system.
	Delete(ctx context.Context, id string) error
}

// Default returns the first valid system: the one the single-description
// endpoints (/api/v1/description, …) and old links operate on. It carries a
// note when several exist.
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

// DefaultID is the ID of the default system, or "" when there is none.
func DefaultID(ctx context.Context, s Store) string {
	snap, err := Default(ctx, s)
	if err != nil {
		return ""
	}
	return snap.ID
}

// validate checks a YAML document and returns the decoded description.
func validate(raw []byte) (*v1alpha1.SystemDescription, description.Result, error) {
	res := description.Validate(raw)
	if !res.Valid {
		return nil, res, &InvalidError{Result: res}
	}
	return res.Document, res, nil
}

// assignIDs gives every snapshot its ID: the name, or name~namespace when
// names repeat. list must be sorted.
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

// keepOwnAnnotations copies Kamerton's annotations from src to dst,
// removing those absent from src.
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

// ---- Memory (no storage; a description may be set programmatically) ----

// Memory holds one description that cannot be saved. Used when the core runs
// without a file and without a cluster, and in tests.
type Memory struct {
	mu   sync.RWMutex
	snap *Snapshot
}

// Set replaces the description.
func (m *Memory) Set(sd *v1alpha1.SystemDescription, raw []byte, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap = &Snapshot{ID: sd.Name, SD: sd, Raw: raw, Source: source}
}

// Mode implements Store.
func (m *Memory) Mode() string { return ModeNone }

// List implements Store.
func (m *Memory) List(context.Context) ([]*Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.snap == nil {
		return nil, nil
	}
	return []*Snapshot{m.snap}, nil
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, id string) (*Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.snap == nil || m.snap.ID != id {
		return nil, ErrNoDescription
	}
	return m.snap, nil
}

// Create implements Store.
func (m *Memory) Create(_ context.Context, raw []byte) (*Snapshot, description.Result, error) {
	if _, res, err := validate(raw); err != nil {
		return nil, res, err
	}
	return nil, description.Result{}, ErrNotWritable
}

// Save implements Store.
func (m *Memory) Save(_ context.Context, _ string, raw []byte) (*Snapshot, description.Result, error) {
	if _, res, err := validate(raw); err != nil {
		return nil, res, err
	}
	return nil, description.Result{}, ErrNotWritable
}

// Update implements Store.
func (m *Memory) Update(context.Context, string, func(*v1alpha1.SystemDescription) error) (*Snapshot, error) {
	return nil, ErrNotWritable
}

// Delete implements Store.
func (m *Memory) Delete(context.Context, string) error { return ErrNotWritable }
