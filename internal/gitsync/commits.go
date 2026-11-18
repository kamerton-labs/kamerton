// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Commit is one commit of the followed branch.
type Commit struct {
	Hash   string    `json:"hash"`
	Title  string    `json:"title"`
	Author string    `json:"author"`
	When   time.Time `json:"when"`
	URL    string    `json:"url,omitempty"`
	// Systems whose description files the commit touches.
	Systems []string `json:"systems,omitempty"`
	// Applied: this is the commit the cluster was last read from.
	Applied bool `json:"applied,omitempty"`
}

// maxCommits bounds what one request reads.
const maxCommits = 50

// Commits lists the last commits of the followed branch, newest first. The result is kept
// until the branch moves.
func (s *Service) Commits(ctx context.Context, limit int) ([]Commit, error) {
	if limit <= 0 || limit > maxCommits {
		limit = maxCommits
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	auth, err := s.auth(ctx)
	if err != nil {
		return nil, err
	}
	head, err := s.remoteHead(ctx, auth)
	if err == errEmptyRepo {
		return []Commit{}, nil
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	cached, at, applied, lay := s.commits, s.commitsAt, s.status.Commit, s.lay
	s.mu.Unlock()
	if at == head && len(cached) > 0 {
		return markApplied(cached, applied, limit), nil
	}
	// One more than shown, so the oldest shown commit has a parent to compare with.
	repo, err := git.CloneContext(ctx, memory.NewStorage(), nil, &git.CloneOptions{
		URL: s.URL, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(s.branch()),
		SingleBranch: true, Depth: maxCommits + 1, Tags: git.NoTags,
	})
	if err != nil {
		return nil, s.gitErr("clone", err)
	}
	it, err := repo.Log(&git.LogOptions{From: plumbing.NewHash(head)})
	if err != nil {
		return nil, err
	}
	h, base := s.host(), s.webBase()
	var out []Commit
	_ = it.ForEach(func(c *object.Commit) error {
		if len(out) >= maxCommits {
			return errStop
		}
		title, _, _ := strings.Cut(c.Message, "\n")
		one := Commit{Hash: c.Hash.String(), Title: strings.TrimSpace(title), Author: c.Author.Name, When: c.Author.When.UTC(), Systems: touched(c, lay)}
		if h != nil && base != "" {
			one.URL = h.web(base, "commit", one.Hash, "")
		}
		out = append(out, one)
		return nil
	})
	s.mu.Lock()
	s.commits, s.commitsAt = out, head
	s.mu.Unlock()
	return markApplied(out, applied, limit), nil
}

var errStop = stopError{}

type stopError struct{}

func (stopError) Error() string { return "stop" }

func markApplied(list []Commit, applied string, limit int) []Commit {
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]Commit, len(list))
	copy(out, list)
	for i := range out {
		out[i].Applied = out[i].Hash == applied
	}
	return out
}

// touched names the systems whose description files differ from the commit's parent. A commit
// whose parent is beyond what was fetched gets none.
func touched(c *object.Commit, lay layout) []string {
	stats, err := c.Stats()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, st := range stats {
		p := st.Name
		if lay.skip(p) || !(lay.systems == "" || strings.HasPrefix(p, lay.systems+"/")) {
			continue
		}
		if ext := path.Ext(p); ext == ".yaml" || ext == ".yml" {
			if strings.TrimPrefix(p, lay.root+"/") == ProjectFile {
				continue
			}
			seen[strings.TrimSuffix(path.Base(p), ext)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
