// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package prometheus wraps the Prometheus HTTP API behind a small interface that is easy to fake.
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

type Querier interface {
	Query(ctx context.Context, query string, ts time.Time) (model.Value, error)
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (model.Matrix, error)
}

type Client struct {
	api v1.API
}

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

func (c *Client) Query(ctx context.Context, query string, ts time.Time) (model.Value, error) {
	v, _, err := c.api.Query(ctx, query, ts)
	return v, err
}

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
