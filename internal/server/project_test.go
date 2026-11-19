// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/project"
	"github.com/kamerton-labs/kamerton/internal/stack"
)

func TestProjectAPI(t *testing.T) {
	// Without a place for projects the installation is its own project: no first screen.
	srv := newTestServer(t)
	if code, body := get(t, srv.URL+"/api/v1/project"); code != 200 || !strings.Contains(body, `"exists":true`) {
		t.Errorf("no projects: %d %s", code, body)
	}

	dir := t.TempDir()
	sw, stacks := &descstore.Switch{}, &stack.SwitchStateStore{}
	local := &project.Local{Root: filepath.Join(dir, "kamerton"), RecentFile: filepath.Join(dir, "recent.json"), Descriptions: sw, Stacks: stacks}
	s := httptest.NewServer(New(Options{Descriptions: sw, Sets: &stack.Sets{Store: stacks}, Projects: local}).Handler())
	t.Cleanup(s.Close)

	if code, body := get(t, s.URL+"/api/v1/project"); code != 200 || !strings.Contains(body, `"exists":false`) || !strings.Contains(body, `"where":"local"`) {
		t.Fatalf("before: %d %s", code, body)
	}
	resp, err := post(t, s.URL+"/api/v1/project", "application/json", `{"name":""}`)
	if err != nil || resp.StatusCode != 422 {
		t.Fatalf("no name: %v %v", resp.StatusCode, err)
	}
	resp, err = post(t, s.URL+"/api/v1/project", "application/json", `{"name":"Shop"}`)
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("create: %v %v", resp.StatusCode, err)
	}
	if code, body := get(t, s.URL+"/api/v1/project"); code != 200 || !strings.Contains(body, `"name":"Shop"`) {
		t.Errorf("after: %d %s", code, body)
	}
	// Systems now have somewhere to go.
	if code, body := get(t, s.URL+"/api/v1/systems"); code != 200 || !strings.Contains(body, `"mode":"file"`) {
		t.Errorf("systems: %d %s", code, body)
	}
	resp, err = post(t, s.URL+"/api/v1/project", "application/json", `{"name":"Shop"}`)
	if err != nil || resp.StatusCode != 409 {
		t.Errorf("the same folder again: %v %v", resp.StatusCode, err)
	}

	// A folder without git: the step fails with a problem the UI says in words.
	if _, err := exec.LookPath("git"); err == nil {
		resp, err = post(t, s.URL+"/api/v1/project/git/fetch", "application/json", `{}`)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 422 || !strings.Contains(string(body), `"problem":"not_repo"`) || !strings.Contains(string(body), `"state":{"available":false`) {
			t.Errorf("fetch without git: %d %s", resp.StatusCode, body)
		}
	}

	// Closing goes back to the first screen and offers the project again.
	resp, err = post(t, s.URL+"/api/v1/project/close", "application/json", `{}`)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("close: %v %v", resp.StatusCode, err)
	}
	if _, body := get(t, s.URL+"/api/v1/project"); !strings.Contains(body, `"exists":false`) || !strings.Contains(body, `"recent":[{"name":"Shop"`) {
		t.Errorf("after closing: %s", body)
	}

	// Opening by folder is for a local Kamerton only.
	c := httptest.NewServer(New(Options{Projects: &project.Cluster{}}).Handler())
	t.Cleanup(c.Close)
	resp, err = post(t, c.URL+"/api/v1/project/open", "application/json", `{"path":"/tmp"}`)
	if err != nil || resp.StatusCode != 409 {
		t.Errorf("open in a cluster: %v %v", resp.StatusCode, err)
	}
}
