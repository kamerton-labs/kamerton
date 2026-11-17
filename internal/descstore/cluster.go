// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package descstore

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
)

type Cluster struct {
	// An uncached Client sees writes immediately; a cached one is fine for reads.
	Client client.Client
	// Namespace is where new descriptions are created; its descriptions are listed first.
	Namespace string
	// WatchNamespace limits the search; empty means all namespaces.
	WatchNamespace string
	// Name, when set, is listed first and so becomes the default system.
	Name string
}

func (c *Cluster) Mode() string { return ModeCluster }

func (c *Cluster) snapshot(sd *v1alpha1.SystemDescription) (*Snapshot, error) {
	raw, err := description.ToYAML(sd)
	if err != nil {
		return nil, err
	}
	return &Snapshot{ID: sd.Name, SD: sd, Raw: raw, Source: "cluster:" + sd.Namespace + "/" + sd.Name}, nil
}

func (c *Cluster) List(ctx context.Context) ([]*Snapshot, error) {
	var list v1alpha1.SystemDescriptionList
	var opts []client.ListOption
	if c.WatchNamespace != "" {
		opts = append(opts, client.InNamespace(c.WatchNamespace))
	}
	if err := c.Client.List(ctx, &list, opts...); err != nil {
		return nil, err
	}
	out := make([]*Snapshot, 0, len(list.Items))
	for i := range list.Items {
		snap, err := c.snapshot(&list.Items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	sortSnapshots(out, c.Namespace)
	if c.Name != "" {
		for i, s := range out {
			if s.SD.Name == c.Name {
				out = append([]*Snapshot{s}, append(out[:i:i], out[i+1:]...)...)
				break
			}
		}
	}
	assignIDs(out, func(s *Snapshot) string { return s.SD.Namespace })
	return out, nil
}

func (c *Cluster) Get(ctx context.Context, id string) (*Snapshot, error) {
	list, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	if s := findByID(list, id); s != nil {
		return s, nil
	}
	return nil, ErrNoDescription
}

func (c *Cluster) key(ctx context.Context, id string) (types.NamespacedName, error) {
	cur, err := c.Get(ctx, id)
	if err != nil {
		return types.NamespacedName{}, err
	}
	return types.NamespacedName{Namespace: cur.SD.Namespace, Name: cur.SD.Name}, nil
}

// Create puts the resource in the document's namespace, else in Kamerton's.
func (c *Cluster) Create(ctx context.Context, raw []byte) (*Snapshot, description.Result, error) {
	doc, res, err := validate(raw)
	if err != nil {
		return nil, res, err
	}
	ns := doc.Namespace
	if ns == "" {
		ns = c.Namespace
	}
	if c.WatchNamespace != "" && ns != c.WatchNamespace {
		return nil, res, fmt.Errorf("kamerton watches only namespace %s; cannot create a description in %s", c.WatchNamespace, ns)
	}
	obj := &v1alpha1.SystemDescription{}
	obj.Name, obj.Namespace = doc.Name, ns
	obj.Annotations = keepOwnAnnotations(nil, doc.Annotations)
	obj.Spec = doc.Spec
	if err := c.Client.Create(ctx, obj); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, res, ErrExists
		}
		return nil, res, fmt.Errorf("create SystemDescription %s/%s: %w", ns, doc.Name, err)
	}
	snap, err := c.snapshot(obj)
	return snap, res, err
}

// Save replaces the spec and Kamerton's annotations; other metadata is left alone.
func (c *Cluster) Save(ctx context.Context, id string, raw []byte) (*Snapshot, description.Result, error) {
	doc, res, err := validate(raw)
	if err != nil {
		return nil, res, err
	}
	key, err := c.key(ctx, id)
	if err != nil {
		return nil, res, err
	}
	if key.Name != doc.Name {
		return nil, res, ErrRename
	}
	var existing v1alpha1.SystemDescription
	if err := c.Client.Get(ctx, key, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, res, ErrNoDescription
		}
		return nil, res, err
	}
	existing.Spec = doc.Spec
	existing.Annotations = keepOwnAnnotations(existing.Annotations, doc.Annotations)
	if err := c.Client.Update(ctx, &existing); err != nil {
		if apierrors.IsConflict(err) {
			return nil, res, ErrConflict
		}
		return nil, res, fmt.Errorf("update SystemDescription %s/%s: %w", key.Namespace, key.Name, err)
	}
	snap, err := c.snapshot(&existing)
	if snap != nil {
		snap.ID = id
	}
	return snap, res, err
}

// Update retries when the resource changed concurrently.
func (c *Cluster) Update(ctx context.Context, id string, mutate func(*v1alpha1.SystemDescription) error) (*Snapshot, error) {
	key, err := c.key(ctx, id)
	if err != nil {
		return nil, err
	}
	var snap *Snapshot
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var sd v1alpha1.SystemDescription
		if err := c.Client.Get(ctx, key, &sd); err != nil {
			if apierrors.IsNotFound(err) {
				return ErrNoDescription
			}
			return err
		}
		if err := mutate(&sd); err != nil {
			return err
		}
		raw, err := description.ToYAML(&sd)
		if err != nil {
			return err
		}
		if _, _, err := validate(raw); err != nil {
			return err
		}
		if err := c.Client.Update(ctx, &sd); err != nil {
			return err
		}
		snap, err = c.snapshot(&sd)
		if snap != nil {
			snap.ID = id
		}
		return err
	})
	return snap, err
}

func (c *Cluster) Delete(ctx context.Context, id string) error {
	key, err := c.key(ctx, id)
	if err != nil {
		return err
	}
	obj := &v1alpha1.SystemDescription{}
	obj.Name, obj.Namespace = key.Name, key.Namespace
	if err := c.Client.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNoDescription
		}
		if apierrors.IsForbidden(err) && strings.Contains(err.Error(), "delete") {
			return fmt.Errorf("kamerton has no permission to delete SystemDescriptions (upgrade the Helm chart): %w", err)
		}
		return err
	}
	return nil
}
