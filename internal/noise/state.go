// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

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
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The bounds keep the state far below the 1 MiB limit of a ConfigMap: a
// counter is about 150 bytes, a message about 200.
const (
	maxRules    = 1000
	maxCount    = 100000
	maxMessages = 500
	// messageTTL: buttons older than this answer that they have expired.
	messageTTL = 7 * 24 * time.Hour
)

// RuleCounts are the verdicts about one rule; counters only, nothing about
// who gave them.
type RuleCounts struct {
	System string `json:"system"`
	Key
	Counts
	UpdatedAt time.Time `json:"updatedAt"`
}

// Message is a follow-up message with buttons, keyed by the reference in
// their callback data.
type Message struct {
	System string    `json:"system"`
	Rules  []Key     `json:"rules"`
	At     time.Time `json:"at"`
}

type State struct {
	Rules    []RuleCounts       `json:"rules,omitempty"`
	Messages map[string]Message `json:"messages,omitempty"`
}

// StateStore persists State; Update re-reads and retries on conflicts.
type StateStore interface {
	Update(ctx context.Context, mutate func(*State) error) (*State, error)
}

// ErrNoChange can be returned by a mutate function to skip the write.
var ErrNoChange = errors.New("no change")

type MemoryStateStore struct {
	mu sync.Mutex
	s  State
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

func cloneState(s *State) (*State, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Messages == nil {
		out.Messages = map[string]Message{}
	}
	return &out, nil
}

func (s *State) find(system string, k Key) int {
	for i, r := range s.Rules {
		if r.System == system && r.Key == k {
			return i
		}
	}
	return -1
}

// add changes the counters of one rule by du and dn, never below zero or
// above maxCount, and drops the least recently changed rules beyond maxRules.
func (s *State) add(system string, k Key, du, dn int, now time.Time) Counts {
	i := s.find(system, k)
	if i < 0 {
		s.Rules = append(s.Rules, RuleCounts{System: system, Key: k})
		i = len(s.Rules) - 1
	}
	r := &s.Rules[i]
	r.Useful = min(max(r.Useful+du, 0), maxCount)
	r.Noise = min(max(r.Noise+dn, 0), maxCount)
	r.UpdatedAt = now
	c := r.Counts
	if len(s.Rules) > maxRules {
		sort.SliceStable(s.Rules, func(a, b int) bool { return s.Rules[a].UpdatedAt.After(s.Rules[b].UpdatedAt) })
		s.Rules = s.Rules[:maxRules]
	}
	return c
}

func (s *State) pruneMessages(now time.Time) {
	for ref, m := range s.Messages {
		if now.Sub(m.At) > messageTTL {
			delete(s.Messages, ref)
		}
	}
	if len(s.Messages) <= maxMessages {
		return
	}
	refs := make([]string, 0, len(s.Messages))
	for ref := range s.Messages {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return s.Messages[refs[i]].At.Before(s.Messages[refs[j]].At) })
	for _, ref := range refs[:len(refs)-maxMessages] {
		delete(s.Messages, ref)
	}
}

const StateConfigMapKey = "state.json"

// conflictBackoff allows more attempts than retry.DefaultRetry: presses
// and API calls of several replicas may land in the same moment.
var conflictBackoff = wait.Backoff{Steps: 10, Duration: 10 * time.Millisecond, Factor: 1.5, Jitter: 0.5}

type ConfigMapStateStore struct {
	// Client should be uncached: a cached client would start a cluster-wide
	// ConfigMap informer.
	Client    client.Client
	Namespace string
	Name      string
}

func (c *ConfigMapStateStore) read(ctx context.Context) (*State, *corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	err := c.Client.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, &cm)
	if apierrors.IsNotFound(err) {
		s, _ := cloneState(&State{})
		return s, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read noise state ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
	}
	var s State
	if raw := cm.Data[StateConfigMapKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, nil, fmt.Errorf("decode noise state ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
		}
	}
	out, err := cloneState(&s)
	return out, &cm, err
}

func (c *ConfigMapStateStore) Update(ctx context.Context, mutate func(*State) error) (*State, error) {
	var result *State
	err := retry.RetryOnConflict(conflictBackoff, func() error {
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
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kamerton", "app.kubernetes.io/component": "noise"},
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
