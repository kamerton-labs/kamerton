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

// Propose implements descstore.GitWriter: one commit with one change.
func (s *Service) Propose(ctx context.Context, c descstore.GitChange) (*descstore.GitProposal, error) {
	return s.proposeAll(ctx, []descstore.GitChange{c}, c.Message, c.Author)
}

// proposeAll writes changes to the repository in one commit: the description files and, with
// a folder for them, the files generated from each.
func (s *Service) proposeAll(ctx context.Context, changes []descstore.GitChange, message, author string) (*descstore.GitProposal, error) {
	if !s.WriteEnabled() {
		return nil, descstore.ErrFromGit
	}
	if len(changes) == 0 {
		return nil, errors.New("nothing to push")
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
	if !lay.manifest && !first {
		s.mu.Lock()
		reqs := s.requests
		s.mu.Unlock()
		for _, r := range reqs {
			if r.System == "project" {
				// The project's own request is still open: its folders are not on the branch yet.
				return nil, fmt.Errorf("the project is not on branch %s yet: merge its pull request first (%s)", s.branch(), r.URL)
			}
		}
	}
	var titles []string
	for _, c := range changes {
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
			return nil, fmt.Errorf("system %s has no file path", c.System)
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
		titles = append(titles, fmt.Sprintf("%s system %s", what, c.System))
	}
	if st, err := wt.Status(); err != nil {
		return nil, err
	} else if st.IsClean() {
		return nil, errors.New("nothing to change: the repository already has this content")
	}
	title := titles[0]
	if len(titles) > 1 {
		title = fmt.Sprintf("Change %d systems", len(titles))
	}
	note := "Made in the Kamerton UI."
	if author != "" {
		note = "Made in the Kamerton UI by " + oneLine(author) + "."
	}
	if len(titles) > 1 {
		note = strings.Join(titles, "\n") + "\n\n" + note
	}
	// The first line of a person's message is the title; the rest goes into the body.
	if head, rest, _ := strings.Cut(strings.TrimSpace(message), "\n"); oneLine(head) != "" {
		title = oneLine(head)
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
		name := "changes"
		if len(changes) == 1 {
			name = safeRef(changes[0].System)
		}
		out.Branch = branchPrefix + name + "-" + short(hash.String())
	}
	spec := gitconfig.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", s.branch(), out.Branch))
	if err := repo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", Auth: auth, RefSpecs: []gitconfig.RefSpec{spec}}); err != nil {
		return nil, s.gitErr("push", err)
	}
	s.log().Info("git: pushed", "systems", len(changes), "branch", out.Branch, "commit", short(out.Commit))
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

// InitProject makes the configured folder a project: the project file, a README and a
// .gitignore. In a repository with other content the folder is new, so nothing of what is
// already there is touched. The folder must not hold a project yet.
func (s *Service) InitProject(ctx context.Context, author string) (*descstore.GitProposal, error) {
	if !s.WriteEnabled() {
		return nil, errors.New("creating a project needs push to be on")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	auth, err := s.auth(ctx)
	if err != nil {
		return nil, err
	}
	if auth == nil && isHTTP(s.URL) {
		return nil, errors.New("creating a project needs an access token with write access")
	}
	fs := memfs.New()
	repo, err := git.CloneContext(ctx, memory.NewStorage(), fs, &git.CloneOptions{
		URL: s.URL, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(s.branch()),
		SingleBranch: true, Depth: 1, Tags: git.NoTags,
	})
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
	root := cleanPath(s.Path)
	if _, err := fs.Stat(path.Join(root, ProjectFile)); err == nil {
		return nil, fmt.Errorf("%s already holds a project; open it instead", orDefault(root, "the repository"))
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	for name, content := range s.starterFiles() {
		if _, err := fs.Stat(name); err == nil && path.Base(name) != ProjectFile {
			// A README or .gitignore that is already there stays as it is.
			continue
		}
		if err := util.WriteFile(fs, name, content, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
		if _, err := wt.Add(name); err != nil {
			return nil, err
		}
	}
	title := "Start a Kamerton project"
	if root != "" {
		title += " in " + root
	}
	note := "Made in the Kamerton UI."
	if author != "" {
		note = "Made in the Kamerton UI by " + oneLine(author) + "."
	}
	hash, err := wt.Commit(title+"\n\n"+note+"\n", &git.CommitOptions{
		Author: &object.Signature{Name: "Kamerton", Email: "kamerton@noreply.invalid", When: s.now()},
	})
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	// A project is three new files in a folder of its own, so it goes straight to the followed
	// branch: systems can be pushed at once. Only a branch that refuses a push gets a request.
	out := &descstore.GitProposal{Branch: s.branch(), Direct: true, Commit: hash.String()}
	push := func(to string) error {
		spec := gitconfig.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", s.branch(), to))
		return repo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", Auth: auth, RefSpecs: []gitconfig.RefSpec{spec}})
	}
	if err := push(out.Branch); err != nil {
		if first || s.Write != WritePullRequest {
			return nil, s.gitErr("push", err)
		}
		out.Direct, out.Branch = false, branchPrefix+"project-"+short(hash.String())
		if err := push(out.Branch); err != nil {
			return nil, s.gitErr("push", err)
		}
	}
	s.log().Info("git: project created", "folder", root, "branch", out.Branch, "commit", short(out.Commit))
	if out.Direct {
		go s.Sync(context.WithoutCancel(ctx))
		return out, nil
	}
	if host := s.host(); host != nil {
		tok, _ := s.Token(ctx)
		url, err := host.openRequest(ctx, strings.TrimSpace(tok), out.Branch, s.branch(), title, note+" Systems appear here after the merge.")
		if err != nil {
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
