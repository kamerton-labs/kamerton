// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package project

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/gitsync"
)

// NameStore keeps the name of a project that lives in the cluster.
type NameStore interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, name string) error
}

// Cluster is the project of an installation: its systems are resources in the cluster, its name
// is kept there too, and a repository can be connected to it at the start or later.
type Cluster struct {
	Names     NameStore
	Git       *gitsync.Runner
	Systems   descstore.Store
	Namespace string
}

func (c *Cluster) Info(ctx context.Context) Info {
	in := Info{Where: WhereCluster, Namespace: c.Namespace}
	if c.Names != nil {
		in.Name, _ = c.Names.Get(ctx)
	}
	if c.Git != nil {
		v := c.Git.View(ctx)
		in.CanClone = v.Available && !v.Locked
		// A repository makes a project of its own, and one set by the installation always is.
		if v.Connected {
			in.Exists = true
		}
	}
	if in.Name != "" {
		in.Exists = true
	}
	// An installation from before projects, with systems of its own, is a project already: it is
	// not to be sent to the first screen.
	if !in.Exists && c.Systems != nil {
		if list, err := c.Systems.List(ctx); err != nil || len(list) > 0 {
			in.Exists = true
		}
	}
	return in
}

// New names the installation's project. Its systems stay in the cluster until a repository is
// connected.
func (c *Cluster) New(ctx context.Context, req NewRequest, _ string) (Info, error) {
	name, err := cleanName(req.Name)
	if err != nil {
		return Info{}, err
	}
	if c.Info(ctx).Exists {
		return Info{}, ErrExists
	}
	if c.Names == nil {
		return Info{}, userErr("there is nowhere to keep the project here")
	}
	if err := c.Names.Set(ctx, name); err != nil {
		return Info{}, err
	}
	return c.Info(ctx), nil
}

// ConfigMapName keeps the project's name in a ConfigMap.
type ConfigMapName struct {
	Client          client.Client
	Namespace, Name string
}

const nameKey = "name"

func (c *ConfigMapName) Get(ctx context.Context) (string, error) {
	var cm corev1.ConfigMap
	err := c.Client.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, &cm)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cm.Data[nameKey], nil
}

func (c *ConfigMapName) Set(ctx context.Context, name string) error {
	var cm corev1.ConfigMap
	err := c.Client.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, &cm)
	if apierrors.IsNotFound(err) {
		cm.Namespace, cm.Name = c.Namespace, c.Name
		cm.Data = map[string]string{nameKey: name}
		return c.Client.Create(ctx, &cm)
	}
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[nameKey] = name
	return c.Client.Update(ctx, &cm)
}
