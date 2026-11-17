// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// State holds no secret: the SMTP password lives only in the password store (a Secret).
type State struct {
	// Server is the mail server; nil until it is set up.
	Server     *ServerSettings       `json:"server,omitempty"`
	Recipients map[string]*Recipient `json:"recipients,omitempty"`
}

type ServerSettings struct {
	// Smarthost is host:port.
	Smarthost string `json:"smarthost"`
	From      string `json:"from"`
	// Username is the SMTP login; empty means no authentication.
	Username    string    `json:"username,omitempty"`
	HasPassword bool      `json:"hasPassword"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Recipient struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	To        []string  `json:"to"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *State) SortedRecipients() []*Recipient {
	out := make([]*Recipient, 0, len(s.Recipients))
	for _, r := range s.Recipients {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// StateStore persists State; Update re-reads and retries on conflicts.
type StateStore interface {
	Get(ctx context.Context) (*State, error)
	Update(ctx context.Context, mutate func(*State) error) (*State, error)
}

// ErrNoChange can be returned by a mutate function to skip the write.
var ErrNoChange = errors.New("no change")

func cloneState(s *State) (*State, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Recipients == nil {
		out.Recipients = map[string]*Recipient{}
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
		if errors.Is(err, ErrNoChange) {
			return cloneState(&m.s)
		}
		return nil, err
	}
	m.s = *cp
	return cloneState(&m.s)
}

const StateConfigMapKey = "state.json"

// ConfigMapStateStore keeps state as JSON in a ConfigMap, shared by replicas and kept across restarts.
type ConfigMapStateStore struct {
	// Client should be uncached.
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
		s, _ := cloneState(&State{})
		return s, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read state ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
	}
	var s State
	if raw := cm.Data[StateConfigMapKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, nil, fmt.Errorf("decode state ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
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
			if errors.Is(err, ErrNoChange) {
				result = s
				return nil
			}
			return err
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if cm == nil {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Namespace: c.Namespace, Name: c.Name,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kamerton", "app.kubernetes.io/component": "email"},
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

// PasswordStore stores the SMTP password (telegram.SecretTokenStore fits).
type PasswordStore interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, password string) error
	Delete(ctx context.Context) error
	Ref() (name, key string)
}

// Default Secret holding the SMTP password: the convention of hand-written
// descriptions (authPasswordSecret: {name: kamerton-smtp, key: password}).
const (
	DefaultSecretName = "kamerton-smtp"
	DefaultSecretKey  = "password"
)

type MemoryPasswordStore struct {
	mu sync.Mutex
	pw string
}

func (m *MemoryPasswordStore) Get(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pw, nil
}

func (m *MemoryPasswordStore) Set(_ context.Context, pw string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pw = pw
	return nil
}

func (m *MemoryPasswordStore) Delete(ctx context.Context) error { return m.Set(ctx, "") }

func (m *MemoryPasswordStore) Ref() (string, string) { return DefaultSecretName, DefaultSecretKey }
