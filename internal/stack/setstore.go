// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package stack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StateStore persists State. Update re-reads, applies mutate and writes,
// retrying on conflicts; a mutate error aborts without writing.
type StateStore interface {
	Get(ctx context.Context) (*State, error)
	Update(ctx context.Context, mutate func(*State) error) (*State, error)
}

func cloneState(s *State) (*State, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Sets == nil {
		out.Sets = map[string]*Set{}
	}
	return &out, nil
}

type MemoryStateStore struct {
	mu sync.Mutex
	s  State
}

func (m *MemoryStateStore) Get(context.Context) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneState(&m.s)
}

func (m *MemoryStateStore) Update(_ context.Context, mutate func(*State) error) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp, err := cloneState(&m.s)
	if err != nil {
		return nil, err
	}
	if err := mutate(cp); err != nil {
		return nil, err
	}
	m.s = *cp
	return cloneState(&m.s)
}

// FileStateStore is used in local mode. A missing file is an empty state.
type FileStateStore struct {
	Path string
	mu   sync.Mutex
}

func (f *FileStateStore) Get(context.Context) (*State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.read()
}

func (f *FileStateStore) read() (*State, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return cloneState(&State{})
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Path, err)
	}
	return cloneState(&s)
}

func (f *FileStateStore) Update(_ context.Context, mutate func(*State) error) (*State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.read()
	if err != nil {
		return nil, err
	}
	if err := mutate(s); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".kamerton-stacks-*")
	if err != nil {
		return nil, err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	return cloneState(s)
}

const StateConfigMapKey = "stacks.json"

const DefaultConfigMapName = "kamerton-stacks"

// ConfigMapStateStore is shared by replicas and watched by the controller.
// Nothing in it is secret.
type ConfigMapStateStore struct {
	// Client should be uncached, so a retry after a conflict reads the latest version.
	Client    client.Client
	Namespace string
	Name      string
}

func (c *ConfigMapStateStore) Get(ctx context.Context) (*State, error) {
	s, _, err := c.read(ctx)
	return s, err
}

func (c *ConfigMapStateStore) read(ctx context.Context) (*State, *corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	err := c.Client.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, &cm)
	if apierrors.IsNotFound(err) {
		s, err := cloneState(&State{})
		return s, nil, err
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read sets ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
	}
	var s State
	if raw := cm.Data[StateConfigMapKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, nil, fmt.Errorf("decode sets ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
		}
	}
	out, err := cloneState(&s)
	return out, &cm, err
}

func (c *ConfigMapStateStore) Update(ctx context.Context, mutate func(*State) error) (*State, error) {
	var result *State
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		s, cm, err := c.read(ctx)
		if err != nil {
			return err
		}
		if err := mutate(s); err != nil {
			return err
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if cm == nil {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Namespace: c.Namespace, Name: c.Name,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kamerton", "app.kubernetes.io/component": "stacks"},
			}}
			cm.Data = map[string]string{StateConfigMapKey: string(raw)}
			if err := c.Client.Create(ctx, cm); err != nil {
				if apierrors.IsAlreadyExists(err) {
					return apierrors.NewConflict(corev1.Resource("configmaps"), c.Name, err)
				}
				return err
			}
		} else {
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data[StateConfigMapKey] = string(raw)
			if err := c.Client.Update(ctx, cm); err != nil {
				return err
			}
		}
		result = s
		return nil
	})
	return result, err
}

// SwitchStateStore keeps the sets of the project that is open; a local Kamerton keeps them next
// to the project's systems and switches with the project.
type SwitchStateStore struct {
	mu  sync.RWMutex
	cur StateStore
}

// Set uses st from now on; nil keeps the sets in memory.
func (s *SwitchStateStore) Set(st StateStore) {
	s.mu.Lock()
	s.cur = st
	s.mu.Unlock()
}

func (s *SwitchStateStore) current() StateStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		s.cur = &MemoryStateStore{}
	}
	return s.cur
}

func (s *SwitchStateStore) Get(ctx context.Context) (*State, error) { return s.current().Get(ctx) }

func (s *SwitchStateStore) Update(ctx context.Context, mutate func(*State) error) (*State, error) {
	return s.current().Update(ctx, mutate)
}
