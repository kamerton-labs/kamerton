// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package stack knows the observability programs Kamerton works with:
// sets of their addresses (the built-in set from the flags and sets added
// in the UI, see sets.go), checks that an address really is the expected
// program (identify.go), and finds programs already running in the cluster
// through the Kubernetes API (discover.go).
package stack

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Status is the health of one program of a set.
type Status struct {
	Name       string `json:"name"`
	URL        string `json:"url,omitempty"`
	Configured bool   `json:"configured"`
	// External: not installed with Kamerton (origin "own").
	External bool `json:"external"`
	// Origin is bundled, own or none.
	Origin string `json:"origin"`
	// Healthy is nil when not configured.
	Healthy   *bool  `json:"healthy"`
	State     string `json:"state,omitempty"`
	Message   string `json:"message,omitempty"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
}

// Checker checks program addresses, remembering results for a short time
// so that several sets sharing a program and frequent page refreshes do not
// multiply requests.
type Checker struct {
	HTTP *http.Client
	// TTL of remembered results (default 10s).
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at  time.Time
	res CheckResult
}

// Check is Check with a short-lived cache.
func (c *Checker) Check(ctx context.Context, kind, u string) CheckResult {
	ttl := c.TTL
	if ttl == 0 {
		ttl = 10 * time.Second
	}
	key := kind + " " + u
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && time.Since(e.at) < ttl {
		c.mu.Unlock()
		return e.res
	}
	c.mu.Unlock()
	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 5 * time.Second}
	}
	res := Check(ctx, httpc, kind, u)
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.cache[key] = cached{at: time.Now(), res: res}
	c.mu.Unlock()
	return res
}

// Health checks every program of a set concurrently.
func (c *Checker) Health(ctx context.Context, set *Resolved) []Status {
	out := make([]Status, len(set.Programs))
	var wg sync.WaitGroup
	for i, p := range set.Programs {
		out[i] = Status{Name: p.Kind, URL: p.URL, Configured: p.URL != "", External: p.Origin == OriginOwn, Origin: p.Origin}
		if p.URL == "" {
			continue
		}
		wg.Add(1)
		go func(i int, p Program) {
			defer wg.Done()
			r := c.Check(ctx, p.Kind, p.URL)
			ok := r.Healthy
			out[i].Healthy = &ok
			out[i].State, out[i].Message, out[i].Version, out[i].Error, out[i].LatencyMs = r.State, r.Message, r.Version, r.Error, r.LatencyMs
		}(i, p)
	}
	wg.Wait()
	return out
}
