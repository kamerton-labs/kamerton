// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
)

// hosted serves a bare repository over smart HTTP the way a hosting service does: a token as
// the basic-auth password, pushes allowed.
type hosted struct {
	t    *testing.T
	bare string
	srv  *httptest.Server
}

func newHosted(t *testing.T, token string, files map[string]string) *hosted {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not installed")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not installed")
	}
	root := t.TempDir()
	h := &hosted{t: t, bare: filepath.Join(root, "obs.git")}
	work := filepath.Join(root, "work")
	if files == nil {
		// A repository as it is right after "New repository": no commits, no branch.
		h.git(root, "init", "-q", "--bare", "-b", "main", h.bare)
		h.git(h.bare, "config", "http.receivepack", "true")
		h.serve(backend, root, token)
		return h
	}
	h.git(root, "init", "-q", "-b", "main", work)
	for name, content := range files {
		full := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.git(work, "add", "-A")
	h.git(work, "commit", "-q", "-m", "first")
	h.git(root, "clone", "-q", "--bare", work, h.bare)
	h.git(h.bare, "config", "http.receivepack", "true")
	h.serve(backend, root, token)
	return h
}

func (h *hosted) serve(backend, root, token string) {
	cgiHandler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); token != "" && (!ok || pass != token) {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cgiHandler.ServeHTTP(w, r)
	}))
	h.t.Cleanup(h.srv.Close)
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

// api is a stand-in for the hosting service's HTTP API.
type api struct {
	mu    sync.Mutex
	calls []call
	srv   *httptest.Server
}

type call struct {
	Path, Auth string
	Body       map[string]any
}

func newAPI(t *testing.T) *api {
	a := &api{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The list of open requests: one of Kamerton's and one of somebody else's.
			_, _ = w.Write([]byte(`[{"title":"Change system shop","html_url":"https://example.com/obs/pull/7","head":{"ref":"kamerton/shop-0a1b2c3d"}},
				{"title":"Unrelated","html_url":"https://example.com/obs/pull/8","head":{"ref":"feature/x"}}]`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		c := call{Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(raw, &c.Body)
		a.mu.Lock()
		a.calls = append(a.calls, c)
		a.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url":"https://example.com/obs/pull/7"}`))
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *api) find(suffix string) []call {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []call
	for _, c := range a.calls {
		if strings.Contains(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

func token(v string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return v, nil }
}

func TestPrivateRepositoryNeedsTheToken(t *testing.T) {
	h := newHosted(t, "s3cret", map[string]string{"systems/shop.yaml": shop})
	store, c := cluster(t)
	s := &Service{URL: h.url(), Path: "systems", Target: store}

	if st := s.Sync(context.Background()); !strings.Contains(st.Error, "access denied") && !strings.Contains(st.Error, "not found") {
		t.Fatalf("without a token: %+v", st)
	}
	s.Token = token("wrong")
	if st := s.Sync(context.Background()); st.Error == "" || strings.Contains(st.Error, "wrong") {
		t.Fatalf("with a wrong token: %+v", st)
	}
	s.Token = token("s3cret\n")
	if st := s.Sync(context.Background()); st.Error != "" || get(t, c, "shop") == nil {
		t.Fatalf("with the token: %+v", st)
	}
	if raw, ok := s.Raw("systems/shop.yaml"); !ok || string(raw) != shop {
		t.Errorf("Raw = %q, %v", raw, ok)
	}
}

func TestSaveFromUIBecomesAPullRequest(t *testing.T) {
	h := newHosted(t, "tok", map[string]string{"systems/shop.yaml": "# our shop\n" + shop})
	fake := newAPI(t)
	store, c := cluster(t)
	s := &Service{
		URL: h.url(), Path: "systems", Target: store, Token: token("tok"),
		Write: WritePullRequest, Provider: ProviderGitHub, APIURL: fake.srv.URL, CommitStatus: true,
		GeneratedPath: "generated",
		Generate: func(raw []byte) (map[string][]byte, error) {
			return map[string][]byte{"rules.yaml": []byte("# rules of\n" + string(raw))}, nil
		},
	}
	store.Git = s
	first := s.Sync(context.Background())
	if first.Error != "" || first.Write != WritePullRequest {
		t.Fatalf("sync: %+v", first)
	}
	// The read commit is marked as applied.
	if st := fake.find("/statuses/" + first.Commit); len(st) != 1 || st[0].Body["state"] != "success" || st[0].Auth != "Bearer tok" {
		t.Fatalf("commit status: %+v", st)
	}

	changed := "# our shop\n" + strings.ReplaceAll(shop, "name: api", "name: gateway")
	// Saving keeps the change; nothing is pushed until someone says so.
	if _, _, err := store.Save(context.Background(), "shop", []byte(changed)); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Save = %v, want ErrStaged", err)
	}
	if out := h.git(h.bare, "branch", "--list", "kamerton/*"); out != "" {
		t.Fatalf("a save must not push: %s", out)
	}
	if saved, ok := s.Saved(context.Background(), "shop"); !ok || saved.Kind != KindChange || string(saved.Content) != changed {
		t.Fatalf("saved change: %+v %v", saved, ok)
	}
	p := *push(t, s, "")
	if list, _ := s.Changes(context.Background()); len(list) != 0 {
		t.Errorf("a push must clear the saved changes: %+v", list)
	}
	if p.Direct || !strings.HasPrefix(p.Branch, "kamerton/shop-") || p.URL != "https://example.com/obs/pull/7" {
		t.Fatalf("proposal = %+v", p)
	}
	// The cluster is untouched until the merge.
	if sd := get(t, c, "shop"); sd.Spec.Services[0].Name != "api" {
		t.Errorf("the cluster changed before the merge: %+v", sd.Spec.Services)
	}
	// The branch holds the file as written, comment included, and the generated rules.
	if got := h.git(h.bare, "show", p.Branch+":systems/shop.yaml"); got != strings.TrimSpace(changed) {
		t.Errorf("file on the branch:\n%s", got)
	}
	if got := h.git(h.bare, "show", p.Branch+":generated/shop/rules.yaml"); !strings.Contains(got, "name: gateway") {
		t.Errorf("generated file on the branch:\n%s", got)
	}
	if main := h.git(h.bare, "show", "main:systems/shop.yaml"); !strings.Contains(main, "name: api") {
		t.Error("the followed branch must not change")
	}
	pulls := fake.find("/pulls")
	if len(pulls) != 1 || pulls[0].Body["head"] != p.Branch || pulls[0].Body["base"] != "main" || pulls[0].Body["title"] != "Change system shop" {
		t.Fatalf("pull request: %+v", pulls)
	}

	// The open request is listed for the UI, with the system worked out from the branch.
	if st := s.Status(); len(st.Requests) != 1 || st.Requests[0].System != "shop" || st.Requests[0].URL != "https://example.com/obs/pull/7" {
		t.Errorf("open requests: %+v", st.Requests)
	}

	// Merged: the change arrives by itself.
	h.git(h.bare, "update-ref", "refs/heads/main", p.Commit)
	if st := s.Sync(context.Background()); st.Commit != p.Commit {
		t.Fatalf("after the merge: %+v", st)
	}
	if sd := get(t, c, "shop"); sd.Spec.Services[0].Name != "gateway" {
		t.Errorf("after the merge: %+v", sd.Spec.Services)
	}
}

func TestSaveFromUICommitsStraightToTheBranch(t *testing.T) {
	h := newHosted(t, "tok", map[string]string{"systems/shop.yaml": shop})
	store, c := cluster(t)
	s := &Service{URL: h.url(), Path: "systems", Target: store, Token: token("tok"), Write: WriteCommit}
	store.Git = s
	s.Sync(context.Background())

	if _, _, err := store.Save(context.Background(), "shop", []byte(strings.ReplaceAll(shop, "name: api", "name: gateway"))); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Save = %v", err)
	}
	if p := push(t, s, ""); !p.Direct || p.Branch != "main" {
		t.Fatalf("push = %+v", p)
	}
	if got := h.git(h.bare, "show", "main:systems/shop.yaml"); !strings.Contains(got, "name: gateway") {
		t.Errorf("main:\n%s", got)
	}
	s.Sync(context.Background())
	if sd := get(t, c, "shop"); sd.Spec.Services[0].Name != "gateway" {
		t.Errorf("after the commit: %+v", sd.Spec.Services)
	}

	// Deleting the system removes its file, and the system goes with it.
	if err := store.Delete(context.Background(), "shop"); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Delete = %v", err)
	}
	push(t, s, "")
	if out := h.git(h.bare, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, "shop.yaml") {
		t.Errorf("the file is still there: %s", out)
	}
	s.Sync(context.Background())
	if get(t, c, "shop") != nil {
		t.Error("the system is still there")
	}
}

func TestNewSystemBecomesANewFile(t *testing.T) {
	h := newHosted(t, "tok", map[string]string{"systems/shop.yaml": shop})
	store, c := cluster(t)
	s := &Service{URL: h.url(), Path: "systems", Target: store, Token: token("tok"), Write: WriteCommit}
	store.Git = s
	s.Sync(context.Background())

	billing := strings.ReplaceAll(shop, "name: shop", "name: billing")
	if _, _, err := store.Create(context.Background(), []byte(billing)); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Create = %v, want ErrStaged", err)
	}
	// Two saved changes go out together, in one commit with one message.
	if _, _, err := store.Save(context.Background(), "shop", []byte(strings.ReplaceAll(shop, "name: api", "name: gateway"))); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Save = %v", err)
	}
	before := h.git(h.bare, "rev-list", "--count", "main")
	push(t, s, "Add system billing")
	if after := h.git(h.bare, "rev-list", "--count", "main"); after == before || h.git(h.bare, "rev-list", "--count", "main") != "2" {
		t.Errorf("two changes must be one commit: %s -> %s", before, after)
	}
	if got := h.git(h.bare, "show", "main:systems/shop.yaml"); !strings.Contains(got, "name: gateway") {
		t.Errorf("the second change is not in the commit:\n%s", got)
	}
	if got := h.git(h.bare, "show", "main:systems/billing.yaml"); got != strings.TrimSpace(billing) {
		t.Errorf("new file:\n%s", got)
	}
	if msg := h.git(h.bare, "log", "-1", "--format=%s", "main"); msg != "Add system billing" {
		t.Errorf("commit message = %q", msg)
	}
	s.Sync(context.Background())
	sd := get(t, c, "billing")
	if sd == nil || descstore.FromGit(sd) == nil || descstore.FromGit(sd).Path != "systems/billing.yaml" {
		t.Fatalf("the new system did not come back from git: %+v", sd)
	}

	// A name that is taken, in the cluster or in the repository, is refused.
	if _, _, err := store.Create(context.Background(), []byte(shop)); !errors.Is(err, descstore.ErrExists) {
		t.Errorf("Create of an existing system = %v, want ErrExists", err)
	}
}

func TestMoveToGitTakesASystemOverInPlace(t *testing.T) {
	h := newHosted(t, "tok", map[string]string{"systems/shop.yaml": shop})
	legacy := &v1alpha1.SystemDescription{}
	legacy.Name, legacy.Namespace = "legacy", "kamerton"
	legacy.Spec.Services = []v1alpha1.Service{{Name: "made-by-hand", Type: v1alpha1.ServiceTypeHTTP, Critical: true}}
	store, c := cluster(t, legacy)
	s := &Service{URL: h.url(), Path: "systems", Target: store, Token: token("tok"), Write: WritePullRequest}
	store.Git = s
	s.Sync(context.Background())

	if err := store.MoveToGit(context.Background(), "legacy"); err != nil {
		t.Fatalf("MoveToGit = %v", err)
	}
	if saved, ok := s.Saved(context.Background(), "legacy"); !ok || saved.Kind != KindMove {
		t.Fatalf("saved: %+v %v", saved, ok)
	}
	p := push(t, s, "")
	if p.Direct || !strings.HasPrefix(p.Branch, "kamerton/legacy-") {
		t.Fatalf("push = %+v", p)
	}
	if got := h.git(h.bare, "show", p.Branch+":systems/legacy.yaml"); !strings.Contains(got, "made-by-hand") || strings.Contains(got, "git-adopt") {
		t.Errorf("file on the branch:\n%s", got)
	}
	// Until the merge the system works as before and still belongs to nobody in git.
	s.Sync(context.Background())
	if sd := get(t, c, "legacy"); sd == nil || descstore.FromGit(sd) != nil {
		t.Fatalf("before the merge: %+v", sd)
	}

	h.git(h.bare, "update-ref", "refs/heads/main", p.Commit)
	st := s.Sync(context.Background())
	if states(st)["systems/legacy.yaml"] != StateApplied {
		t.Fatalf("after the merge: %+v", st)
	}
	sd := get(t, c, "legacy")
	if from := descstore.FromGit(sd); from == nil || from.Path != "systems/legacy.yaml" || sd.Annotations[descstore.AnnotationGitAdopt] != "" {
		t.Errorf("taken over: %+v annotations=%v", from, sd.Annotations)
	}
	if sd.Spec.Services[0].Name != "made-by-hand" {
		t.Errorf("the description changed on the way: %+v", sd.Spec.Services)
	}
	if err := store.MoveToGit(context.Background(), "legacy"); err == nil {
		t.Error("moving a system that is already in git must be refused")
	}
}

func TestStartingFromAnEmptyRepository(t *testing.T) {
	h := newHosted(t, "tok", nil)
	store, c := cluster(t)
	s := &Service{
		URL: h.url(), Target: store, Token: token("tok"), Write: WritePullRequest,
		Generate: func(raw []byte) (map[string][]byte, error) {
			return map[string][]byte{"rules/shop.rules.yaml": []byte("# rules\n")}, nil
		},
	}
	store.Git = s

	// An empty repository is a state, not an error.
	st := s.Sync(context.Background())
	if st.Error != "" || !st.Empty || st.Commit != "" {
		t.Fatalf("empty repository: %+v", st)
	}

	// The first system made in the UI starts the branch, even in pull-request mode: there is
	// nothing to open a request into yet.
	if _, _, err := store.Create(context.Background(), []byte(shop)); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Create = %v, want ErrStaged", err)
	}
	if p := push(t, s, ""); !p.Direct || p.Branch != "main" {
		t.Fatalf("push = %+v, want a first commit on main", p)
	}
	if got := h.git(h.bare, "show", "main:systems/shop.yaml"); got != strings.TrimSpace(shop) {
		t.Errorf("first file:\n%s", got)
	}
	// The new repository describes itself: a project file, a README and a .gitignore.
	tree := h.git(h.bare, "ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"kamerton.yaml", "README.md", ".gitignore", "systems/shop.yaml", "generated/shop/rules/shop.rules.yaml"} {
		if !strings.Contains(tree, want) {
			t.Errorf("the first commit lacks %s:\n%s", want, tree)
		}
	}
	if manifest := h.git(h.bare, "show", "main:kamerton.yaml"); !strings.Contains(manifest, "kind: Project") || !strings.Contains(manifest, "name: obs") {
		t.Errorf("project file:\n%s", manifest)
	}
	st = s.Sync(context.Background())
	if st.Empty || st.Error != "" || get(t, c, "shop") == nil {
		t.Fatalf("after the first commit: %+v", st)
	}
	if !st.HasProjectFile || st.Project != "obs" || st.SystemsPath != "systems" || st.GeneratedPath != "generated" {
		t.Errorf("layout from the project file: %+v", st)
	}
	if from := descstore.FromGit(get(t, c, "shop")); from.Path != "systems/shop.yaml" {
		t.Errorf("path = %q", from.Path)
	}
}

func TestProjectFileSetsTheLayout(t *testing.T) {
	billing := strings.ReplaceAll(shop, "name: shop", "name: billing")
	draft := strings.ReplaceAll(shop, "name: shop", "name: draft")
	h := newHosted(t, "tok", map[string]string{
		"obs/kamerton.yaml":                    "apiVersion: kamerton.dev/v1alpha1\nkind: Project\nmetadata:\n  name: payments\nspec:\n  systems: descriptions\n  generated: \"\"\n  ignore: [descriptions/drafts]\n",
		"obs/descriptions/shop.yaml":           shop,
		"obs/descriptions/team-a/billing.yaml": billing,
		"obs/descriptions/drafts/draft.yaml":   draft,
		"elsewhere/outside.yaml":               strings.ReplaceAll(shop, "name: shop", "name: outside"),
	})
	store, c := cluster(t)
	s := &Service{
		URL: h.url(), Path: "obs", Target: store, Token: token("tok"), Write: WriteCommit, GeneratedPath: "from-settings",
		Generate: func([]byte) (map[string][]byte, error) { return map[string][]byte{"rules.yaml": []byte("x")}, nil },
	}
	store.Git = s
	st := s.Sync(context.Background())
	if st.Error != "" || st.Project != "payments" || st.SystemsPath != "obs/descriptions" || st.GeneratedPath != "" || len(st.Files) != 2 {
		t.Fatalf("status: %+v", st)
	}
	if get(t, c, "shop") == nil || get(t, c, "billing") == nil || get(t, c, "draft") != nil || get(t, c, "outside") != nil {
		t.Error("systems must come from the project's folder, subfolders included, without the ignored ones")
	}
	// A new system goes where the project file says, and no generated files are written: the
	// project file turned them off, whatever the installation's settings say.
	warehouse := strings.ReplaceAll(shop, "name: shop", "name: warehouse")
	if _, _, err := store.Create(context.Background(), []byte(warehouse)); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Create = %v", err)
	}
	push(t, s, "")
	tree := h.git(h.bare, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, "obs/descriptions/warehouse.yaml") || strings.Contains(tree, "from-settings") || strings.Contains(tree, "rules.yaml") {
		t.Errorf("tree after a new system:\n%s", tree)
	}
}

func TestBrokenProjectFileChangesNothing(t *testing.T) {
	h := newHosted(t, "tok", map[string]string{"kamerton.yaml": "kind: Project\nspec:\n  sistems: x\n", "systems/shop.yaml": shop})
	store, c := cluster(t)
	s := &Service{URL: h.url(), Target: store, Token: token("tok")}
	if st := s.Sync(context.Background()); !strings.Contains(st.Error, "kamerton.yaml") || get(t, c, "shop") != nil {
		t.Fatalf("a mistyped field in the project file must be an error: %+v", st)
	}
}

func TestFolderThatIsNotThereYet(t *testing.T) {
	// The repository exists, with a README, but nobody made the folder yet.
	h := newHosted(t, "tok", map[string]string{"README.md": "observability"})
	store, c := cluster(t)
	s := &Service{URL: h.url(), Path: "systems", Target: store, Token: token("tok"), Write: WriteCommit}
	store.Git = s
	if st := s.Sync(context.Background()); st.Error != "" || len(st.Files) != 0 {
		t.Fatalf("a folder yet to be made is not an error: %+v", st)
	}
	if _, _, err := store.Create(context.Background(), []byte(shop)); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Create = %v", err)
	}
	push(t, s, "")
	s.Sync(context.Background())
	if get(t, c, "shop") == nil {
		t.Fatal("the first system did not come back")
	}
	// Now a typo in the folder is an error again, and the system stays.
	s.Path = "sistems"
	h.git(h.bare, "update-ref", "refs/heads/keep", "main")
	s.mu.Lock()
	s.status.Commit = ""
	s.mu.Unlock()
	if st := s.Sync(context.Background()); !strings.Contains(st.Error, "not found") || get(t, c, "shop") == nil {
		t.Fatalf("a typo after systems exist: %+v", st)
	}
}

func push(t *testing.T, s *Service, message string) *descstore.GitProposal {
	t.Helper()
	p, err := s.Push(context.Background(), nil, message, "")
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	return p
}

func TestSavedChangesCanBeDiscardedAndUndone(t *testing.T) {
	h := newHosted(t, "tok", map[string]string{"systems/shop.yaml": shop})
	store, _ := cluster(t)
	s := &Service{URL: h.url(), Path: "systems", Target: store, Token: token("tok"), Write: WriteCommit}
	store.Git = s
	s.Sync(context.Background())
	ctx := context.Background()

	changed := strings.ReplaceAll(shop, "name: api", "name: gateway")
	_, _, _ = store.Save(ctx, "shop", []byte(changed))
	// Saving the repository's own content again leaves nothing to push.
	_, _, _ = store.Save(ctx, "shop", []byte(shop))
	if list, _ := s.Changes(ctx); len(list) != 0 {
		t.Fatalf("back to the repository's content: %+v", list)
	}
	_, _, _ = store.Save(ctx, "shop", []byte(changed))
	if err := s.Discard(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx, nil, "", ""); err == nil {
		t.Error("nothing is left to push after a discard")
	}
	// A new system that was never pushed is forgotten when deleted.
	billing := strings.ReplaceAll(shop, "name: shop", "name: billing")
	_, _, _ = store.Create(ctx, []byte(billing))
	_ = s.Stage(ctx, descstore.GitChange{System: "billing"})
	if list, _ := s.Changes(ctx); len(list) != 0 {
		t.Errorf("a deleted unpushed system: %+v", list)
	}
}

func TestCommitsListsTheBranch(t *testing.T) {
	h := newHosted(t, "tok", map[string]string{"systems/shop.yaml": shop})
	store, _ := cluster(t)
	s := &Service{URL: h.url(), Path: "systems", Target: store, Token: token("tok"), Write: WriteCommit}
	store.Git = s
	s.Sync(context.Background())
	_, _, _ = store.Save(context.Background(), "shop", []byte(strings.ReplaceAll(shop, "name: api", "name: gateway")))
	push(t, s, "shop: rename the service")
	st := s.Sync(context.Background())

	list, err := s.Commits(context.Background(), 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("commits: %+v, %v", list, err)
	}
	if c := list[0]; c.Title != "shop: rename the service" || c.Author != "Kamerton" || !c.Applied || c.Hash != st.Commit || len(c.Systems) != 1 || c.Systems[0] != "shop" {
		t.Errorf("newest commit: %+v", c)
	}
	if list[1].Title != "first" || list[1].Applied {
		t.Errorf("oldest commit: %+v", list[1])
	}
}

func TestReadOnlyRepositoryRefusesASave(t *testing.T) {
	h := newHosted(t, "", map[string]string{"systems/shop.yaml": shop})
	store, _ := cluster(t)
	s := &Service{URL: h.url(), Path: "systems", Target: store}
	store.Git = s
	s.Sync(context.Background())
	if _, _, err := store.Save(context.Background(), "shop", []byte(shop)); !errors.Is(err, descstore.ErrFromGit) {
		t.Errorf("Save = %v, want ErrFromGit", err)
	}
}

func TestHostFromURL(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/team/obs.git":      "github https://api.github.com team/obs",
		"https://gitlab.com/group/sub/obs.git": "gitlab https://gitlab.com/api/v4 group/sub/obs",
		"https://gitlab.example.com/team/obs":  "gitlab https://gitlab.example.com/api/v4 team/obs",
		"https://git.example.com/team/obs.git": "",
		"git://192.168.5.2:9418/obs.git":       "",
	} {
		got := ""
		if h := (&Service{URL: url}).host(); h != nil {
			got = h.provider + " " + h.api + " " + h.repo
		}
		if got != want {
			t.Errorf("host(%s) = %q, want %q", url, got, want)
		}
	}
	if h := (&Service{URL: "https://git.example.com/team/obs.git", Provider: "github"}).host(); h == nil || h.api != "https://git.example.com/api/v3" {
		t.Errorf("self-hosted GitHub: %+v", h)
	}
}

type memSettings struct{ s *Settings }

func (m *memSettings) Load(context.Context) (*Settings, error) { return m.s, nil }
func (m *memSettings) Save(_ context.Context, s *Settings) error {
	m.s = s
	return nil
}

type memToken struct{ v string }

func (m *memToken) Get(context.Context) (string, error) { return m.v, nil }
func (m *memToken) Set(_ context.Context, v string) error {
	m.v = v
	return nil
}
func (m *memToken) Delete(context.Context) error {
	m.v = ""
	return nil
}

func TestRepositoryIsConnectedChangedAndDisconnectedWhileRunning(t *testing.T) {
	h := newHosted(t, "tok", map[string]string{"systems/shop.yaml": shop})
	store, c := cluster(t)
	tokens := &memToken{}
	r := &Runner{Base: Service{Target: store}, Store: &memSettings{}, Tokens: tokens, Release: store.ReleaseGit}
	store.Git = r
	ctx := context.Background()

	if r.Current(ctx) != nil || r.View(ctx).Connected || r.WriteEnabled() {
		t.Fatal("nothing is connected at first")
	}
	// Trying a repository changes nothing and says what is wrong, or what is there.
	if c := r.Try(ctx, Settings{URL: h.url(), Path: "systems"}, "wrong"); c.OK || c.Error == "" {
		t.Errorf("a wrong token: %+v", c)
	}
	if c := r.Try(ctx, Settings{URL: "ftp://x"}, ""); c.OK {
		t.Errorf("a bad address: %+v", c)
	}
	if c := r.Try(ctx, Settings{URL: h.url(), Path: "systems"}, "tok"); !c.OK || c.Files != 1 {
		t.Errorf("a good repository: %+v", c)
	}
	if tokens.v != "" || r.Current(ctx) != nil {
		t.Fatal("a try must not store anything")
	}

	if err := r.Connect(ctx, Settings{URL: h.url(), Path: "systems", Write: "commit"}, "tok"); err != nil {
		t.Fatal(err)
	}
	s := r.Current(ctx)
	s.Sync(ctx)
	if v := r.View(ctx); !v.Connected || !v.HasToken || v.Write != WriteCommit || v.Locked || get(t, c, "shop") == nil {
		t.Fatalf("after connecting: %+v", v)
	}
	if _, _, err := store.Save(ctx, "shop", []byte(strings.ReplaceAll(shop, "name: api", "name: gateway"))); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("a save goes through the connected repository: %v", err)
	}

	// Disconnecting deletes nothing: the system stays and becomes an ordinary, editable one.
	if err := r.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	sd := get(t, c, "shop")
	if sd == nil || descstore.FromGit(sd) != nil || tokens.v != "" || r.Current(ctx) != nil {
		t.Fatalf("after disconnecting: %+v token=%q", sd, tokens.v)
	}
	if _, _, err := store.Save(ctx, "shop", []byte(shop)); err != nil {
		t.Errorf("the released system must be editable: %v", err)
	}
}

func TestRepositoryFromTheInstallationIsLocked(t *testing.T) {
	r := &Runner{Locked: &Settings{URL: "https://example.com/team/obs.git", Write: "pullRequest"}, Store: &memSettings{}}
	ctx := context.Background()
	if v := r.View(ctx); !v.Locked || !v.Connected || v.URL != "https://example.com/team/obs.git" {
		t.Fatalf("view: %+v", v)
	}
	if err := r.Connect(ctx, Settings{URL: "https://example.com/other.git"}, ""); err == nil {
		t.Error("a locked repository cannot be changed from the UI")
	}
	if err := r.Disconnect(ctx); err == nil {
		t.Error("a locked repository cannot be disconnected from the UI")
	}
}

func TestCreateAProjectInARepositoryThatHasOtherThings(t *testing.T) {
	// A team's existing repository: code, a README of its own.
	h := newHosted(t, "tok", map[string]string{"README.md": "our service", "src/main.go": "package main"})
	store, c := cluster(t)
	r := &Runner{Base: Service{Target: store}, Store: &memSettings{}, Tokens: &memToken{}, Release: store.ReleaseGit}
	store.Git = r
	ctx := context.Background()

	if chk := r.Try(ctx, Settings{URL: h.url(), Path: "observability"}, "tok"); !chk.OK || chk.HasProject || chk.Files != 0 {
		t.Fatalf("before: %+v", chk)
	}
	if _, err := r.Create(ctx, Settings{URL: h.url(), Path: "observability"}, "tok", ""); err == nil {
		t.Error("creating without push must be refused")
	}
	p, err := r.Create(ctx, Settings{URL: h.url(), Path: "observability", Write: "commit"}, "tok", "anna")
	if err != nil || !p.Direct {
		t.Fatalf("Create = %+v, %v", p, err)
	}
	tree := h.git(h.bare, "ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"observability/kamerton.yaml", "observability/README.md", "observability/.gitignore", "README.md", "src/main.go"} {
		if !strings.Contains(tree, want) {
			t.Errorf("tree lacks %s:\n%s", want, tree)
		}
	}
	// The team's own README is untouched, and the commit names who started the project.
	if got := h.git(h.bare, "show", "main:README.md"); got != "our service" {
		t.Errorf("the repository's README changed: %q", got)
	}
	if msg := h.git(h.bare, "log", "-1", "--format=%B", "main"); !strings.Contains(msg, "Start a Kamerton project in observability") || !strings.Contains(msg, "by anna") {
		t.Errorf("commit message: %q", msg)
	}
	if chk := r.Try(ctx, Settings{URL: h.url(), Path: "observability"}, "tok"); !chk.HasProject || chk.Project != "obs" {
		t.Errorf("after: %+v", chk)
	}
	// The first system lands in the project's own folder.
	s := r.Current(ctx)
	s.Sync(ctx)
	if _, _, err := store.Create(ctx, []byte(shop)); !errors.Is(err, descstore.ErrStaged) {
		t.Fatalf("Create system = %v", err)
	}
	push(t, s, "")
	s.Sync(ctx)
	if from := descstore.FromGit(get(t, c, "shop")); from == nil || from.Path != "observability/systems/shop.yaml" {
		t.Errorf("first system: %+v", from)
	}
	// A second project in the same folder is refused.
	if _, err := s.InitProject(ctx, ""); err == nil || !strings.Contains(err.Error(), "already holds a project") {
		t.Errorf("a second InitProject = %v", err)
	}
}
