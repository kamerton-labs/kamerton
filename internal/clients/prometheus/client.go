// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package prometheus is a thin wrapper over the official Prometheus HTTP API
// client. Kamerton only needs a few read calls, so the interface is small and
// easy to fake in tests.
package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// Querier is the subset of the Prometheus API used by Kamerton.
type Querier interface {
	// Query runs an instant query.
	Query(ctx context.Context, query string, ts time.Time) (model.Value, error)
	// QueryRange runs a range query.
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (model.Matrix, error)
}

// Client implements Querier against a Prometheus-compatible HTTP API.
type Client struct {
	api v1.API
}

// New creates a client for the Prometheus server at address (for example
// http://prometheus-operated.monitoring:9090).
func New(address string) (*Client, error) {
	c, err := api.NewClient(api.Config{
		Address: address,
		Client:  &http.Client{Timeout: 30 * time.Second},
	})
	if err != nil {
		return nil, err
	}
	return &Client{api: v1.NewAPI(c)}, nil
}

// Query implements Querier.
func (c *Client) Query(ctx context.Context, query string, ts time.Time) (model.Value, error) {
	v, _, err := c.api.Query(ctx, query, ts)
	return v, err
}

// QueryRange implements Querier.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (model.Matrix, error) {
	v, _, err := c.api.QueryRange(ctx, query, v1.Range{Start: start, End: end, Step: step})
	if err != nil {
		return nil, err
	}
	m, ok := v.(model.Matrix)
	if !ok {
		return nil, fmt.Errorf("range query returned %s, want matrix", v.Type())
	}
	return m, nil
}
