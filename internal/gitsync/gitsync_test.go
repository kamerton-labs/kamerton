// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/descstore"
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

type repo struct {
	t   *testing.T
	dir string
	r   *git.Repository
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed: go-git reads a local repository through it")
	}
	dir := t.TempDir()
	r, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	if err != nil {
		t.Fatal(err)
	}
	return &repo{t: t, dir: dir, r: r}
}

// commit writes files (nil content deletes) and commits them.
func (r *repo) commit(files map[string]*string) string {
	r.t.Helper()
	w, err := r.r.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	for name, content := range files {
		full := filepath.Join(r.dir, name)
		if content == nil {
			if _, err := w.Remove(name); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(*content), 0o600); err != nil {
			r.t.Fatal(err)
		}
		if _, err := w.Add(name); err != nil {
			r.t.Fatal(err)
		}
	}
	h, err := w.Commit("change", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}})
	if err != nil {
		r.t.Fatal(err)
	}
	return h.String()
}

func str(s string) *string { return &s }

func cluster(t *testing.T, objs ...client.Object) (*descstore.Cluster, client.Client) {
	t.Helper()
	sch := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build()
	return &descstore.Cluster{Client: c, Namespace: "kamerton"}, c
}

func get(t *testing.T, c client.Client, name string) *v1alpha1.SystemDescription {
	t.Helper()
	var sd v1alpha1.SystemDescription
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kamerton", Name: name}, &sd); err != nil {
		return nil
	}
	return &sd
}

func states(st Status) map[string]string {
	out := map[string]string{}
	for _, f := range st.Files {
		out[f.Path] = f.State
	}
	return out
}

func TestSyncFollowsTheBranch(t *testing.T) {
	r := newRepo(t)
	first := r.commit(map[string]*string{
		"systems/shop.yaml":    str(shop),
		"systems/values.yaml":  str("replicaCount: 2\n"), // not a description: ignored
		"README.md":            str("hello"),
		"elsewhere/other.yaml": str(strings.ReplaceAll(shop, "name: shop", "name: outside")),
	})
	store, c := cluster(t)
	s := &Service{URL: r.dir, Branch: "main", Path: "systems", Target: store}

	st := s.Sync(context.Background())
	if st.Error != "" || st.Commit != first || len(st.Files) != 1 || states(st)["systems/shop.yaml"] != StateApplied {
		t.Fatalf("first sync: %+v", st)
	}
	sd := get(t, c, "shop")
	if sd == nil || len(sd.Spec.Services) != 1 {
		t.Fatalf("shop not created: %+v", sd)
	}
	if from := descstore.FromGit(sd); from == nil || from.Path != "systems/shop.yaml" || from.Commit != first || !strings.HasSuffix(from.Repo, "#main") {
		t.Errorf("source = %+v", descstore.FromGit(sd))
	}
	if get(t, c, "outside") != nil {
		t.Error("a file outside the folder must not be applied")
	}

	// The UI cannot change a system that lives in git.
	if _, _, err := store.Save(context.Background(), "shop", []byte(shop)); err != descstore.ErrFromGit {
		t.Errorf("Save = %v, want ErrFromGit", err)
	}
	if err := store.Delete(context.Background(), "shop"); err != descstore.ErrFromGit {
		t.Errorf("Delete = %v, want ErrFromGit", err)
	}

	// Nothing new: no clone, no writes.
	if st = s.Sync(context.Background()); st.Commit != first || states(st)["systems/shop.yaml"] != StateApplied {
		t.Fatalf("idle sync changed the status: %+v", st)
	}

	// A change in the file reaches the cluster with the new commit.
	second := r.commit(map[string]*string{"systems/shop.yaml": str(strings.ReplaceAll(shop, "name: api", "name: gateway"))})
	st = s.Sync(context.Background())
	if st.Commit != second || states(st)["systems/shop.yaml"] != StateApplied {
		t.Fatalf("second sync: %+v", st)
	}
	if sd = get(t, c, "shop"); sd.Spec.Services[0].Name != "gateway" || descstore.FromGit(sd).Commit != second {
		t.Errorf("after the change: %+v %+v", sd.Spec.Services, descstore.FromGit(sd))
	}

	// A broken file keeps the working system and says what is wrong.
	r.commit(map[string]*string{"systems/shop.yaml": str(strings.ReplaceAll(shop, "type: http", "type: carrier-pigeon"))})
	st = s.Sync(context.Background())
	if states(st)["systems/shop.yaml"] != StateInvalid || st.Files[0].Error == "" || st.Files[0].System != "shop" {
		t.Fatalf("broken file: %+v", st)
	}
	if sd = get(t, c, "shop"); sd == nil || sd.Spec.Services[0].Name != "gateway" {
		t.Errorf("a broken file must leave the system as it was: %+v", sd)
	}

	// The file is gone: so is the system.
	r.commit(map[string]*string{"systems/shop.yaml": nil})
	st = s.Sync(context.Background())
	if st.Error != "" || len(st.Removed) != 1 || st.Removed[0] != "shop" || get(t, c, "shop") != nil {
		t.Fatalf("after the file is deleted: %+v", st)
	}
}

func TestSyncLeavesOtherSystemsAlone(t *testing.T) {
	r := newRepo(t)
	r.commit(map[string]*string{"shop.yaml": str(shop)})
	byHand := &v1alpha1.SystemDescription{}
	byHand.Name, byHand.Namespace = "shop", "kamerton"
	byHand.Spec.Services = []v1alpha1.Service{{Name: "made-in-ui", Type: v1alpha1.ServiceTypeHTTP}}
	manual := &v1alpha1.SystemDescription{}
	manual.Name, manual.Namespace = "manual", "kamerton"
	store, c := cluster(t, byHand, manual)
	s := &Service{URL: r.dir, Target: store}

	st := s.Sync(context.Background())
	if states(st)["shop.yaml"] != StateConflict {
		t.Fatalf("a file must not take over a system made by hand: %+v", st)
	}
	if sd := get(t, c, "shop"); sd.Spec.Services[0].Name != "made-in-ui" {
		t.Errorf("the hand-made system was overwritten: %+v", sd.Spec.Services)
	}
	if get(t, c, "manual") == nil {
		t.Error("a system that never came from git was deleted")
	}
}

func TestSyncFailureDeletesNothing(t *testing.T) {
	r := newRepo(t)
	r.commit(map[string]*string{"systems/shop.yaml": str(shop)})
	store, c := cluster(t)
	s := &Service{URL: r.dir, Path: "systems", Target: store}
	s.Sync(context.Background())

	// A mistyped folder, then an unreachable repository: the system stays.
	s.Path = "sistems"
	r.commit(map[string]*string{"note.txt": str("x")})
	if st := s.Sync(context.Background()); !strings.Contains(st.Error, "not found") {
		t.Fatalf("missing folder: %+v", st)
	}
	s.Path, s.URL = "systems", filepath.Join(r.dir, "nowhere")
	if st := s.Sync(context.Background()); st.Error == "" {
		t.Fatal("an unreachable repository must be reported")
	}
	if get(t, c, "shop") == nil {
		t.Error("a failed sync deleted a system")
	}
}

func TestRepoHidesCredentials(t *testing.T) {
	s := &Service{URL: "https://user:secret@example.com/team/obs.git"}
	if got := s.Repo(); strings.Contains(got, "secret") || got != "https://example.com/team/obs.git" {
		t.Errorf("Repo() = %q", got)
	}
}
