// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"sigs.k8s.io/yaml"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/gitsync"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

// Local keeps projects as folders on the machine Kamerton runs on, like an editor does: a new
// project is a folder with a project file, an existing one is opened by its folder, and one in
// git is cloned into a folder. The folder is then the working copy; committing and pushing are
// done there with the person's own git.
type Local struct {
	// Root is where new projects go unless a folder is named: ~/kamerton.
	Root string
	// RecentFile lists the projects opened before.
	RecentFile string
	// Descriptions and Stacks are switched to the project that is opened.
	Descriptions *descstore.Switch
	Stacks       *stack.SwitchStateStore
	// StacksFile, when set, keeps the sets in one file whatever project is open.
	StacksFile string
	Logger     *slog.Logger

	mu  sync.Mutex
	cur *Info
}

// NewLocal fills in the default folders of this user.
func NewLocal(descriptions *descstore.Switch, stacks *stack.SwitchStateStore, log *slog.Logger) *Local {
	l := &Local{Descriptions: descriptions, Stacks: stacks, Logger: log}
	if home, err := os.UserHomeDir(); err == nil {
		l.Root = filepath.Join(home, "kamerton")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		l.RecentFile = filepath.Join(dir, "kamerton", "projects.json")
	}
	return l
}

func (l *Local) log() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}

func (l *Local) Info(context.Context) Info {
	l.mu.Lock()
	cur := l.cur
	l.mu.Unlock()
	in := Info{Where: WhereLocal, DefaultRoot: tilde(l.Root), CanClone: true}
	if cur != nil {
		in.Exists, in.Name, in.Path, in.Git = true, cur.Name, tilde(cur.Path), cur.Git
	}
	for _, r := range l.recent() {
		if cur != nil && r.Path == cur.Path {
			continue
		}
		if st, err := os.Stat(r.Path); err == nil && st.IsDir() {
			in.Recent = append(in.Recent, Recent{Name: r.Name, Path: tilde(r.Path)})
		}
	}
	return in
}

// tilde shows a path in the home folder as ~/…, the way people write it; requests take it back.
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return "~" + string(filepath.Separator) + rel
	}
	return p
}

// Adopt makes a store given at start (--description with files, not a project folder) the open
// project, named after its folder.
func (l *Local) Adopt(store descstore.Store, dir string) {
	l.Descriptions.Set(store)
	abs, _ := filepath.Abs(dir)
	l.mu.Lock()
	l.cur = &Info{Name: filepath.Base(abs), Path: abs, Git: inGit(abs)}
	l.mu.Unlock()
}

// Close closes the open project, back to the first screen; nothing on disk changes, and the
// project stays among the recent ones.
func (l *Local) Close(context.Context) {
	l.Descriptions.Set(nil)
	if l.Stacks != nil && l.StacksFile == "" {
		l.Stacks.Set(nil)
	}
	l.mu.Lock()
	l.cur = nil
	l.mu.Unlock()
}

// OpenLast opens the project opened last, the way an editor comes back to where it was. It
// reports whether one was opened; a project that is gone or broken is skipped quietly.
func (l *Local) OpenLast(ctx context.Context) bool {
	for _, r := range l.recent() {
		if _, err := l.Open(ctx, r.Path); err == nil {
			return true
		}
	}
	return false
}

// IsProject reports whether a folder holds a project file.
func IsProject(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, gitsync.ProjectFile))
	return err == nil && !st.IsDir()
}

// Open opens the project in a folder: one with a project file, or a folder of description files
// from before project files existed.
func (l *Local) Open(_ context.Context, dir string) (Info, error) {
	abs, err := l.abs(dir)
	if err != nil {
		return Info{}, err
	}
	st, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, userErr("there is no folder " + abs)
	}
	if err != nil {
		return Info{}, err
	}
	if !st.IsDir() {
		return Info{}, userErr(abs + " is a file, not a folder")
	}
	name, systems := filepath.Base(abs), abs
	if raw, err := os.ReadFile(filepath.Join(abs, gitsync.ProjectFile)); err == nil {
		var p gitsync.Project
		if err := yaml.UnmarshalStrict(raw, &p); err != nil {
			return Info{}, userErr(fmt.Sprintf("%s: %s", gitsync.ProjectFile, strings.TrimPrefix(err.Error(), "error unmarshaling JSON: ")))
		}
		if p.Kind != "Project" {
			return Info{}, userErr(gitsync.ProjectFile + ": kind must be Project")
		}
		if n := strings.TrimSpace(p.Metadata.Name); n != "" {
			name = n
		}
		sub := strings.Trim(path.Clean("/"+filepath.ToSlash(p.Spec.Systems)), "/")
		if sub == "" {
			sub = "systems"
		}
		systems = filepath.Join(abs, filepath.FromSlash(sub))
		if err := os.MkdirAll(systems, 0o755); err != nil {
			return Info{}, err
		}
	} else if !hasDescriptions(abs) {
		return Info{}, userErr("there is no project in " + abs + "; create one there")
	}
	store, err := descstore.NewFile(systems)
	if err != nil {
		return Info{}, err
	}
	l.Descriptions.Set(store)
	if l.Stacks != nil {
		file := l.StacksFile
		if file == "" {
			file = filepath.Join(abs, ".kamerton-stacks.json")
		}
		l.Stacks.Set(&stack.FileStateStore{Path: file})
	}
	cur := &Info{Name: name, Path: abs, Git: inGit(abs)}
	l.mu.Lock()
	l.cur = cur
	l.mu.Unlock()
	l.remember(Recent{Name: name, Path: abs})
	l.log().Info("project opened", "name", name, "path", abs, "systems", systems)
	return l.Info(context.Background()), nil
}

// New makes a project in a folder, by default DefaultRoot/<name>, and opens it. A folder that is
// already there is fine as long as it holds no project; its README and .gitignore stay.
func (l *Local) New(ctx context.Context, req NewRequest, author string) (Info, error) {
	name, err := cleanName(req.Name)
	if err != nil {
		return Info{}, err
	}
	dir := req.Path
	if strings.TrimSpace(dir) == "" {
		if l.Root == "" {
			return Info{}, userErr("name a folder for the project")
		}
		dir = filepath.Join(l.Root, Slug(name))
	}
	abs, err := l.abs(dir)
	if err != nil {
		return Info{}, err
	}
	if IsProject(abs) {
		return Info{}, ErrExists
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		return Info{}, userErr(abs + " is a file, not a folder")
	}
	created := !exists(abs)
	undo := func() {
		if created {
			_ = os.RemoveAll(abs)
		}
	}
	// A local project is read from its folder, with git or without: the README says so.
	written, err := writeStarter(abs, name, "", false)
	if err != nil {
		undo()
		return Info{}, err
	}
	if req.Git && !inGit(abs) {
		if err := initRepo(abs, written, author); err != nil {
			undo()
			return Info{}, fmt.Errorf("start the git repository: %w", err)
		}
	}
	return l.Open(ctx, abs)
}

// Clone takes the project from a repository into a folder and opens it. A repository without
// a project gets one, pushed back with the first commit, so the clone and the repository agree.
func (l *Local) Clone(ctx context.Context, req CloneRequest, author string) (Info, error) {
	set := gitsync.Settings{URL: strings.TrimSpace(req.URL), Branch: strings.TrimSpace(req.Branch), Path: strings.TrimSpace(req.Folder)}
	if set.Branch == "" {
		set.Branch = "main"
	}
	token := strings.TrimSpace(req.Token)
	chk := (&gitsync.Runner{Logger: l.Logger}).Try(ctx, set, token)
	if !chk.OK {
		return Info{}, userErr(chk.Error)
	}
	create := !chk.HasProject && chk.Files == 0
	if create && !chk.CanPush {
		return Info{}, userErr("creating a project needs an access token with write access")
	}
	dir := req.Path
	if strings.TrimSpace(dir) == "" {
		if l.Root == "" {
			return Info{}, userErr("name a folder for the clone")
		}
		dir = filepath.Join(l.Root, Slug(gitsync.RepoName(set.URL)))
	}
	abs, err := l.abs(dir)
	if err != nil {
		return Info{}, err
	}
	if !emptyOrMissing(abs) {
		return Info{}, userErr(abs + " is not empty; choose another folder for the clone")
	}
	created := !exists(abs)
	undo := func() {
		if created {
			_ = os.RemoveAll(abs)
			return
		}
		entries, _ := os.ReadDir(abs)
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(abs, e.Name()))
		}
	}
	var auth *githttp.BasicAuth
	if token != "" {
		auth = &githttp.BasicAuth{Username: "kamerton", Password: token}
	}
	branch := plumbing.NewBranchReferenceName(set.Branch)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var repo *git.Repository
	if chk.Empty {
		repo, err = git.PlainInitWithOptions(abs, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: branch}})
		if err == nil {
			_, err = repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{set.URL}})
		}
	} else {
		opts := &git.CloneOptions{URL: set.URL, ReferenceName: branch, SingleBranch: true}
		if auth != nil {
			opts.Auth = auth
		}
		repo, err = git.PlainCloneContext(ctx, abs, false, opts)
	}
	if err != nil {
		undo()
		return Info{}, fmt.Errorf("clone %s: %w", set.URL, err)
	}
	root := filepath.Join(abs, filepath.FromSlash(strings.Trim(path.Clean("/"+set.Path), "/")))
	if create {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = gitsync.RepoName(set.URL)
		}
		if name, err = cleanName(name); err != nil {
			undo()
			return Info{}, err
		}
		written, err := writeStarter(abs, name, filepath.ToSlash(strings.TrimPrefix(root, abs)), true)
		if err == nil {
			err = commit(repo, written, startTitle(set.Path), author)
		}
		if err == nil {
			pushOpts := &git.PushOptions{RemoteName: "origin", RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec(branch + ":" + branch)}}
			if auth != nil {
				pushOpts.Auth = auth
			}
			if err = repo.PushContext(ctx, pushOpts); err != nil {
				err = fmt.Errorf("push the first commit to %s: %w; if the branch takes changes only by pull request, create the project in the repository first", set.Branch, err)
			}
		}
		if err != nil {
			undo()
			return Info{}, err
		}
	}
	in, err := l.Open(ctx, root)
	if err != nil {
		undo()
		return Info{}, err
	}
	return in, nil
}

func (l *Local) abs(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", userErr("name a folder")
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	return filepath.Abs(dir)
}

// writeStarter writes the files of a new project under root/sub and returns their paths
// relative to root, slash-separated. A README or .gitignore already there is kept.
func writeStarter(root, name, sub string, inGit bool) ([]string, error) {
	var written []string
	for rel, content := range gitsync.StarterFiles(name, strings.Trim(sub, "/"), inGit) {
		file := filepath.Join(root, filepath.FromSlash(rel))
		if exists(file) && path.Base(rel) != gitsync.ProjectFile {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(file, content, 0o644); err != nil {
			return nil, err
		}
		written = append(written, rel)
	}
	sort.Strings(written)
	return written, nil
}

func initRepo(dir string, files []string, author string) error {
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	if err != nil {
		return err
	}
	return commit(repo, files, startTitle(""), author)
}

func startTitle(folder string) string {
	folder = strings.Trim(path.Clean("/"+folder), "/")
	if folder == "" {
		return "Start a Kamerton project"
	}
	return "Start a Kamerton project in " + folder
}

func commit(repo *git.Repository, files []string, title, author string) error {
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	for _, f := range files {
		if _, err := wt.Add(f); err != nil {
			return err
		}
	}
	note := "Made in the Kamerton UI."
	if author = strings.Join(strings.Fields(author), " "); author != "" {
		note = "Made in the Kamerton UI by " + author + "."
	}
	_, err = wt.Commit(title+"\n\n"+note+"\n", &git.CommitOptions{
		Author: &object.Signature{Name: "Kamerton", Email: "kamerton@noreply.invalid", When: time.Now()},
	})
	return err
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func emptyOrMissing(dir string) bool {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	return err == nil && len(entries) == 0
}

func hasDescriptions(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
			return true
		}
	}
	return false
}

// inGit reports whether a folder is inside a git working copy.
func inGit(dir string) bool {
	_, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true})
	return err == nil
}

const maxRecent = 10

func (l *Local) recent() []Recent {
	if l.RecentFile == "" {
		return nil
	}
	raw, err := os.ReadFile(l.RecentFile)
	if err != nil {
		return nil
	}
	var list []Recent
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	return list
}

// remember puts a project first in the list of recent ones; a list that cannot be written is
// not worth failing the open for.
func (l *Local) remember(r Recent) {
	if l.RecentFile == "" {
		return
	}
	list := []Recent{r}
	for _, old := range l.recent() {
		if old.Path != r.Path && len(list) < maxRecent {
			list = append(list, old)
		}
	}
	raw, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(filepath.Dir(l.RecentFile), 0o755); err == nil {
		err = os.WriteFile(l.RecentFile, raw, 0o644)
		if err == nil {
			return
		}
	}
	l.log().Warn("the list of recent projects was not saved", "file", l.RecentFile)
}
