// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package history

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

// Bounds. A typical record is 1 to 2 KB, so 300 of them stay near half of
// the 1 MiB a ConfigMap may hold; maxBytes guards against the worst case
// (every string at its cap) by dropping the oldest closed records first.
const (
	MaxRecords = 300
	MaxAge     = 180 * 24 * time.Hour
	maxBytes   = 900 << 10
)

type State struct {
	Records []Record `json:"records,omitempty"`
}

type Store interface {
	Update(ctx context.Context, mutate func(*State) error) (*State, error)
}

// ErrNoChange can be returned by a mutate function to skip the write.
var ErrNoChange = errors.New("no change")

type MemoryStore struct {
	mu sync.Mutex
	s  State
}

func (m *MemoryStore) Update(_ context.Context, mutate func(*State) error) (*State, error) {
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
	return &out, nil
}

// prune drops closed records older than MaxAge, then keeps at most
// MaxRecords and maxBytes: closed records without a note go first, then
// other closed ones, oldest first; open records go last.
func (s *State) prune(now time.Time) {
	kept := s.Records[:0]
	for _, r := range s.Records {
		if !r.Open && now.Sub(r.end()) > MaxAge {
			continue
		}
		kept = append(kept, r)
	}
	s.Records = kept
	sort.SliceStable(s.Records, func(i, j int) bool { return s.Records[i].StartedAt.Before(s.Records[j].StartedAt) })
	sizes := make([]int, len(s.Records))
	total := 0
	for i, r := range s.Records {
		b, _ := json.Marshal(r)
		sizes[i] = len(b) + 1
		total += sizes[i]
	}
	drop := map[int]bool{}
	count := len(s.Records)
	for _, pass := range []func(Record) bool{
		func(r Record) bool { return !r.Open && r.Note == nil },
		func(r Record) bool { return !r.Open },
		func(Record) bool { return true },
	} {
		for i, r := range s.Records {
			if count <= MaxRecords && total <= maxBytes {
				break
			}
			if !drop[i] && pass(r) {
				drop[i] = true
				count--
				total -= sizes[i]
			}
		}
	}
	if len(drop) == 0 {
		return
	}
	kept = s.Records[:0]
	for i, r := range s.Records {
		if !drop[i] {
			kept = append(kept, r)
		}
	}
	s.Records = kept
}

const ConfigMapKey = "history.json"

// conflictBackoff allows more attempts than retry.DefaultRetry: the webhook,
// the alerts API of every replica and Telegram replies may write at once.
var conflictBackoff = wait.Backoff{Steps: 10, Duration: 10 * time.Millisecond, Factor: 1.5, Jitter: 0.5}

type ConfigMapStore struct {
	// Client should be uncached: a cached client would start a cluster-wide
	// ConfigMap informer.
	Client    client.Client
	Namespace string
	Name      string
}

func (c *ConfigMapStore) read(ctx context.Context) (*State, *corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	err := c.Client.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Name}, &cm)
	if apierrors.IsNotFound(err) {
		return &State{}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read incident history ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
	}
	var s State
	if raw := cm.Data[ConfigMapKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, nil, fmt.Errorf("decode incident history ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
		}
	}
	return &s, &cm, nil
}

func (c *ConfigMapStore) Update(ctx context.Context, mutate func(*State) error) (*State, error) {
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
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kamerton", "app.kubernetes.io/component": "history"},
			}}
			cm.Data = map[string]string{ConfigMapKey: string(raw)}
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
			cm.Data[ConfigMapKey] = string(raw)
			if err := c.Client.Update(ctx, cm); err != nil {
				return err
			}
		}
		result = s
		return nil
	})
	return result, err
}
