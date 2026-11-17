// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package backtest estimates how often an alerting rule would have fired over stored history.
package backtest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
)

var ErrNotConfigured = errors.New("backtest: Prometheus is not configured")

// MaxPoints caps the number of steps per query (Prometheus rejects > 11000).
const MaxPoints = 11000

type Request struct {
	Expr  string        `json:"expr"`
	For   time.Duration `json:"for"`
	Start time.Time     `json:"start"`
	End   time.Time     `json:"end"`
	Step  time.Duration `json:"step"`
}

type Episode struct {
	Labels   map[string]string `json:"labels"`
	FiringAt time.Time         `json:"firingAt"`
	// ResolvedAt is the first step without the condition, or the end of the
	// range if the alert would still be firing.
	ResolvedAt time.Time `json:"resolvedAt"`
	Ongoing    bool      `json:"ongoing"`
}

type Result struct {
	Firings  int       `json:"firings"`
	Series   int       `json:"series"`
	Episodes []Episode `json:"episodes"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Step     string    `json:"step"`
}

func Run(ctx context.Context, q prometheus.Querier, req Request) (*Result, error) {
	if q == nil {
		return nil, ErrNotConfigured
	}
	if req.Expr == "" {
		return nil, errors.New("backtest: empty expression")
	}
	if !req.End.After(req.Start) {
		return nil, errors.New("backtest: end must be after start")
	}
	if req.Step <= 0 {
		req.Step = time.Minute
	}
	if n := req.End.Sub(req.Start) / req.Step; n > MaxPoints {
		return nil, fmt.Errorf("backtest: %d steps exceed the limit of %d; increase the step or shorten the range", n, MaxPoints)
	}
	m, err := q.QueryRange(ctx, req.Expr, req.Start, req.End, req.Step)
	if err != nil {
		return nil, fmt.Errorf("backtest: %w", err)
	}
	res := Evaluate(m, req.For, req.Step, req.End)
	res.Start, res.End = req.Start, req.End
	return res, nil
}

// Evaluate computes firing episodes as Prometheus would: a run of samples fires once
// it lasts forDur. Precision is one step, so step should not exceed the evaluation interval.
func Evaluate(m model.Matrix, forDur, step time.Duration, end time.Time) *Result {
	res := &Result{Series: len(m), Step: model.Duration(step).String(), Episodes: []Episode{}}
	for _, s := range m {
		lbls := make(map[string]string, len(s.Metric))
		for k, v := range s.Metric {
			lbls[string(k)] = string(v)
		}
		var (
			runStart time.Time
			prev     time.Time
			firing   bool
			inRun    bool
		)
		closeRun := func(resolvedAt time.Time, ongoing bool) {
			if firing {
				res.Episodes[len(res.Episodes)-1].ResolvedAt = resolvedAt
				res.Episodes[len(res.Episodes)-1].Ongoing = ongoing
			}
			inRun, firing = false, false
		}
		for _, p := range s.Values {
			t := p.Timestamp.Time()
			if inRun && t.Sub(prev) > step {
				closeRun(prev.Add(step), false)
			}
			if !inRun {
				inRun, runStart = true, t
			}
			if !firing && t.Sub(runStart) >= forDur {
				firing = true
				res.Firings++
				res.Episodes = append(res.Episodes, Episode{Labels: lbls, FiringAt: t})
			}
			prev = t
		}
		if inRun {
			next := prev.Add(step)
			if next.After(end) {
				closeRun(end, true)
			} else {
				closeRun(next, false)
			}
		}
	}
	sort.Slice(res.Episodes, func(i, j int) bool { return res.Episodes[i].FiringAt.Before(res.Episodes[j].FiringAt) })
	return res
}
