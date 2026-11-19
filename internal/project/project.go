// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package project is what Kamerton opens first: a project, with a name and a place. Run on a
// machine, the place is a folder on its disk; run in a cluster, the place is the cluster, and a
// git repository can stand behind either.
package project

import (
	"context"
	"errors"
	"strings"
	"unicode"
)

// Where a project lives.
const (
	WhereLocal   = "local"
	WhereCluster = "cluster"
)

// Info is what the first screen and the sidebar know of the project.
type Info struct {
	// Exists: a project is open. Without one the UI shows only the first screen.
	Exists bool   `json:"exists"`
	Name   string `json:"name,omitempty"`
	Where  string `json:"where"`
	// Path is the project's folder on disk (local).
	Path string `json:"path,omitempty"`
	// Namespace is where the systems are kept (cluster).
	Namespace string `json:"namespace,omitempty"`
	// DefaultRoot is the folder new projects go into unless another is named (local).
	DefaultRoot string `json:"defaultRoot,omitempty"`
	// Recent are projects opened before on this machine (local).
	Recent []Recent `json:"recent,omitempty"`
	// CanClone: a project can be taken from a git repository here.
	CanClone bool `json:"canClone"`
	// Git: the project's folder is a git working copy (local).
	Git bool `json:"git,omitempty"`
}

// Recent is a project opened before.
type Recent struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Closed: the project was closed by hand, so it does not open by itself on the next start.
	Closed bool `json:"closed,omitempty"`
}

// NewRequest makes a project.
type NewRequest struct {
	Name string `json:"name"`
	// Path is the folder (local); empty means DefaultRoot/<name>.
	Path string `json:"path"`
	// Git starts a git repository in the folder with the first commit (local).
	Git bool `json:"git"`
}

// CloneRequest takes a project from a git repository into a folder (local).
type CloneRequest struct {
	URL    string `json:"url"`
	Branch string `json:"branch"`
	// Folder is the project's folder inside the repository; empty is its root.
	Folder string `json:"folder"`
	Token  string `json:"token"`
	// Path is where the clone goes; empty means DefaultRoot/<repository>.
	Path string `json:"path"`
	// Name of the project when there is none in the repository yet; empty means the repository's.
	Name string `json:"name"`
}

// Projects is where the project lives: a folder on a machine, or the cluster.
type Projects interface {
	Info(ctx context.Context) Info
	New(ctx context.Context, req NewRequest, author string) (Info, error)
	// Rename gives the open project another name.
	Rename(ctx context.Context, name string) (Info, error)
}

// Local projects can also be opened by their folder and cloned from git.
type LocalProjects interface {
	Projects
	Open(ctx context.Context, path string) (Info, error)
	Close(ctx context.Context)
	Clone(ctx context.Context, req CloneRequest, author string) (Info, error)
	// The project's folder as a git working copy, pulled and pushed with the person's own git.
	GitState(ctx context.Context) GitState
	Fetch(ctx context.Context) (GitState, error)
	Pull(ctx context.Context) (GitState, error)
	Push(ctx context.Context, message string) (GitState, error)
}

var (
	// ErrExists: there is a project already; open it instead.
	ErrExists = errors.New("there is a project here already; open it instead")
	// ErrNotOpen: there is no project to change.
	ErrNotOpen = errors.New("no project is open")
	// ErrNoName: a project needs a name.
	ErrNoName = errors.New("give the project a name")
)

// UserError is a mistake in the request, as opposed to a failure of the disk or the network.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

func userErr(msg string) error { return &UserError{Msg: msg} }

// cleanName trims a name and refuses one that is empty or too long to show.
func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", ErrNoName
	}
	if len([]rune(name)) > 80 {
		return "", userErr("the name is longer than 80 characters")
	}
	return name, nil
}

// Slug is a folder name for a project name: letters and digits of any script kept, everything
// else a dash. "Мой магазин" → "мой-магазин".
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "project"
	}
	return out
}
