// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package stack checks, discovers and groups the observability programs Kamerton uses.
package stack

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type Status struct {
	Name       string `json:"name"`
	URL        string `json:"url,omitempty"`
	Configured bool   `json:"configured"`
	// External means origin "own": not installed with Kamerton.
	External bool `json:"external"`
	// Origin: bundled, own or none.
	Origin string `json:"origin"`
	// Healthy is nil when not configured.
	Healthy   *bool  `json:"healthy"`
	State     string `json:"state,omitempty"`
	Message   string `json:"message,omitempty"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
}

// Checker caches results briefly, so sets sharing a program and frequent page
// refreshes do not multiply requests.
type Checker struct {
	HTTP *http.Client
	// TTL defaults to 10s.
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at  time.Time
	res CheckResult
}

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
