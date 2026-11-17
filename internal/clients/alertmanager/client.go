// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package alertmanager reads active alerts from the Alertmanager v2 API.
package alertmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Alert is the subset of an Alertmanager v2 alert used by Kamerton.
type Alert struct {
	Fingerprint  string            `json:"fingerprint"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Status       struct {
		State string `json:"state"`
	} `json:"status"`
}

// Reader lists active alerts.
type Reader interface {
	ActiveAlerts(ctx context.Context, filter map[string]string) ([]Alert, error)
}

// Client implements Reader over HTTP.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for the Alertmanager at base (for example
// http://alertmanager-operated.monitoring:9093).
func New(base string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 15 * time.Second}}
}

// ActiveAlerts returns active (not silenced, not inhibited) alerts matching
// the label equality filter.
func (c *Client) ActiveAlerts(ctx context.Context, filter map[string]string) ([]Alert, error) {
	q := url.Values{}
	q.Set("active", "true")
	q.Set("silenced", "false")
	q.Set("inhibited", "false")
	for k, v := range filter {
		q.Add("filter", fmt.Sprintf("%s=%q", k, v))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v2/alerts?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("alertmanager: %s", resp.Status)
	}
	var alerts []Alert
	if err := json.NewDecoder(resp.Body).Decode(&alerts); err != nil {
		return nil, fmt.Errorf("alertmanager: decode: %w", err)
	}
	return alerts, nil
}
