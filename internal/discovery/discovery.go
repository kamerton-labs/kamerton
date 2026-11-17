// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package discovery finds services that already send telemetry, so the UI can
// propose them for the description.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// ErrNotConfigured means no Prometheus is configured, so nothing can be discovered.
var ErrNotConfigured = errors.New("discovery: Prometheus is not configured")

// Service is one discovered service.
type Service struct {
	// Name is the value of the service label in span metrics.
	Name string `json:"name"`
	// RequestsPerSecond is the current rate of server/consumer spans.
	RequestsPerSecond float64 `json:"requestsPerSecond"`
	// ErrorRatio is the current share of failed calls (0..1).
	ErrorRatio float64 `json:"errorRatio"`
	// Described is true when the service is already in the description.
	Described bool `json:"described"`
}

// Discoverer reads service names from span metrics in Prometheus.
type Discoverer struct {
	Prom      prometheus.Querier // nil means not configured
	Telemetry description.ResolvedTelemetry
	Window    time.Duration
	Now       func() time.Time
}

// Discover lists services that emitted spans during the window, marking the
// ones whose names are in described.
//
// TODO: infer dependencies from service graph metrics
// (traces_service_graph_request_total{client,server}) to pre-fill dependsOn.
func (d *Discoverer) Discover(ctx context.Context, described map[string]bool) ([]Service, error) {
	if d.Prom == nil {
		return nil, ErrNotConfigured
	}
	s := d.Telemetry.Spans
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	w := d.Window
	if w == 0 {
		w = 5 * time.Minute
	}
	all := rules.Matchers{}.Re(s.SpanKindLabel, rules.QuoteValues(s.ServerSpanKinds)...)
	failed := append(append(rules.Matchers{}, all...), rules.Matchers{}.Eq(s.StatusCodeLabel, s.ErrorStatusValue)...)
	rateQ := fmt.Sprintf("sum by (%s) (rate(%s[%s]))", s.ServiceLabel, all.Selector(s.CallsMetric), rules.FormatDuration(w))
	errQ := fmt.Sprintf("sum by (%s) (rate(%s[%s]))", s.ServiceLabel, failed.Selector(s.CallsMetric), rules.FormatDuration(w))

	ts := now()
	rates, err := d.vector(ctx, rateQ, ts)
	if err != nil {
		return nil, err
	}
	errs, err := d.vector(ctx, errQ, ts)
	if err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(rates))
	for _, smpl := range rates {
		name := string(smpl.Metric[model.LabelName(s.ServiceLabel)])
		if name == "" {
			continue
		}
		svc := Service{Name: name, RequestsPerSecond: float64(smpl.Value), Described: described[name]}
		for _, e := range errs {
			if string(e.Metric[model.LabelName(s.ServiceLabel)]) == name && smpl.Value > 0 {
				svc.ErrorRatio = float64(e.Value) / float64(smpl.Value)
			}
		}
		out = append(out, svc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (d *Discoverer) vector(ctx context.Context, q string, ts time.Time) (model.Vector, error) {
	v, err := d.Prom.Query(ctx, q, ts)
	if err != nil {
		return nil, fmt.Errorf("discovery: query %q: %w", q, err)
	}
	vec, ok := v.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("discovery: query returned %s, want vector", v.Type())
	}
	return vec, nil
}
