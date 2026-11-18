// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package gitsync reads system descriptions from a git repository and applies them. The
// repository is then the place where a system is changed and reviewed; Kamerton only follows it.
package gitsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"sigs.k8s.io/yaml"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
)

const (
	DefaultInterval = time.Minute
	// maxFile and maxFiles bound what a repository can make Kamerton read.
	maxFile  = 1 << 20
	maxFiles = 500
)

// File states.
const (
	StateApplied   = "applied"
	StateUnchanged = "unchanged"
	StateInvalid   = "invalid"
	StateConflict  = "conflict"
	StateFailed    = "failed"
)

// Target is where descriptions go; *descstore.Cluster implements it.
type Target interface {
	ApplyGit(ctx context.Context, raw []byte, src descstore.GitSource) (name string, changed bool, res description.Result, err error)
	PruneGit(ctx context.Context, repo string, keep map[string]bool) ([]string, error)
	GitKey(doc *v1alpha1.SystemDescription) string
	GitPaths(ctx context.Context, repo string) ([]string, error)
}

// errEmptyRepo: the repository has no commits yet, which is how every repository starts.
var errEmptyRepo = errors.New("the repository is empty")

// errNoBranch: the repository has commits, but not on the followed branch.
var errNoBranch = errors.New("branch not found")

// errNoFolder: the folder is not on the branch. A typo and a folder whose last file was
// deleted look the same in git.
var errNoFolder = errors.New("folder not found")

type Service struct {
	// URL is an http(s) or file URL of the repository, Branch its branch, Path a folder in it
	// (empty: the whole repository).
	URL, Branch, Path string
	// Token, when it returns a non-empty string, is sent as the password of HTTP basic auth,
	// which is how GitHub, GitLab and Gitea take access tokens. Read on every sync.
	Token  func(ctx context.Context) (string, error)
	Target Target
	// Write says what a save in the UI does to a system from git: WriteOff, WritePullRequest or
	// WriteCommit. Writing needs a token with write access.
	Write string
	// Provider (github or gitlab) and APIURL are worked out from URL for github.com and
	// gitlab hosts; set them for a self-hosted service.
	Provider, APIURL string
	// CommitStatus marks every read commit on the hosting service as applied or not.
	CommitStatus bool
	// GeneratedPath, when set, is a folder where a change written by Kamerton also carries the
	// files generated from the description (<GeneratedPath>/<system>/), made by Generate.
	GeneratedPath string
	Generate      func(description []byte) (map[string][]byte, error)
	// Pending keeps the changes saved in the UI until they are pushed; nil: in memory.
	Pending  PendingStore
	Interval time.Duration
	Logger   *slog.Logger
	Now      func() time.Time

	mu     sync.Mutex
	status Status
	// raw holds the files of the last read by path, so the UI edits the text people wrote,
	// comments included, not the cluster's copy.
	raw map[string][]byte
	// reported is the commit and outcome last sent as a commit status.
	reported string
	// requests are the open requests seen at the last look.
	requests []Request
	// lay is the layout seen at the last read.
	lay layout
	// commits is the history read while the branch stood at commitsAt.
	commits   []Commit
	commitsAt string
	// history is the clone the commits were read from, kept to open one of them.
	history *git.Repository
}

// webBase is the repository's address for a browser; empty when the URL is not http(s).
func (s *Service) webBase() string {
	if !isHTTP(s.URL) {
		return ""
	}
	return strings.TrimSuffix(s.Repo(), ".git")
}

// FileURL opens a file of the followed branch on the hosting service; empty when unknown.
func (s *Service) FileURL(file string) string {
	h, base := s.host(), s.webBase()
	if h == nil || base == "" {
		return ""
	}
	return h.web(base, "file", s.branch(), file)
}

// refreshRequests looks up the open requests; a failure keeps the last list.
func (s *Service) refreshRequests(ctx context.Context) {
	h := s.host()
	if h == nil || s.Token == nil || !s.WriteEnabled() {
		return
	}
	tok, err := s.Token(ctx)
	if err != nil || strings.TrimSpace(tok) == "" {
		return
	}
	list, err := h.openRequests(ctx, strings.TrimSpace(tok))
	if err != nil {
		s.log().Debug("git: open requests unavailable", "error", err)
		return
	}
	s.mu.Lock()
	s.requests = list
	s.mu.Unlock()
}

// Raw returns a file as it was at the last read.
func (s *Service) Raw(path string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.raw[path]
	return b, ok
}

// WriteMode is the write mode in effect.
func (s *Service) WriteMode() string {
	if s.WriteEnabled() {
		return s.Write
	}
	return WriteOff
}

// report marks the commit on the hosting service; a refusal is logged once per outcome.
func (s *Service) report(ctx context.Context, st Status) {
	h := s.host()
	if !s.CommitStatus || h == nil || s.Token == nil || st.Commit == "" {
		return
	}
	ok, text := true, fmt.Sprintf("applied: %d systems", len(st.Files))
	for _, f := range st.Files {
		if f.State != StateApplied && f.State != StateUnchanged {
			ok, text = false, fmt.Sprintf("not applied: %s: %s", f.Path, f.Error)
			break
		}
	}
	key := st.Commit + "|" + text
	s.mu.Lock()
	done := s.reported == key
	s.reported = key
	s.mu.Unlock()
	if done {
		return
	}
	tok, err := s.Token(ctx)
	if err == nil {
		err = h.setStatus(ctx, strings.TrimSpace(tok), st.Commit, ok, text)
	}
	if err != nil {
		s.log().Warn("git: commit status not set", "commit", short(st.Commit), "error", err)
	}
}

type FileStatus struct {
	Path   string `json:"path"`
	System string `json:"system,omitempty"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}

// Status is the outcome of the last sync, for the UI.
type Status struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path,omitempty"`
	// Commit is the commit the files were read from; empty before the first successful read.
	Commit    string    `json:"commit,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	SyncedAt  time.Time `json:"syncedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
	// Project is the name in the project file; HasProjectFile is false for a repository laid
	// out by the installation's settings. SystemsPath and GeneratedPath are where files are.
	Project        string `json:"project,omitempty"`
	HasProjectFile bool   `json:"hasProjectFile"`
	SystemsPath    string `json:"systemsPath,omitempty"`
	GeneratedPath  string `json:"generatedPath,omitempty"`
	// Empty: the repository has no commits yet.
	Empty bool `json:"empty,omitempty"`
	// Write: pullRequest, commit or empty when the repository is only read.
	Write   string       `json:"write,omitempty"`
	Files   []FileStatus `json:"files"`
	Removed []string     `json:"removed,omitempty"`
	// Requests are the open pull or merge requests Kamerton made, when the hosting service is known.
	Requests []Request `json:"requests,omitempty"`
	// RepoURL and CommitURL open the repository and the applied commit on the hosting service.
	RepoURL   string `json:"repoURL,omitempty"`
	CommitURL string `json:"commitURL,omitempty"`
}

// NeedLeaderElection: one replica writes.
func (s *Service) NeedLeaderElection() bool { return true }

func (s *Service) Start(ctx context.Context) error {
	every := s.Interval
	if every <= 0 {
		every = DefaultInterval
	}
	s.Sync(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.Sync(ctx)
		}
	}
}

// Status returns the outcome of the last sync.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.Repo, st.Branch, st.Path = s.Repo(), s.branch(), cleanPath(s.Path)
	st.Write = s.WriteMode()
	st.Project, st.SystemsPath, st.GeneratedPath, st.HasProjectFile = s.lay.name, s.lay.systems, s.lay.generated, s.lay.manifest
	st.Requests = s.requests
	if base := s.webBase(); base != "" {
		st.RepoURL = base
		if h := s.host(); h != nil && st.Commit != "" {
			st.CommitURL = h.web(base, "commit", st.Commit, "")
		}
	}
	if st.Files == nil {
		st.Files = []FileStatus{}
	}
	return st
}

// Repo is the repository URL without credentials: it goes into annotations and the UI.
func (s *Service) Repo() string {
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme == "" {
		return s.URL
	}
	u.User = nil
	return u.String()
}

func (s *Service) branch() string {
	if s.Branch == "" {
		return "main"
	}
	return s.Branch
}

// repoKey tells this repository and branch from any other, for pruning.
func (s *Service) repoKey() string { return s.Repo() + "#" + s.branch() }

// Sync reads the branch and applies its descriptions. Nothing is deleted unless the whole read
// succeeded, so a network failure or a mistyped folder never removes a system.
func (s *Service) Sync(ctx context.Context) Status {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	now := s.now()
	fail := func(err error) Status {
		s.log().Warn("git: sync failed", "repo", s.Repo(), "branch", s.branch(), "error", err)
		s.mu.Lock()
		s.status.CheckedAt, s.status.Error = now, err.Error()
		s.mu.Unlock()
		return s.Status()
	}
	auth, err := s.auth(ctx)
	if err != nil {
		return fail(err)
	}
	head, err := s.remoteHead(ctx, auth)
	if errors.Is(err, errEmptyRepo) {
		// Not a failure: nothing to read yet, and nothing is deleted. The first system made in
		// the UI, or the first push, starts the branch.
		s.mu.Lock()
		s.status = Status{CheckedAt: now, Empty: true}
		s.raw = nil
		s.mu.Unlock()
		return s.Status()
	}
	if err != nil {
		return fail(err)
	}
	s.refreshRequests(ctx)
	s.mu.Lock()
	last, clean := s.status.Commit, s.status.Error == "" && !hasProblems(s.status.Files)
	s.mu.Unlock()
	if head == last && clean {
		// Nothing new, and the last pass left nothing to retry.
		s.mu.Lock()
		s.status.CheckedAt = now
		s.mu.Unlock()
		return s.Status()
	}
	files, lay, err := s.read(ctx, auth)
	if errors.Is(err, errNoFolder) && s.folderMayBeEmpty(ctx, lay.systems) {
		// Either the folder's last file is gone, or nothing was ever read from this repository
		// and the folder is yet to be made. A typo is told apart below.
		files, err = nil, nil
	}
	if err != nil {
		return fail(err)
	}
	src := descstore.GitSource{Repo: s.repoKey(), Commit: head}
	keep := map[string]bool{}
	prune := true
	var out []FileStatus
	for _, f := range files {
		fs := FileStatus{Path: f.path}
		// Kept even when the file is broken: a mistake in a file must not delete its system.
		if id := identity(f.raw); id != nil {
			keep[s.Target.GitKey(id)] = true
			fs.System = id.Name
		} else {
			// Which system this file is cannot be told, so nothing is deleted this time.
			prune = false
		}
		src.Path = f.path
		name, changed, res, err := s.Target.ApplyGit(ctx, f.raw, src)
		if name != "" {
			fs.System = name
		}
		var inv *descstore.InvalidError
		switch {
		case errors.As(err, &inv):
			fs.State, fs.Error = StateInvalid, firstIssue(res)
		case errors.Is(err, descstore.ErrNotFromGit):
			fs.State, fs.Error = StateConflict, err.Error()
		case err != nil:
			fs.State, fs.Error = StateFailed, err.Error()
		case changed:
			fs.State = StateApplied
		default:
			fs.State = StateUnchanged
		}
		if fs.State == StateApplied {
			s.log().Info("git: system applied", "file", f.path, "system", fs.System, "commit", short(head))
		} else if fs.Error != "" && s.firstTime(head, fs) {
			s.log().Warn("git: file not applied", "file", f.path, "system", fs.System, "state", fs.State, "error", fs.Error)
		}
		out = append(out, fs)
	}
	var removed []string
	if prune {
		removed, err = s.Target.PruneGit(ctx, s.repoKey(), keep)
		if err != nil {
			return fail(err)
		}
		for _, name := range removed {
			s.log().Info("git: system removed, its file is gone", "system", name, "commit", short(head))
		}
	}
	raw := make(map[string][]byte, len(files))
	for _, f := range files {
		raw[f.path] = f.raw
	}
	s.mu.Lock()
	s.status = Status{Commit: head, CheckedAt: now, SyncedAt: now, Files: out, Removed: removed}
	s.raw = raw
	s.lay = lay
	s.mu.Unlock()
	st := s.Status()
	s.report(ctx, st)
	return st
}

// firstTime: a problem is retried on every pass, but logged once per commit.
func (s *Service) firstTime(commit string, f FileStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Commit != commit {
		return true
	}
	for _, old := range s.status.Files {
		if old.Path == f.Path {
			return old.State != f.State || old.Error != f.Error
		}
	}
	return true
}

// folderMayBeEmpty tells an empty folder from a mistyped one. A typo shows as systems that came
// from this repository but from another folder: then the missing folder is an error, and
// nothing is deleted. With no systems from this repository at all there is nothing to lose.
func (s *Service) folderMayBeEmpty(ctx context.Context, root string) bool {
	paths, err := s.Target.GitPaths(ctx, s.repoKey())
	if err != nil {
		return false
	}
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}

func hasProblems(files []FileStatus) bool {
	for _, f := range files {
		if f.State != StateApplied && f.State != StateUnchanged {
			return true
		}
	}
	return false
}

func firstIssue(res description.Result) string {
	if len(res.Errors) == 0 {
		return "the description is invalid"
	}
	return res.Errors[0].Path + ": " + res.Errors[0].Message
}

func (s *Service) auth(ctx context.Context) (transport.AuthMethod, error) {
	if s.Token == nil {
		return nil, nil
	}
	tok, err := s.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the access token: %w", err)
	}
	if tok = strings.TrimSpace(tok); tok == "" {
		return nil, nil
	}
	return &githttp.BasicAuth{Username: "kamerton", Password: tok}, nil
}

func (s *Service) remoteHead(ctx context.Context, auth transport.AuthMethod) (string, error) {
	remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{s.URL}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return "", errEmptyRepo
	}
	if err != nil {
		return "", s.gitErr("list branches", err)
	}
	want := plumbing.NewBranchReferenceName(s.branch())
	for _, r := range refs {
		if r.Name() == want {
			return r.Hash().String(), nil
		}
	}
	return "", fmt.Errorf("%w: %q in %s", errNoBranch, s.branch(), s.Repo())
}

type file struct {
	path string
	raw  []byte
}

// read clones the tip of the branch into memory and returns the description files under Path.
func (s *Service) read(ctx context.Context, auth transport.AuthMethod) ([]file, layout, error) {
	fs := memfs.New()
	_, err := git.CloneContext(ctx, memory.NewStorage(), fs, &git.CloneOptions{
		URL: s.URL, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(s.branch()),
		SingleBranch: true, Depth: 1, Tags: git.NoTags,
	})
	if err != nil {
		return nil, layout{}, s.gitErr("clone", err)
	}
	lay, err := s.layoutOf(fs)
	if err != nil {
		return nil, lay, err
	}
	if lay.systems != "" {
		if st, err := fs.Stat(lay.systems); err != nil || !st.IsDir() {
			return nil, lay, fmt.Errorf("%w: %q on branch %s", errNoFolder, lay.systems, s.branch())
		}
	}
	var out []file
	if err := walk(fs, lay.systems, lay, &out); err != nil {
		return nil, lay, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, lay, nil
}

func walk(fs billy.Filesystem, dir string, lay layout, out *[]file) error {
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read folder %q: %w", dir, err)
	}
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		if lay.skip(p) {
			continue
		}
		if e.IsDir() {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if err := walk(fs, p, lay, out); err != nil {
				return err
			}
			continue
		}
		if ext := path.Ext(e.Name()); ext != ".yaml" && ext != ".yml" || e.Size() > maxFile {
			continue
		}
		f, err := fs.Open(p)
		if err != nil {
			return fmt.Errorf("open %s: %w", p, err)
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxFile))
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if !isDescription(raw) {
			continue
		}
		if len(*out) >= maxFiles {
			return fmt.Errorf("more than %d description files; point Kamerton at a folder", maxFiles)
		}
		*out = append(*out, file{path: p, raw: raw})
	}
	return nil
}

// isDescription: other YAML in the same folder (Helm values, CI files) is none of our business.
func isDescription(raw []byte) bool {
	var head struct {
		Kind string `json:"kind"`
	}
	return yaml.Unmarshal(raw, &head) == nil && head.Kind == "SystemDescription"
}

// identity reads the name and namespace of a file's system even when the rest does not validate.
func identity(raw []byte) *v1alpha1.SystemDescription {
	var head struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if yaml.Unmarshal(raw, &head) != nil || head.Metadata.Name == "" {
		return nil
	}
	sd := &v1alpha1.SystemDescription{}
	sd.Name, sd.Namespace = head.Metadata.Name, head.Metadata.Namespace
	return sd
}

func cleanPath(p string) string {
	p = strings.Trim(path.Clean("/"+p), "/")
	if p == "." {
		return ""
	}
	return p
}

// gitErr words the failures people actually meet, without the URL's credentials.
func (s *Service) gitErr(what string, err error) error {
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		return &repoErr{fmt.Sprintf("%s %s: access denied; check the token in the Secret", what, s.Repo()), err}
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return &repoErr{fmt.Sprintf("%s %s: repository not found (a private repository looks the same without a token)", what, s.Repo()), err}
	}
	return &repoErr{fmt.Sprintf("%s %s: %s", what, s.Repo(), strings.ReplaceAll(err.Error(), s.URL, s.Repo())), err}
}

// repoErr is a message about a repository that keeps the error behind it, so Problem can
// still tell what went wrong.
type repoErr struct {
	msg string
	err error
}

func (e *repoErr) Error() string { return e.msg }
func (e *repoErr) Unwrap() error { return e.err }

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
