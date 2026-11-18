// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package alertmanager reads active alerts from the Alertmanager v2 API.
package alertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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
	// Tail: silenced only by Kamerton's own tail silences.
	Tail bool `json:"-"`
}

// Status is the state of an alert in Alertmanager: "active", "suppressed"
// (silenced or inhibited) or "unprocessed".
type Status struct {
	State       string   `json:"state"`
	SilencedBy  []string `json:"silencedBy,omitempty"`
	InhibitedBy []string `json:"inhibitedBy,omitempty"`
}

func (a *Alert) Inhibited() bool { return len(a.Status.InhibitedBy) > 0 }

// Muted: not sent to the channels, by an inhibit rule or by Kamerton's own silence on the tail
// of an incident.
func (a *Alert) Muted() bool { return a.Inhibited() || a.Tail }

// TailSilenceAuthor marks the silences Kamerton puts on the consequences of an incident, so that
// they stay quiet for a while after the cause is gone.
const TailSilenceAuthor = "kamerton"

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

// AlertsWithInhibited also returns alerts under Kamerton's own tail silences (Tail set); alerts
// silenced by anyone else stay out, as in ActiveAlerts.
func (c *Client) AlertsWithInhibited(ctx context.Context, filter map[string]string) ([]Alert, error) {
	alerts, err := c.list(ctx, filter, true)
	if err != nil {
		return nil, err
	}
	silenced := false
	for _, a := range alerts {
		silenced = silenced || len(a.Status.SilencedBy) > 0
	}
	if !silenced {
		return alerts, nil
	}
	own, err := c.tailSilences(ctx)
	if err != nil {
		return nil, err
	}
	out := alerts[:0]
	for _, a := range alerts {
		a.Tail = len(a.Status.SilencedBy) > 0
		for _, id := range a.Status.SilencedBy {
			a.Tail = a.Tail && own[id]
		}
		if len(a.Status.SilencedBy) == 0 || a.Tail {
			out = append(out, a)
		}
	}
	return out, nil
}

// list: muted also asks for silenced alerts; the caller keeps only those under tail silences.
func (c *Client) list(ctx context.Context, filter map[string]string, inhibited bool) ([]Alert, error) {
	q := url.Values{}
	q.Set("active", "true")
	q.Set("silenced", fmt.Sprint(inhibited))
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

// Silence is one silence on exactly one alert: a matcher per label.
type Silence struct {
	// ID of the silence to prolong; empty creates one.
	ID      string
	Labels  map[string]string
	EndsAt  time.Time
	Comment string
}

type silenceMatcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

type silenceBody struct {
	ID        string           `json:"id,omitempty"`
	Matchers  []silenceMatcher `json:"matchers"`
	StartsAt  time.Time        `json:"startsAt"`
	EndsAt    time.Time        `json:"endsAt"`
	CreatedBy string           `json:"createdBy"`
	Comment   string           `json:"comment"`
	Status    struct {
		State string `json:"state"`
	} `json:"status"`
}

// SetSilence creates the silence or moves its end, and returns its ID. Alertmanager keeps the ID
// when only the end changes.
func (c *Client) SetSilence(ctx context.Context, sil Silence, now time.Time) (string, error) {
	body := silenceBody{ID: sil.ID, StartsAt: now, EndsAt: sil.EndsAt, CreatedBy: TailSilenceAuthor, Comment: sil.Comment}
	names := make([]string, 0, len(sil.Labels))
	for k := range sil.Labels {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		body.Matchers = append(body.Matchers, silenceMatcher{Name: k, Value: sil.Labels[k], IsEqual: true})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v2/silences", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("alertmanager: silence: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		SilenceID string `json:"silenceID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("alertmanager: silence: decode: %w", err)
	}
	return out.SilenceID, nil
}

// tailSilences returns the IDs of the active silences Kamerton made.
func (c *Client) tailSilences(ctx context.Context) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v2/silences", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("alertmanager: silences: %s", resp.Status)
	}
	var list []silenceBody
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("alertmanager: silences: decode: %w", err)
	}
	own := map[string]bool{}
	for _, s := range list {
		if s.CreatedBy == TailSilenceAuthor && s.Status.State == "active" {
			own[s.ID] = true
		}
	}
	return own, nil
}
