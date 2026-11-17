// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package descstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
)

// File keeps descriptions in YAML files (local mode): one file per system.
//
// Paths lists description files; Dir, when set, is a directory whose
// *.yaml / *.yml files are all descriptions (re-scanned on every call) and
// where new systems are created as <name>.yaml. Without Dir new systems are
// written next to the first file and remembered until restart; start the
// core with a directory to keep them.
type File struct {
	Paths []string
	Dir   string

	mu      sync.Mutex
	created []string
}

// NewFile builds a file store from the --description value: one or more
// comma-separated files, or a directory.
func NewFile(spec string) (*File, error) {
	f := &File{}
	for _, p := range strings.Split(spec, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		switch {
		case err == nil && st.IsDir():
			if f.Dir != "" {
				return nil, fmt.Errorf("only one description directory is supported (%s, %s)", f.Dir, p)
			}
			f.Dir = p
		case err == nil || errors.Is(err, os.ErrNotExist):
			f.Paths = append(f.Paths, p)
		default:
			return nil, err
		}
	}
	if f.Dir == "" && len(f.Paths) == 0 {
		return nil, errors.New("no description file or directory given")
	}
	return f, nil
}

// Mode implements Store.
func (f *File) Mode() string { return ModeFile }

func (f *File) paths() ([]string, error) {
	f.mu.Lock()
	out := append(append([]string{}, f.Paths...), f.created...)
	f.mu.Unlock()
	if f.Dir != "" {
		entries, err := os.ReadDir(f.Dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || strings.HasPrefix(n, ".") || (!strings.HasSuffix(n, ".yaml") && !strings.HasSuffix(n, ".yml")) {
				continue
			}
			out = append(out, filepath.Join(f.Dir, n))
		}
	}
	seen := map[string]bool{}
	uniq := out[:0]
	for _, p := range out {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if !seen[abs] {
			seen[abs] = true
			uniq = append(uniq, p)
		}
	}
	return uniq, nil
}

// read loads one file. Missing files return ErrNoDescription; invalid files
// a snapshot with Invalid set.
func (f *File) read(path string) (*Snapshot, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoDescription
	}
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Raw: raw, Source: "file:" + path}
	res := description.Validate(raw)
	if res.Valid {
		snap.SD = res.Document
		return snap, nil
	}
	snap.Invalid = &res
	// Still find a name so the system can be listed and fixed.
	var meta struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if yaml.Unmarshal(raw, &meta) == nil && meta.Metadata.Name != "" {
		snap.ID = meta.Metadata.Name
	} else {
		snap.ID = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".yaml"), ".yml")
	}
	return snap, nil
}

// List implements Store. Files are re-read on every call, so edits made
// outside Kamerton are picked up.
func (f *File) List(context.Context) ([]*Snapshot, error) {
	paths, err := f.paths()
	if err != nil {
		return nil, err
	}
	var out []*Snapshot
	for _, p := range paths {
		snap, err := f.read(p)
		if errors.Is(err, ErrNoDescription) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	sortSnapshots(out, "")
	assignIDs(out, func(s *Snapshot) string { return strings.TrimPrefix(s.Source, "file:") })
	return out, nil
}

// Get implements Store.
func (f *File) Get(ctx context.Context, id string) (*Snapshot, error) {
	list, err := f.List(ctx)
	if err != nil {
		return nil, err
	}
	if s := findByID(list, id); s != nil {
		return s, nil
	}
	return nil, ErrNoDescription
}

func pathOf(s *Snapshot) string { return strings.TrimPrefix(s.Source, "file:") }

// Create implements Store.
func (f *File) Create(ctx context.Context, raw []byte) (*Snapshot, description.Result, error) {
	sd, res, err := validate(raw)
	if err != nil {
		return nil, res, err
	}
	list, err := f.List(ctx)
	if err != nil {
		return nil, res, err
	}
	for _, s := range list {
		if s.Name() == sd.Name {
			return nil, res, ErrExists
		}
	}
	dir := f.Dir
	if dir == "" && len(f.Paths) > 0 {
		dir = filepath.Dir(f.Paths[0])
	}
	if dir == "" {
		return nil, res, ErrNotWritable
	}
	path := filepath.Join(dir, sd.Name+".yaml")
	if _, err := os.Stat(path); err == nil {
		return nil, res, fmt.Errorf("%w: file %s exists", ErrExists, path)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := writeAtomic(path, raw); err != nil {
		return nil, res, err
	}
	if f.Dir == "" {
		f.created = append(f.created, path)
	}
	return &Snapshot{ID: sd.Name, SD: sd, Raw: raw, Source: "file:" + path}, res, nil
}

// Save implements Store. The file is replaced atomically and verbatim.
func (f *File) Save(ctx context.Context, id string, raw []byte) (*Snapshot, description.Result, error) {
	sd, res, err := validate(raw)
	if err != nil {
		return nil, res, err
	}
	cur, err := f.Get(ctx, id)
	if err != nil {
		return nil, res, err
	}
	if cur.Name() != sd.Name {
		return nil, res, ErrRename
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := writeAtomic(pathOf(cur), raw); err != nil {
		return nil, res, err
	}
	return &Snapshot{ID: cur.ID, SD: sd, Raw: raw, Source: cur.Source}, res, nil
}

// Update implements Store. Comments in the file are not preserved.
func (f *File) Update(ctx context.Context, id string, mutate func(*v1alpha1.SystemDescription) error) (*Snapshot, error) {
	cur, err := f.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.SD == nil {
		return nil, &InvalidError{Result: *cur.Invalid}
	}
	sd := cur.SD.DeepCopy()
	if err := mutate(sd); err != nil {
		return nil, err
	}
	raw, err := description.ToYAML(sd)
	if err != nil {
		return nil, err
	}
	snap, _, err := f.Save(ctx, id, raw)
	return snap, err
}

// Delete implements Store: the file is removed.
func (f *File) Delete(ctx context.Context, id string) error {
	cur, err := f.Get(ctx, id)
	if err != nil {
		return err
	}
	p := pathOf(cur)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Remove(p); err != nil {
		return err
	}
	f.Paths = removeString(f.Paths, p)
	f.created = removeString(f.created, p)
	return nil
}

func removeString(list []string, s string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
