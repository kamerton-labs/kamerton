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
	_, _, err := store.Save(context.Background(), "shop", []byte(changed))
	var proposed *descstore.ProposedError
	if !errors.As(err, &proposed) {
		t.Fatalf("Save = %v, want a proposal", err)
	}
	p := proposed.Proposal
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

	_, _, err := store.Save(context.Background(), "shop", []byte(strings.ReplaceAll(shop, "name: api", "name: gateway")))
	var proposed *descstore.ProposedError
	if !errors.As(err, &proposed) || !proposed.Proposal.Direct || proposed.Proposal.Branch != "main" {
		t.Fatalf("Save = %v", err)
	}
	if got := h.git(h.bare, "show", "main:systems/shop.yaml"); !strings.Contains(got, "name: gateway") {
		t.Errorf("main:\n%s", got)
	}
	s.Sync(context.Background())
	if sd := get(t, c, "shop"); sd.Spec.Services[0].Name != "gateway" {
		t.Errorf("after the commit: %+v", sd.Spec.Services)
	}

	// Deleting the system removes its file, and the system goes with it.
	err = store.Delete(context.Background(), "shop")
	if !errors.As(err, &proposed) {
		t.Fatalf("Delete = %v", err)
	}
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
	_, _, err := store.Create(context.Background(), []byte(billing))
	var proposed *descstore.ProposedError
	if !errors.As(err, &proposed) || !proposed.Proposal.Direct {
		t.Fatalf("Create = %v, want a proposal", err)
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

	p, err := store.MoveToGit(context.Background(), "legacy")
	if err != nil || p.Direct || !strings.HasPrefix(p.Branch, "kamerton/legacy-") {
		t.Fatalf("MoveToGit = %+v, %v", p, err)
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
	if _, err := store.MoveToGit(context.Background(), "legacy"); err == nil {
		t.Error("moving a system that is already in git must be refused")
	}
}

func TestStartingFromAnEmptyRepository(t *testing.T) {
	h := newHosted(t, "tok", nil)
	store, c := cluster(t)
	s := &Service{URL: h.url(), Path: "systems", Target: store, Token: token("tok"), Write: WritePullRequest}
	store.Git = s

	// An empty repository is a state, not an error.
	st := s.Sync(context.Background())
	if st.Error != "" || !st.Empty || st.Commit != "" {
		t.Fatalf("empty repository: %+v", st)
	}

	// The first system made in the UI starts the branch, even in pull-request mode: there is
	// nothing to open a request into yet.
	_, _, err := store.Create(context.Background(), []byte(shop))
	var proposed *descstore.ProposedError
	if !errors.As(err, &proposed) || !proposed.Proposal.Direct || proposed.Proposal.Branch != "main" {
		t.Fatalf("Create = %v, want a first commit on main", err)
	}
	if got := h.git(h.bare, "show", "main:systems/shop.yaml"); got != strings.TrimSpace(shop) {
		t.Errorf("first file:\n%s", got)
	}
	st = s.Sync(context.Background())
	if st.Empty || st.Error != "" || get(t, c, "shop") == nil {
		t.Fatalf("after the first commit: %+v", st)
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
	if _, _, err := store.Create(context.Background(), []byte(shop)); err == nil {
		t.Fatal("Create must answer with a proposal")
	}
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
