// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package evidence

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// maxOperations is how many failing operations are read.
const maxOperations = 3

// FailingOperations returns the operations of a service with the most failed
// calls within the window, most first. The query selects the same span
// metrics as the error-rate rules (generator.SpanTarget: metric and label
// names, span kinds, excluded operations).
func (c *Collector) FailingOperations(ctx context.Context, prom prometheus.Querier, sd *v1alpha1.SystemDescription, svc *v1alpha1.Service, now time.Time) ([]generator.OperationErrors, error) {
	t := generator.SpanTarget(sd, svc)
	if t.CallsMetric == description.DisabledMetric || t.CallsMetric == "" {
		return nil, nil
	}
	v, err := prom.Query(ctx, rules.FailingOperations(t, c.window(), maxOperations), now)
	if err != nil {
		return nil, fmt.Errorf("failing operations: %w", err)
	}
	vec, _ := v.(model.Vector)
	var out []generator.OperationErrors
	for _, s := range vec {
		op := string(s.Metric[model.LabelName(t.OperationLabel)])
		if op == "" || float64(s.Value) < 0.5 {
			continue
		}
		out = append(out, generator.OperationErrors{Operation: op, Failed: float64(s.Value)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Failed != out[j].Failed {
			return out[i].Failed > out[j].Failed
		}
		return out[i].Operation < out[j].Operation
	})
	return out, nil
}
