// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package telegram

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

// State is what Kamerton remembers about the bot. It holds no secret: the bot
// ID is the public part of the token, used to notice that the token was replaced.
type State struct {
	Bot *BotInfo `json:"bot,omitempty"`
	// Offset is the next getUpdates offset (last processed update_id + 1).
	Offset int64 `json:"offset,omitempty"`
	// Chats are keyed by chat ID, including chats the bot has left.
	Chats map[int64]*KnownChat `json:"chats,omitempty"`
	// Poll is the health of the polling loop, written by the leader.
	Poll PollStatus `json:"poll"`
}

type BotInfo struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	FirstName string    `json:"firstName"`
	CheckedAt time.Time `json:"checkedAt"`
}

type KnownChat struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
	// Status of the bot in the chat: member, administrator, left, kicked, ...
	Status string `json:"status"`
	// Available is false when the bot was removed or the chat cannot be reached.
	Available    bool      `json:"available"`
	Problem      string    `json:"problem,omitempty"`
	MigratedFrom int64     `json:"migratedFrom,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// Source: "update" (discovered by polling), "check" (checked by ID),
	// "description" (referenced by the description).
	Source string `json:"source"`
}

type PollStatus struct {
	Poller     string    `json:"poller,omitempty"`
	LastPollAt time.Time `json:"lastPollAt,omitempty"`
	LastError  string    `json:"lastError,omitempty"`
	ErrorAt    time.Time `json:"errorAt,omitempty"`
	Conflict   bool      `json:"conflict,omitempty"`
}

func (s *State) SortedChats() []*KnownChat {
	out := make([]*KnownChat, 0, len(s.Chats))
	for _, c := range s.Chats {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Available != out[j].Available {
			return out[i].Available
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// StateStore persists State. Update must be safe against concurrent writers
// (another replica or the poller): it re-reads and retries on conflicts.
type StateStore interface {
	Get(ctx context.Context) (*State, error)
	Update(ctx context.Context, mutate func(*State) error) (*State, error)
}

// ErrNoChange can be returned by a mutate function to skip the write.
var ErrNoChange = errors.New("no change")

type MemoryStateStore struct {
	mu sync.Mutex
	s  State
}

func (m *MemoryStateStore) Get(context.Context) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(&m.s)
}

func (m *MemoryStateStore) Update(_ context.Context, mutate func(*State) error) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp, err := clone(&m.s)
	if err != nil {
		return nil, err
	}
	if err := mutate(cp); err != nil {
		if errors.Is(err, ErrNoChange) {
			return clone(&m.s)
		}
		return nil, err
	}
	m.s = *cp
	return clone(&m.s)
}

func clone(s *State) (*State, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Chats == nil {
		out.Chats = map[int64]*KnownChat{}
	}
	return &out, nil
}

const StateConfigMapKey = "state.json"

// ConfigMapStateStore needs no volume: the root filesystem is read-only.
type ConfigMapStateStore struct {
	// Client should be uncached: a cached client would start a cluster-wide
	// ConfigMap informer.
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
		s, _ := clone(&State{})
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
	out, err := clone(&s)
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
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kamerton", "app.kubernetes.io/component": "telegram"},
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
