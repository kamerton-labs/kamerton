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

type Alert struct {
	Fingerprint  string            `json:"fingerprint"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Status       Status            `json:"status"`
}

// Status is the state of an alert in Alertmanager: "active", "suppressed"
// (silenced or inhibited) or "unprocessed".
type Status struct {
	State       string   `json:"state"`
	SilencedBy  []string `json:"silencedBy,omitempty"`
	InhibitedBy []string `json:"inhibitedBy,omitempty"`
}

func (a *Alert) Inhibited() bool { return len(a.Status.InhibitedBy) > 0 }

// MutedReader also lists alerts muted by inhibit rules. Readers that do not
// implement it are asked for active alerts only.
type MutedReader interface {
	// AlertsWithInhibited returns active alerts, including inhibited ones
	// (Status.InhibitedBy set), without silenced ones.
	AlertsWithInhibited(ctx context.Context, filter map[string]string) ([]Alert, error)
}

type Reader interface {
	ActiveAlerts(ctx context.Context, filter map[string]string) ([]Alert, error)
}

type Client struct {
	base string
	http *http.Client
}

func New(base string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 15 * time.Second}}
}

// ActiveAlerts returns alerts that are neither silenced nor inhibited, filtered by label equality.
func (c *Client) ActiveAlerts(ctx context.Context, filter map[string]string) ([]Alert, error) {
	return c.list(ctx, filter, false)
}

func (c *Client) AlertsWithInhibited(ctx context.Context, filter map[string]string) ([]Alert, error) {
	return c.list(ctx, filter, true)
}

func (c *Client) list(ctx context.Context, filter map[string]string, inhibited bool) ([]Alert, error) {
	q := url.Values{}
	q.Set("active", "true")
	q.Set("silenced", "false")
	q.Set("inhibited", fmt.Sprint(inhibited))
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
