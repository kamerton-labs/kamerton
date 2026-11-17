// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/internal/description"
)

type fakeProm struct{ queries []string }

func (f *fakeProm) Query(_ context.Context, q string, _ time.Time) (model.Value, error) {
	f.queries = append(f.queries, q)
	if strings.Contains(q, "STATUS_CODE_ERROR") {
		return model.Vector{{Metric: model.Metric{"service_name": "checkout"}, Value: 0.5}}, nil
	}
	return model.Vector{
		{Metric: model.Metric{"service_name": "checkout"}, Value: 10},
		{Metric: model.Metric{"service_name": "ad"}, Value: 2},
	}, nil
}

func (f *fakeProm) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (model.Matrix, error) {
	return nil, errors.New("unused")
}

func TestDiscover(t *testing.T) {
	fp := &fakeProm{}
	d := &Discoverer{Prom: fp, Telemetry: description.ResolveTelemetry(nil)}
	got, err := d.Discover(context.Background(), map[string]bool{"checkout": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "ad" || got[1].Name != "checkout" {
		t.Fatalf("unexpected: %+v", got)
	}
	if !got[1].Described || got[0].Described {
		t.Errorf("described flags wrong: %+v", got)
	}
	if got[1].ErrorRatio != 0.05 {
		t.Errorf("error ratio = %v", got[1].ErrorRatio)
	}
	if !strings.Contains(fp.queries[0], "traces_span_metrics_calls_total") {
		t.Errorf("query = %s", fp.queries[0])
	}
}

func TestNotConfigured(t *testing.T) {
	d := &Discoverer{Telemetry: description.ResolveTelemetry(nil)}
	if _, err := d.Discover(context.Background(), nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}
