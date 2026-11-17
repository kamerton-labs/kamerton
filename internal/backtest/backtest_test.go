// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package backtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/common/model"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// series builds a series with samples at the given minute offsets.
func series(name string, minutes ...int) *model.SampleStream {
	s := &model.SampleStream{Metric: model.Metric{"service": model.LabelValue(name)}}
	for _, m := range minutes {
		s.Values = append(s.Values, model.SamplePair{Timestamp: model.TimeFromUnixNano(t0.Add(time.Duration(m) * time.Minute).UnixNano()), Value: 1})
	}
	return s
}

func TestEvaluate(t *testing.T) {
	end := t0.Add(60 * time.Minute)
	m := model.Matrix{
		// 0..4: 4 minutes long -> fires with for=3m at minute 3; gap; 10..11: too short.
		series("a", 0, 1, 2, 3, 4, 10, 11),
		// 55..60: still true at the end -> ongoing.
		series("b", 55, 56, 57, 58, 59, 60),
	}
	res := Evaluate(m, 3*time.Minute, time.Minute, end)
	if res.Firings != 2 {
		t.Fatalf("firings = %d, episodes %+v", res.Firings, res.Episodes)
	}
	a := res.Episodes[0]
	if !a.FiringAt.Equal(t0.Add(3*time.Minute)) || !a.ResolvedAt.Equal(t0.Add(5*time.Minute)) || a.Ongoing {
		t.Errorf("episode a = %+v", a)
	}
	b := res.Episodes[1]
	if !b.FiringAt.Equal(t0.Add(58*time.Minute)) || !b.Ongoing {
		t.Errorf("episode b = %+v", b)
	}
}

func TestEvaluateForZero(t *testing.T) {
	res := Evaluate(model.Matrix{series("a", 5, 20)}, 0, time.Minute, t0.Add(time.Hour))
	if res.Firings != 2 {
		t.Fatalf("with for=0 each isolated sample fires; got %d", res.Firings)
	}
}

type fakeQ struct{ m model.Matrix }

func (f fakeQ) Query(context.Context, string, time.Time) (model.Value, error) {
	return nil, errors.New("unused")
}
func (f fakeQ) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (model.Matrix, error) {
	return f.m, nil
}

func TestRunValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := Run(ctx, nil, Request{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil querier: %v", err)
	}
	q := fakeQ{m: model.Matrix{series("a", 0, 1, 2)}}
	if _, err := Run(ctx, q, Request{Expr: "up == 0", Start: t0, End: t0.Add(30 * 24 * time.Hour), Step: time.Second}); err == nil {
		t.Error("expected step limit error")
	}
	res, err := Run(ctx, q, Request{Expr: "up == 0", For: time.Minute, Start: t0, End: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Firings != 1 || res.Step != "1m" {
		t.Errorf("unexpected result %+v", res)
	}
}
