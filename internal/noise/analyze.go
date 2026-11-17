// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/rulefmt"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/backtest"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

var ErrNotConfigured = errors.New("prometheus is not configured")

// suggestConcurrency bounds the rules whose candidates are evaluated at once.
const suggestConcurrency = 3

type Analyzer struct {
	Prom      prometheus.Querier
	Generator generator.Options
	Now       func() time.Time
	Logger    *slog.Logger
}

func (a *Analyzer) log() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

// Analyze reads the history of every alert of the system once and computes
// suggestions for its noisiest rules. A failed suggestion leaves the rule
// without one; only a failed history read is an error.
func (a *Analyzer) Analyze(ctx context.Context, sd *v1alpha1.SystemDescription, window time.Duration, windowName string, feedback map[Key]Counts) (*Report, error) {
	if a.Prom == nil {
		return nil, ErrNotConfigured
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	end := now.Truncate(Step)
	start := end.Add(-window)
	eps, err := History(ctx, a.Prom, sd.Name, start, end, Step)
	if err != nil {
		return nil, err
	}
	inh := newInhibition(sd, eps)
	byKey := map[Key][]Episode{}
	consequences := map[Key]int{}
	keys := map[Key]bool{}
	for _, e := range eps {
		k := KeyOf(sd, e.Labels["alertname"], e.Labels[rules.LabelService], e.Labels[rules.LabelOperation])
		keys[k] = true
		if inh.mutedEpisode(e) {
			consequences[k]++
			continue
		}
		byKey[k] = append(byKey[k], e)
	}
	rep := newReport(windowName)
	rep.Available = true
	for k := range feedback {
		keys[k] = true
	}
	for k := range keys {
		st := Summarize(byKey[k])
		rr := RuleReport{Key: k, Fired: st.Fired, Short: st.Short, Consequences: consequences[k], LongestSeconds: int64(st.Longest / time.Second), Feedback: feedback[k]}
		if !st.LastFiredAt.IsZero() {
			t := st.LastFiredAt.UTC()
			rr.LastFiredAt = &t
		}
		rr.Noisy = Noisy(st, rr.Feedback, window)
		rep.Rules = append(rep.Rules, rr)
	}
	sortRules(rep.Rules)

	bundle, err := generator.Generate(sd, a.Generator)
	if err != nil {
		a.log().Warn("noise: no suggestions, the description does not generate", "system", sd.Name, "error", err)
		return rep, nil
	}
	ev := &evaluator{q: a.Prom, sd: sd, opts: a.Generator, start: start, end: end, current: bundle, inh: inh}
	var idx []int
	for i := range rep.Rules {
		k := rep.Rules[i].Key
		if rep.Rules[i].Noisy && rep.Rules[i].Short > 0 && len(idx) < MaxSuggestedRules && len(generator.Tunables(sd, k.Service, k.Alert, k.Operation)) > 0 {
			idx = append(idx, i)
		}
	}
	sem := make(chan struct{}, suggestConcurrency)
	var wg sync.WaitGroup
	for _, i := range idx {
		wg.Add(1)
		go func(rr *RuleReport) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := ev.suggest(ctx, rr.Key, byKey[rr.Key])
			if err != nil {
				a.log().Debug("noise: suggestion failed", "system", sd.Name, "rule", rr.String(), "error", err)
			}
			rr.Suggestion = s
		}(&rep.Rules[i])
	}
	wg.Wait()
	return rep, nil
}

type evaluator struct {
	q          prometheus.Querier
	sd         *v1alpha1.SystemDescription
	opts       generator.Options
	start, end time.Time
	current    *generator.Bundle
	inh        *inhibition
}

type Candidate struct {
	Tunable generator.Tunable
	Value   string
	// Delay is how much later a real failure would be reported (longer "for").
	Delay time.Duration
	small bool
}

func findRule(b *generator.Bundle, k Key) *rulefmt.Rule {
	for _, g := range b.RuleGroups.Groups {
		for i, r := range g.Rules {
			if r.Alert == k.Alert && r.Labels[rules.LabelService] == k.Service && r.Labels[rules.LabelOperation] == k.Operation {
				return &g.Rules[i]
			}
		}
	}
	return nil
}

// Candidates: small thresholds first (no delay), then a longer "for" with a
// known, capped delay, then larger thresholds, which change what the rule
// counts as bad.
func Candidates(ts []generator.Tunable, severity string) []Candidate {
	maxFor := MaxSuggestedFor
	if severity == string(v1alpha1.SeverityCritical) {
		maxFor = MaxSuggestedForCritical
	}
	var small, fors, large []Candidate
	for _, t := range ts {
		if t.Field == generator.FieldFor {
			cur, err := rules.ParseDuration(t.Current, 0)
			if err != nil {
				continue
			}
			for _, d := range forSteps {
				if d > cur && d <= maxFor && len(fors) < maxForCandidates {
					fors = append(fors, Candidate{Tunable: t, Value: rules.FormatDuration(d), Delay: d - cur})
				}
			}
			continue
		}
		for _, c := range thresholdValues(t) {
			if c.small {
				small = append(small, c)
			} else {
				large = append(large, c)
			}
		}
	}
	return append(append(small, fors...), large...)
}

var (
	thresholdFactors = []float64{1.25, 1.5, 2, 3}
	forSteps         = []time.Duration{time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute, 10 * time.Minute}
)

// thresholdValues returns the candidate values of one threshold in the form
// the field takes.
func thresholdValues(t generator.Tunable) []Candidate {
	var out []Candidate
	add := func(v string, factor float64) {
		for _, x := range out {
			if x.Value == v {
				return
			}
		}
		out = append(out, Candidate{Tunable: t, Value: v, small: factor <= SmallThresholdFactor})
	}
	switch t.Format {
	case generator.FormatInteger:
		cur, err := strconv.Atoi(t.Current)
		if err != nil || cur < 0 {
			return nil
		}
		for _, v := range []int{cur + 1, cur + 2, cur * 2, cur * 3} {
			if v > cur && len(out) < maxThresholdCandidates {
				factor := math.Inf(1)
				if cur > 0 {
					factor = float64(v) / float64(cur)
				}
				add(strconv.Itoa(v), factor)
			}
		}
	case generator.FormatQuantity:
		q, err := resource.ParseQuantity(t.Current)
		if err != nil || q.Sign() <= 0 {
			return nil
		}
		cur := q.AsApproximateFloat64()
		for _, f := range thresholdFactors {
			add(strconv.FormatInt(int64(math.Ceil(cur*f/(1<<20))), 10)+"Mi", f)
		}
	case generator.FormatShare, generator.FormatNumber:
		cur, err := strconv.ParseFloat(t.Current, 64)
		if err != nil || cur <= 0 {
			return nil
		}
		factorOf := func(v float64) float64 {
			if t.Lower {
				return cur / v
			}
			return v / cur
		}
		for _, f := range thresholdFactors {
			v := cur * f
			if t.Lower {
				v = cur / f
			}
			v = roundNice(v)
			if t.Format == generator.FormatShare && v >= 1 {
				continue
			}
			if (t.Lower && v < cur) || (!t.Lower && v > cur) {
				add(rules.FormatFloat(v), factorOf(v))
			}
		}
		if t.Format == generator.FormatShare && len(out) == 0 && cur < 1 {
			if v := math.Round((1+cur)/2*1000) / 1000; v > cur && v < 1 {
				add(rules.FormatFloat(v), factorOf(v))
			}
		}
	}
	return out
}

// roundNice keeps two significant digits, so 0.0625 becomes 0.063 and
// 1250 becomes 1300.
func roundNice(v float64) float64 {
	if v == 0 {
		return 0
	}
	exp := int(math.Floor(math.Log10(math.Abs(v)))) - 1
	if exp >= 0 {
		p := math.Pow(10, float64(exp))
		return math.Round(v/p) * p
	}
	p := math.Pow(10, float64(-exp))
	f, _ := strconv.ParseFloat(strconv.FormatFloat(math.Round(v*p)/p, 'f', -exp, 64), 64)
	return f
}

// suggest returns the first candidate that misses no long firing and
// removes enough short ones. actual holds only firings people got; the
// candidate's firings at muted times are dropped the same way.
func (e *evaluator) suggest(ctx context.Context, k Key, actual []Episode) (*Suggestion, error) {
	cur := findRule(e.current, k)
	if cur == nil {
		return nil, nil
	}
	st := Summarize(actual)
	matrices := map[string]model.Matrix{}
	m, err := e.q.QueryRange(ctx, cur.Expr, e.start, e.end, Step)
	if err != nil {
		return nil, err
	}
	matrices[cur.Expr] = m
	severity := cur.Labels[rules.LabelSeverity]
	muted := func(from, to time.Time) bool { return e.inh.muted(k.Service, k.Alert, severity, from, to) }
	if fire, _, _ := score(backtest.Evaluate(m, time.Duration(cur.For), Step, e.end), cur.Labels, time.Duration(cur.For), e.start, actual, muted); float64(fire) < MinReproducedShare*float64(st.Fired) {
		return nil, nil
	}
	for _, c := range Candidates(generator.Tunables(e.sd, k.Service, k.Alert, k.Operation), severity) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cp := e.sd.DeepCopy()
		if err := c.Tunable.Apply(cp, k.Service, c.Value); err != nil {
			continue
		}
		b, err := generator.Generate(cp, e.opts)
		if err != nil {
			continue
		}
		r := findRule(b, k)
		if r == nil || (r.Expr == cur.Expr && r.For == cur.For) {
			continue
		}
		m, ok := matrices[r.Expr]
		if !ok {
			if m, err = e.q.QueryRange(ctx, r.Expr, e.start, e.end, Step); err != nil {
				return nil, err
			}
			matrices[r.Expr] = m
		}
		fire, short, miss := score(backtest.Evaluate(m, time.Duration(r.For), Step, e.end), r.Labels, time.Duration(r.For), e.start, actual, muted)
		if fire == 0 && st.Fired < MinFiredForSilence {
			continue
		}
		if miss == 0 && fire < st.Fired && float64(short) <= (1-MinShortRemovedShare)*float64(st.Short) {
			return &Suggestion{
				Field: c.Tunable.Field, Current: c.Tunable.Current, Suggested: c.Value, Unit: c.Tunable.Unit,
				WouldFire: fire, WouldMiss: miss, WouldShort: short, Path: c.Tunable.Path, Format: string(c.Tunable.Format),
				DelaySeconds: int64(c.Delay / time.Second),
			}, nil
		}
	}
	return nil, nil
}

// score compares the firings a candidate would have had with the firings
// that happened: how many, how many short, and how many long real firings
// no candidate firing of the same series overlaps.
func score(res *backtest.Result, static map[string]string, forD time.Duration, start time.Time, actual []Episode, muted func(from, to time.Time) bool) (fire, short, miss int) {
	var eps []Episode
	for _, b := range res.Episodes {
		if muted(b.FiringAt, b.ResolvedAt) {
			continue
		}
		e := Episode{Labels: b.Labels, Start: b.FiringAt, End: b.ResolvedAt, Ongoing: b.Ongoing}
		// A run that began with the data may have begun earlier.
		e.Clipped = !b.FiringAt.After(start.Add(forD))
		eps = append(eps, e)
		if e.Short() {
			short++
		}
	}
	for _, a := range actual {
		if !a.Long() {
			continue
		}
		caught := false
		for _, e := range eps {
			if sameSeries(e.Labels, a.Labels, static) && !e.Start.After(a.End) && !e.End.Add(Step).Before(a.Start) {
				caught = true
				break
			}
		}
		if !caught {
			miss++
		}
	}
	return len(eps), short, miss
}

// sameSeries: the alert carries the labels of the expression's result,
// except those the rule sets itself.
func sameSeries(expr, alert, static map[string]string) bool {
	for k, v := range expr {
		if k == model.MetricNameLabel {
			continue
		}
		if _, ok := static[k]; ok {
			continue
		}
		if alert[k] != v {
			return false
		}
	}
	return true
}
