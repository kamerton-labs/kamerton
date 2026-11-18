// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package project

import (
	"context"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/gitsync"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

const shop = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
spec:
  services:
    - name: api
      type: http
      critical: true
`

func newLocal(t *testing.T) *Local {
	t.Helper()
	dir := t.TempDir()
	return &Local{Root: filepath.Join(dir, "kamerton"), RecentFile: filepath.Join(dir, "config", "projects.json"), Descriptions: &descstore.Switch{}, Stacks: &stack.SwitchStateStore{}}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Shop": "shop", "Мой магазин": "мой-магазин", "  a / b  ": "a-b", "!!!": "project", "obs.v2": "obs.v2"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestANewLocalProjectIsAFolderThatOpensAtOnce(t *testing.T) {
	l := newLocal(t)
	ctx := context.Background()
	if in := l.Info(ctx); in.Exists || in.Where != WhereLocal || in.DefaultRoot != l.Root || !in.CanClone {
		t.Fatalf("before: %+v", in)
	}
	if _, err := l.New(ctx, NewRequest{Name: "  "}, ""); !errors.Is(err, ErrNoName) {
		t.Errorf("no name: %v", err)
	}
	in, err := l.New(ctx, NewRequest{Name: "Мой магазин", Git: true}, "anna")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Root, "мой-магазин")
	if !in.Exists || in.Name != "Мой магазин" || in.Path != dir || !in.Git {
		t.Fatalf("after: %+v", in)
	}
	if p := read(t, filepath.Join(dir, "kamerton.yaml")); !strings.Contains(p, `name: "Мой магазин"`) {
		t.Errorf("project file:\n%s", p)
	}
	if !strings.Contains(read(t, filepath.Join(dir, "README.md")), "reads the systems from this folder") {
		t.Error("a local project's README speaks of the folder")
	}
	// The project is in use: a system saved in the UI lands in its systems folder.
	if _, _, err := l.Descriptions.Create(ctx, []byte(shop)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "systems", "shop.yaml")); err != nil {
		t.Errorf("the system is not in the project's folder: %v", err)
	}
	// "Start a git repository" made the first commit.
	if out, err := exec.Command("git", "-C", dir, "log", "--format=%s %b").Output(); err == nil && !strings.Contains(string(out), "Start a Kamerton project") {
		t.Errorf("git log: %s", out)
	}
	// A second project in the same folder is refused.
	if _, err := l.New(ctx, NewRequest{Name: "x", Path: dir}, ""); !errors.Is(err, ErrExists) {
		t.Errorf("a second project in the folder: %v", err)
	}
}

func TestProjectsAreOpenedByFolderAndRemembered(t *testing.T) {
	l := newLocal(t)
	ctx := context.Background()
	a, err := l.New(ctx, NewRequest{Name: "a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Git {
		t.Error("git was not asked for")
	}
	if _, err := l.New(ctx, NewRequest{Name: "b", Path: filepath.Join(t.TempDir(), "elsewhere")}, ""); err != nil {
		t.Fatal(err)
	}
	// The other project is offered again, the open one is not.
	in := l.Info(ctx)
	if in.Name != "b" || len(in.Recent) != 1 || in.Recent[0].Path != a.Path {
		t.Fatalf("recent: %+v", in)
	}
	if in, err := l.Open(ctx, a.Path); err != nil || in.Name != "a" {
		t.Fatalf("Open = %+v, %v", in, err)
	}
	// After a restart the project opened last opens by itself.
	again := &Local{Root: l.Root, RecentFile: l.RecentFile, Descriptions: &descstore.Switch{}, Stacks: &stack.SwitchStateStore{}}
	if !again.OpenLast(ctx) || again.Info(ctx).Name != "a" {
		t.Errorf("after a restart: %+v", again.Info(ctx))
	}
	// A project closed by hand stays closed after a restart, and is still offered.
	again.Close(ctx)
	third := &Local{Root: l.Root, RecentFile: l.RecentFile, Descriptions: &descstore.Switch{}, Stacks: &stack.SwitchStateStore{}}
	if third.OpenLast(ctx) || third.Info(ctx).Exists || len(third.Info(ctx).Recent) == 0 || third.Info(ctx).Recent[0].Closed {
		t.Errorf("after closing and a restart: %+v", third.Info(ctx))
	}
	// A folder of description files from before project files is a project too.
	old := t.TempDir()
	if err := os.WriteFile(filepath.Join(old, "shop.yaml"), []byte(shop), 0o600); err != nil {
		t.Fatal(err)
	}
	if in, err := l.Open(ctx, old); err != nil || in.Name != filepath.Base(old) {
		t.Fatalf("an old folder: %+v, %v", in, err)
	}
	if list, _ := l.Descriptions.List(ctx); len(list) != 1 {
		t.Errorf("systems of the old folder: %d", len(list))
	}
	// An empty folder and a missing one are not.
	var ue *UserError
	if _, err := l.Open(ctx, t.TempDir()); !errors.As(err, &ue) {
		t.Errorf("an empty folder: %v", err)
	}
	if _, err := l.Open(ctx, filepath.Join(old, "nope")); !errors.As(err, &ue) {
		t.Errorf("a missing folder: %v", err)
	}
}

type hosted struct {
	t    *testing.T
	root string
	srv  *httptest.Server
}

// newHosted serves a bare repository over HTTP with a token, empty or with files.
func newHosted(t *testing.T, files map[string]string) *hosted {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not installed")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not installed")
	}
	h := &hosted{t: t, root: t.TempDir()}
	bare := filepath.Join(h.root, "obs.git")
	if files == nil {
		h.git(h.root, "init", "-q", "--bare", "-b", "main", bare)
	} else {
		work := filepath.Join(h.root, "work")
		h.git(h.root, "init", "-q", "-b", "main", work)
		for name, content := range files {
			full := filepath.Join(work, name)
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		h.git(work, "add", "-A")
		h.git(work, "commit", "-q", "-m", "first")
		h.git(h.root, "clone", "-q", "--bare", work, bare)
	}
	h.git(bare, "config", "http.receivepack", "true")
	cgiHandler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + h.root, "GIT_HTTP_EXPORT_ALL=1"}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); !ok || pass != "tok" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cgiHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hosted) url() string { return h.srv.URL + "/obs.git" }

func (h *hosted) git(dir string, args ...string) string {
	h.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCloneAProjectFromGit(t *testing.T) {
	h := newHosted(t, map[string]string{"kamerton.yaml": "apiVersion: kamerton.dev/v1alpha1\nkind: Project\nmetadata:\n  name: obs\n", "systems/shop.yaml": shop})
	l := newLocal(t)
	ctx := context.Background()
	in, err := l.Clone(ctx, CloneRequest{URL: h.url(), Token: "tok"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "obs" || in.Path != filepath.Join(l.Root, "obs") || !in.Git {
		t.Fatalf("clone: %+v", in)
	}
	if list, _ := l.Descriptions.List(ctx); len(list) != 1 {
		t.Errorf("systems of the clone: %d", len(list))
	}
	// The token is used, not kept in the clone's remote.
	if remote := h.git(in.Path, "remote", "get-url", "origin"); strings.Contains(remote, "tok") {
		t.Errorf("remote carries the token: %s", remote)
	}
	// A folder that is not empty is not cloned into.
	if _, err := l.Clone(ctx, CloneRequest{URL: h.url(), Token: "tok", Path: in.Path}, ""); err == nil {
		t.Error("cloning into a full folder must fail")
	}
}

func TestCloneAnEmptyRepositoryStartsTheProjectThere(t *testing.T) {
	h := newHosted(t, nil)
	l := newLocal(t)
	ctx := context.Background()
	dest := filepath.Join(t.TempDir(), "clone")
	var ue *UserError
	if _, err := l.Clone(ctx, CloneRequest{URL: h.url(), Path: dest}, ""); !errors.As(err, &ue) {
		t.Fatalf("without a token: %v", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a failed clone leaves its folder behind")
	}
	in, err := l.Clone(ctx, CloneRequest{URL: h.url(), Token: "tok", Path: dest, Folder: "observability", Name: "Shop"}, "anna")
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "Shop" || in.Path != filepath.Join(dest, "observability") {
		t.Fatalf("clone: %+v", in)
	}
	// The first commit reached the repository.
	tree := h.git(filepath.Join(h.root, "obs.git"), "ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"observability/kamerton.yaml", "observability/README.md", "observability/systems/.gitkeep"} {
		if !strings.Contains(tree, want) {
			t.Errorf("repository lacks %s:\n%s", want, tree)
		}
	}
	if msg := h.git(filepath.Join(h.root, "obs.git"), "log", "-1", "--format=%B", "main"); !strings.Contains(msg, "in observability") || !strings.Contains(msg, "by anna") {
		t.Errorf("commit: %s", msg)
	}
}

type memNames struct{ v string }

func (m *memNames) Get(context.Context) (string, error) { return m.v, nil }
func (m *memNames) Set(_ context.Context, v string) error {
	m.v = v
	return nil
}

type listOnly struct {
	descstore.Store
	n int
}

func (s listOnly) List(context.Context) ([]*descstore.Snapshot, error) {
	return make([]*descstore.Snapshot, s.n), nil
}

func TestClusterProject(t *testing.T) {
	ctx := context.Background()
	names := &memNames{}
	c := &Cluster{Names: names, Systems: listOnly{}, Namespace: "monitoring"}
	if in := c.Info(ctx); in.Exists || in.Where != WhereCluster || in.Namespace != "monitoring" {
		t.Fatalf("a new installation: %+v", in)
	}
	in, err := c.New(ctx, NewRequest{Name: "Shop"}, "")
	if err != nil || !in.Exists || in.Name != "Shop" {
		t.Fatalf("New = %+v, %v", in, err)
	}
	if _, err := c.New(ctx, NewRequest{Name: "Other"}, ""); !errors.Is(err, ErrExists) {
		t.Errorf("a second project: %v", err)
	}
	// A project taken from a repository keeps its name; a named one is not renamed.
	c.Adopt(ctx, "Other")
	if names.v != "Shop" {
		t.Errorf("Adopt renamed the project: %q", names.v)
	}
	fresh := &memNames{}
	(&Cluster{Names: fresh}).Adopt(ctx, "obs")
	if fresh.v != "obs" {
		t.Errorf("Adopt: %q", fresh.v)
	}

	// An installation from before projects, with systems, is never sent to the first screen.
	old := &Cluster{Names: &memNames{}, Systems: listOnly{n: 2}}
	if !old.Info(ctx).Exists {
		t.Error("an installation with systems is a project")
	}
}

func TestRenameKeepsTheProjectFileAsItIs(t *testing.T) {
	l := newLocal(t)
	ctx := context.Background()
	if _, err := l.Rename(ctx, "x"); !errors.Is(err, ErrNotOpen) {
		t.Errorf("nothing open: %v", err)
	}
	in, err := l.New(ctx, NewRequest{Name: "Shop"}, "")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(in.Path, "kamerton.yaml")
	before := read(t, file)
	if in, err = l.Rename(ctx, "Магазин: склад"); err != nil || in.Name != "Магазин: склад" {
		t.Fatalf("Rename = %+v, %v", in, err)
	}
	after := read(t, file)
	if want := strings.Replace(before, `name: "Shop"`, `name: "Магазин: склад"`, 1); after != want {
		t.Errorf("only the name may change:\n%s", after)
	}
	// The new name is what the folder opens with, and what the recent list shows.
	if in, _ := l.Open(ctx, in.Path); in.Name != "Магазин: склад" {
		t.Errorf("reopened: %q", in.Name)
	}
	if r := l.recent(); len(r) == 0 || r[0].Name != "Магазин: склад" {
		t.Errorf("recent: %+v", r)
	}
}

func TestSetProjectName(t *testing.T) {
	for in, want := range map[string]string{
		"kind: Project\nmetadata:\n  # who\n  name: a\nspec: {}\n": "kind: Project\nmetadata:\n  # who\n  name: \"b\"\nspec: {}\n",
		"kind: Project\nmetadata:\n  labels: {}\nspec: {}\n":       "kind: Project\nmetadata:\n  name: \"b\"\n  labels: {}\nspec: {}\n",
	} {
		got, err := setProjectName([]byte(in), "b")
		if err != nil || string(got) != want {
			t.Errorf("setProjectName(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := setProjectName([]byte("kind: Project\n"), "b"); err == nil {
		t.Error("a file without metadata")
	}
}

func TestRenameInTheCluster(t *testing.T) {
	ctx := context.Background()
	names := &memNames{v: "Shop"}
	c := &Cluster{Names: names, Systems: listOnly{}}
	if in, err := c.Rename(ctx, "Магазин"); err != nil || in.Name != "Магазин" || names.v != "Магазин" {
		t.Fatalf("Rename = %+v, %v", in, err)
	}
	if _, err := (&Cluster{Names: &memNames{}, Systems: listOnly{}}).Rename(ctx, "x"); !errors.Is(err, ErrNotOpen) {
		t.Errorf("no project: %v", err)
	}
	// With a repository the name is its project file's; the cluster does not keep a second one.
	withGit := &Cluster{Names: names, Systems: listOnly{}, Git: &gitsync.Runner{Locked: &gitsync.Settings{URL: "https://example.com/team/obs.git"}}}
	var ue *UserError
	if _, err := withGit.Rename(ctx, "y"); !errors.As(err, &ue) || names.v != "Магазин" {
		t.Errorf("with git: %v, name %q", err, names.v)
	}
}
