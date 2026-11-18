// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package descstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
)

// Annotations of a system that comes from a git repository. The repository is then the only
// place to change it: the UI and the API refuse to save, so the two cannot drift apart.
const (
	AnnotationGitRepo   = AnnotationPrefix + "git-repo"
	AnnotationGitPath   = AnnotationPrefix + "git-path"
	AnnotationGitCommit = AnnotationPrefix + "git-commit"
)

var (
	ErrFromGit = errors.New("this system comes from a git repository: change its file there, Kamerton picks the change up by itself")
	// ErrNotFromGit: a file names a system that someone made in the UI or with kubectl.
	ErrNotFromGit = errors.New("a system with this name already exists and does not come from this repository; rename one of them or delete the existing system")
)

// GitSource says where a system's file is.
type GitSource struct {
	// Repo identifies the repository and branch ("https://host/team/repo.git#main"); it must not
	// hold credentials.
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Commit string `json:"commit"`
}

// FromGit returns the source of a system kept in git, or nil.
func FromGit(sd *v1alpha1.SystemDescription) *GitSource {
	if sd == nil || sd.Annotations[AnnotationGitRepo] == "" {
		return nil
	}
	return &GitSource{Repo: sd.Annotations[AnnotationGitRepo], Path: sd.Annotations[AnnotationGitPath], Commit: sd.Annotations[AnnotationGitCommit]}
}

// ApplyGit creates or replaces the system of one file. It never touches a system that does not
// come from the same repository. changed is false when the cluster already had this content.
func (c *Cluster) ApplyGit(ctx context.Context, raw []byte, src GitSource) (name string, changed bool, res description.Result, err error) {
	doc, res, err := validate(raw)
	if err != nil {
		return "", false, res, err
	}
	ns := doc.Namespace
	if ns == "" {
		ns = c.Namespace
	}
	if c.WatchNamespace != "" && ns != c.WatchNamespace {
		return doc.Name, false, res, fmt.Errorf("kamerton watches only namespace %s; cannot create a description in %s", c.WatchNamespace, ns)
	}
	stamp := func(a map[string]string) map[string]string {
		a = keepOwnAnnotations(a, doc.Annotations)
		if a == nil {
			a = map[string]string{}
		}
		a[AnnotationGitRepo], a[AnnotationGitPath], a[AnnotationGitCommit] = src.Repo, src.Path, src.Commit
		return a
	}
	var existing v1alpha1.SystemDescription
	err = c.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: doc.Name}, &existing)
	if apierrors.IsNotFound(err) {
		obj := &v1alpha1.SystemDescription{}
		obj.Name, obj.Namespace, obj.Spec = doc.Name, ns, doc.Spec
		obj.Annotations = stamp(nil)
		if err := c.Client.Create(ctx, obj); err != nil {
			return doc.Name, false, res, fmt.Errorf("create SystemDescription %s/%s: %w", ns, doc.Name, err)
		}
		return doc.Name, true, res, nil
	}
	if err != nil {
		return doc.Name, false, res, err
	}
	if from := FromGit(&existing); from == nil || from.Repo != src.Repo {
		return doc.Name, false, res, ErrNotFromGit
	}
	before := existing.Annotations[AnnotationGitPath]
	same := reflect.DeepEqual(existing.Spec, doc.Spec) && before == src.Path
	if same {
		// The commit is recorded only with a change of content, so an unrelated commit does not
		// rewrite every system.
		return doc.Name, false, res, nil
	}
	existing.Spec = doc.Spec
	existing.Annotations = stamp(existing.Annotations)
	if err := c.Client.Update(ctx, &existing); err != nil {
		return doc.Name, false, res, fmt.Errorf("update SystemDescription %s/%s: %w", ns, doc.Name, err)
	}
	return doc.Name, true, res, nil
}

// PruneGit deletes the systems of this repository whose files are gone. keep holds
// namespace/name of the systems still there.
func (c *Cluster) PruneGit(ctx context.Context, repo string, keep map[string]bool) ([]string, error) {
	var list v1alpha1.SystemDescriptionList
	var opts []client.ListOption
	if c.WatchNamespace != "" {
		opts = append(opts, client.InNamespace(c.WatchNamespace))
	}
	if err := c.Client.List(ctx, &list, opts...); err != nil {
		return nil, err
	}
	var gone []string
	for i := range list.Items {
		sd := &list.Items[i]
		if from := FromGit(sd); from == nil || from.Repo != repo || keep[sd.Namespace+"/"+sd.Name] {
			continue
		}
		if err := c.Client.Delete(ctx, sd); err != nil && !apierrors.IsNotFound(err) {
			return gone, fmt.Errorf("delete SystemDescription %s/%s: %w", sd.Namespace, sd.Name, err)
		}
		gone = append(gone, sd.Name)
	}
	return gone, nil
}

// GitKey is the key PruneGit expects for the system of a file.
func (c *Cluster) GitKey(doc *v1alpha1.SystemDescription) string {
	ns := doc.Namespace
	if ns == "" {
		ns = c.Namespace
	}
	return ns + "/" + doc.Name
}
