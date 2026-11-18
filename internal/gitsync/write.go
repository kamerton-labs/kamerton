// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/kamerton-labs/kamerton/internal/descstore"
)

// Write modes.
const (
	// WriteOff: the repository is only read; systems from git cannot be changed in the UI.
	WriteOff = ""
	// WritePullRequest: a change from the UI is pushed to a new branch, and a pull or merge
	// request is opened when the hosting service is known.
	WritePullRequest = "pullRequest"
	// WriteCommit: a change from the UI is committed straight to the followed branch.
	WriteCommit = "commit"
)

// ParseWrite maps the setting to a mode; unknown values are off.
func ParseWrite(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "pullrequest", "pull-request", "pr", "mergerequest", "mr":
		return WritePullRequest
	case "commit", "direct":
		return WriteCommit
	}
	return WriteOff
}

// RepoKey implements descstore.GitWriter.
func (s *Service) RepoKey() string { return s.repoKey() }

// WriteEnabled implements descstore.GitWriter.
func (s *Service) WriteEnabled() bool { return s.Write == WritePullRequest || s.Write == WriteCommit }

// Propose implements descstore.GitWriter: one commit with the change of one system's file and,
// when GeneratedPath is set, the files generated from it.
func (s *Service) Propose(ctx context.Context, c descstore.GitChange) (*descstore.GitProposal, error) {
	if !s.WriteEnabled() {
		return nil, descstore.ErrFromGit
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	auth, err := s.auth(ctx)
	if err != nil {
		return nil, err
	}
	if auth == nil && isHTTP(s.URL) {
		return nil, errors.New("writing needs an access token with write access in the Secret")
	}
	fs := memfs.New()
	repo, err := git.CloneContext(ctx, memory.NewStorage(), fs, &git.CloneOptions{
		URL: s.URL, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(s.branch()),
		SingleBranch: true, Depth: 1, Tags: git.NoTags,
	})
	// An empty repository has nothing to clone and no branch to open a request into: the first
	// change starts the followed branch, whatever the write mode.
	first := errors.Is(err, transport.ErrEmptyRemoteRepository)
	if first {
		fs = memfs.New()
		repo, err = git.InitWithOptions(memory.NewStorage(), fs, git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName(s.branch())})
		if err == nil {
			_, err = repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{s.URL}})
		}
	}
	if err != nil {
		return nil, s.gitErr("clone", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	if first {
		// A new repository gets its project file, a README and a .gitignore with the first system.
		for name, content := range s.starterFiles() {
			if err := util.WriteFile(fs, name, content, 0o644); err != nil {
				return nil, fmt.Errorf("write %s: %w", name, err)
			}
			if _, err := wt.Add(name); err != nil {
				return nil, err
			}
		}
	}
	lay, err := s.layoutOf(fs)
	if err != nil {
		return nil, err
	}
	file := cleanPath(c.Path)
	what := "Change"
	if c.New {
		what = "Add"
		file = path.Join(lay.systems, safeRef(c.System)+".yaml")
		if _, err := fs.Stat(file); err == nil {
			return nil, fmt.Errorf("the repository already has %s; a system with this name may be waiting there with an error", file)
		}
	}
	if file == "" {
		return nil, errors.New("the system has no file path")
	}
	if c.Content == nil {
		what = "Remove"
		if _, err := wt.Remove(file); err != nil {
			return nil, fmt.Errorf("remove %s: %w", file, err)
		}
	} else {
		if err := util.WriteFile(fs, file, c.Content, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", file, err)
		}
		if _, err := wt.Add(file); err != nil {
			return nil, err
		}
	}
	if err := s.writeGenerated(fs, wt, c, lay.generated); err != nil {
		return nil, err
	}
	if st, err := wt.Status(); err != nil {
		return nil, err
	} else if st.IsClean() {
		return nil, errors.New("nothing to change: the repository already has this content")
	}
	title := fmt.Sprintf("%s system %s", what, c.System)
	note := "Made in the Kamerton UI."
	if c.Author != "" {
		note = "Made in the Kamerton UI by " + oneLine(c.Author) + "."
	}
	// The first line of a person's message is the title; the rest goes into the body.
	if first, rest, _ := strings.Cut(strings.TrimSpace(c.Message), "\n"); oneLine(first) != "" {
		title = oneLine(first)
		if rest = strings.TrimSpace(rest); rest != "" {
			note = rest + "\n\n" + note
		}
	}
	hash, err := wt.Commit(title+"\n\n"+note+"\n", &git.CommitOptions{
		Author: &object.Signature{Name: "Kamerton", Email: "kamerton@noreply.invalid", When: s.now()},
	})
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	out := &descstore.GitProposal{Branch: s.branch(), Direct: s.Write == WriteCommit || first, Commit: hash.String()}
	if !out.Direct {
		out.Branch = branchPrefix + safeRef(c.System) + "-" + short(hash.String())
	}
	spec := gitconfig.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", s.branch(), out.Branch))
	if err := repo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", Auth: auth, RefSpecs: []gitconfig.RefSpec{spec}}); err != nil {
		return nil, s.gitErr("push", err)
	}
	s.log().Info("git: change written", "system", c.System, "file", file, "branch", out.Branch, "commit", short(out.Commit))
	if out.Direct {
		// Do not wait for the next tick.
		go s.Sync(context.WithoutCancel(ctx))
		return out, nil
	}
	if host := s.host(); host != nil {
		tok, _ := s.Token(ctx)
		url, err := host.openRequest(ctx, strings.TrimSpace(tok), out.Branch, s.branch(), title, note+" Kamerton applies it after the merge.")
		if err != nil {
			// The branch is there; the request can be opened by hand.
			s.log().Warn("git: branch pushed, but the request was not opened", "branch", out.Branch, "error", err)
		}
		out.URL = url
		s.refreshRequests(ctx)
	}
	return out, nil
}

// writeGenerated replaces <GeneratedPath>/<system>/ with what the description generates, so the
// review shows which rules change.
func (s *Service) writeGenerated(fs billy.Filesystem, wt *git.Worktree, c descstore.GitChange, root string) error {
	if root == "" || s.Generate == nil {
		return nil
	}
	dir := path.Join(root, safeRef(c.System))
	if _, err := fs.Stat(dir); err == nil {
		if _, err := wt.Remove(dir); err != nil {
			return fmt.Errorf("remove %s: %w", dir, err)
		}
	}
	if c.Content == nil {
		return nil
	}
	files, err := s.Generate(c.Content)
	if err != nil {
		return fmt.Errorf("generate rules: %w", err)
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := path.Join(dir, cleanPath(n))
		if err := util.WriteFile(fs, p, files[n], 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
		if _, err := wt.Add(p); err != nil {
			return err
		}
	}
	return nil
}

// oneLine keeps a title on one line and within what hosting services show.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func isHTTP(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// safeRef keeps what is safe in a branch name and a folder name.
func safeRef(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if out := strings.Trim(b.String(), ".-"); out != "" {
		return out
	}
	return "system"
}
