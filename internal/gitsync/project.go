// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/go-git/go-billy/v5"
	"sigs.k8s.io/yaml"
)

// ProjectFile at the root of a project says how the repository is laid out, so the repository
// describes itself: moving a project to another cluster needs its address and nothing else.
const ProjectFile = "kamerton.yaml"

const projectKind = "Project"

// Project is the content of ProjectFile.
type Project struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec ProjectSpec `json:"spec"`
}

type ProjectSpec struct {
	// Systems is the folder with description files, one system per file. Default: systems.
	Systems string `json:"systems,omitempty"`
	// Generated is the folder Kamerton writes generated rules, dashboards and runbooks to.
	// Default: generated. An empty string turns it off.
	Generated *string `json:"generated,omitempty"`
	// Ignore lists folders and files, relative to the project root, that Kamerton does not read:
	// drafts, examples, anything kept next to the systems but not meant to run.
	Ignore []string `json:"ignore,omitempty"`
}

// layout is where things are in the repository, from ProjectFile or, without one, from the
// installation's settings.
type layout struct {
	// manifest: a project file was found.
	manifest bool
	name     string
	// root of the project; systems and generated are paths from the repository root.
	root, systems, generated string
	ignore                   []string
}

// layoutOf reads the project file under the configured path. Without one the configured path
// is the folder of systems, as before project files existed.
func (s *Service) layoutOf(fs billy.Filesystem) (layout, error) {
	root := cleanPath(s.Path)
	l := layout{root: root, systems: root, generated: cleanPath(s.GeneratedPath)}
	f, err := fs.Open(path.Join(root, ProjectFile))
	if err != nil {
		return l, nil
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFile))
	_ = f.Close()
	if err != nil {
		return l, fmt.Errorf("read %s: %w", ProjectFile, err)
	}
	var p Project
	if err := yaml.UnmarshalStrict(raw, &p); err != nil {
		return l, fmt.Errorf("%s: %s", ProjectFile, strings.TrimPrefix(err.Error(), "error unmarshaling JSON: "))
	}
	if p.Kind != projectKind {
		return l, fmt.Errorf("%s: kind must be %s", ProjectFile, projectKind)
	}
	l.manifest, l.name = true, p.Metadata.Name
	l.systems = path.Join(root, orDefault(cleanPath(p.Spec.Systems), "systems"))
	l.generated = path.Join(root, "generated")
	if p.Spec.Generated != nil {
		l.generated = ""
		if g := cleanPath(*p.Spec.Generated); g != "" {
			l.generated = path.Join(root, g)
		}
	}
	for _, ig := range p.Spec.Ignore {
		if ig = cleanPath(ig); ig != "" {
			l.ignore = append(l.ignore, path.Join(root, ig))
		}
	}
	return l, nil
}

// skip reports whether a path is not to be read: ignored, or Kamerton's own output.
func (l layout) skip(p string) bool {
	for _, ig := range l.ignore {
		if p == ig || strings.HasPrefix(p, ig+"/") {
			return true
		}
	}
	return l.generated != "" && l.generated != l.systems && (p == l.generated || strings.HasPrefix(p, l.generated+"/"))
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// starterFiles are what Kamerton puts into an empty repository with the first system, so the
// next person who opens it knows what is where. name is the project's name; empty means the
// repository's.
func (s *Service) starterFiles(name string) map[string][]byte {
	if name = strings.TrimSpace(name); name == "" {
		name = RepoName(s.URL)
	}
	return StarterFiles(name, cleanPath(s.Path), true)
}

// RepoName is the last part of a repository's address without .git: what a project in it is
// called when nobody named it.
func RepoName(address string) string {
	if u, err := url.Parse(address); err == nil {
		if base := strings.TrimSuffix(path.Base(strings.Trim(u.Path, "/")), ".git"); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return "observability"
}

// StarterFiles are the files of a new project under root: the project file, a README, a
// .gitignore and the folder of systems. inGit says whether the project is followed from a
// repository, which the README explains.
func StarterFiles(name, root string, inGit bool) map[string][]byte {
	quoted, _ := json.Marshal(name) // a JSON string is a YAML string, whatever the name holds
	how := "Kamerton reads the systems from this folder; a change made here is picked up at once."
	if inGit {
		how = "Kamerton follows the default branch: a merged change is in the cluster within a minute."
	}
	return map[string][]byte{
		path.Join(root, ProjectFile): []byte(`# A Kamerton project: the systems it describes and where things are.
# Open it in Kamerton by its folder or its repository; the layout is read from here.
apiVersion: kamerton.dev/v1alpha1
kind: Project
metadata:
  name: ` + string(quoted) + `
spec:
  # One system per file. Subfolders are fine, for example one per team.
  systems: systems
  # Rules, dashboards and runbooks generated from the systems. Written by Kamerton with every
  # change it makes; do not edit by hand. Set to "" to keep them out of the project.
  generated: generated
  # Folders and files Kamerton does not read.
  ignore:
    - drafts
`),
		path.Join(root, "README.md"): []byte(`# ` + name + `

System descriptions for [Kamerton](https://github.com/kamerton-labs/kamerton).

| Path | What it is | Who changes it |
| --- | --- | --- |
| ` + "`kamerton.yaml`" + ` | The project file: where things are | People, rarely |
| ` + "`systems/`" + ` | One system per file: services, who calls whom, thresholds, receivers | People, here or in the Kamerton UI |
| ` + "`generated/`" + ` | The rules, dashboards and runbooks the systems produce | Kamerton; do not edit by hand |
| ` + "`drafts/`" + ` | Anything not meant to run yet | People; Kamerton does not read it |

` + how + `

Check a file before you commit it, and see which rules a change affects:

` + "```sh" + `
kamerton validate -f systems/shop.yaml
kamerton diff -f systems/shop.yaml --against <the file before the change>
` + "```" + `

Secrets do not belong here. Bot tokens and mail passwords are entered in the Kamerton UI and
kept in the cluster.
`),
		// Git keeps no empty folders: this one shows where the systems go.
		path.Join(root, "systems", ".gitkeep"): []byte(""),
		path.Join(root, ".gitignore"): []byte(`# Local output of "kamerton generate -o out/".
out/
.before.yaml

# Kept by a local Kamerton next to the systems: addresses of Prometheus and Alertmanager.
.kamerton-stacks.json

# Secrets never belong in this repository.
.env
.env.*
*.secret.yaml
*.secret.yml

# Editors and operating systems.
.idea/
.vscode/
.DS_Store
`),
	}
}
