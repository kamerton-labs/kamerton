// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

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

// maxRecords keeps the state far below the 1 MiB limit of a ConfigMap
// (a record is about 200 bytes).
const maxRecords = 500

// Record is one incident a follow-up was prepared for, keyed by
// "<incident ID>@<start, unix seconds>".
type Record struct {
	At     time.Time `json:"at"`
	System string    `json:"system"`
	Roots  []string  `json:"roots,omitempty"`
	// Since is when the first root's alert started.
	Since time.Time `json:"since,omitzero"`
	// Likely are the dependencies the follow-up named as the likely cause.
	Likely []string `json:"likely,omitempty"`
	Sent   bool     `json:"sent,omitempty"`
	// Attempts that sent nothing, and the reason of the last one. Done: given
	// up, no more attempts. LeaseUntil: an attempt is running until then.
	Attempts   int       `json:"attempts,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Done       bool      `json:"done,omitempty"`
	LeaseUntil time.Time `json:"leaseUntil,omitzero"`
}

type State struct {
	Records map[string]Record `json:"records,omitempty"`
}

// StateStore persists State. Update re-reads and retries on conflicts, so
// replicas sharing it send one follow-up per incident between them.
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
	if out.Records == nil {
		out.Records = map[string]Record{}
	}
	return &out, nil
}

// prune drops records older than ttl, then the oldest beyond maxRecords.
func (s *State) prune(now time.Time, ttl time.Duration) {
	for k, r := range s.Records {
		if now.Sub(r.At) > ttl {
			delete(s.Records, k)
		}
	}
	if len(s.Records) <= maxRecords {
		return
	}
	keys := make([]string, 0, len(s.Records))
	for k := range s.Records {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return s.Records[keys[i]].At.Before(s.Records[keys[j]].At) })
	for _, k := range keys[:len(keys)-maxRecords] {
		delete(s.Records, k)
	}
}

const StateConfigMapKey = "state.json"

// ConfigMapStateStore keeps the state next to the Telegram and email state;
// it holds service names and times only.
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
		return nil, nil, fmt.Errorf("read follow-up state ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
	}
	var s State
	if raw := cm.Data[StateConfigMapKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, nil, fmt.Errorf("decode follow-up state ConfigMap %s/%s: %w", c.Namespace, c.Name, err)
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
				Labels: map[string]string{"app.kubernetes.io/managed-by": "kamerton", "app.kubernetes.io/component": "followup"},
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
