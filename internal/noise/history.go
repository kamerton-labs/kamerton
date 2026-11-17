// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package noise

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// HistoryQueries: max_over_time over one step sees a firing shorter than the
// step. ALERTS_FOR_STATE is the activation time: the same value across a gap
// is one firing restored after a restart, a new value is a new firing.
func HistoryQueries(system string, step time.Duration) (firing, activeAt string) {
	m := rules.Matchers{}.Eq(generator.LabelSystem, system).Eq(rules.LabelManaged, "true")
	r := rules.FormatDuration(step)
	return fmt.Sprintf("max_over_time(%s[%s])", m.Eq("alertstate", "firing").Selector("ALERTS"), r),
		fmt.Sprintf("max_over_time(%s[%s])", m.Selector("ALERTS_FOR_STATE"), r)
}

// History reads the firings of every alert of a system between start and
// end with two range queries. A failure of the ALERTS_FOR_STATE query only
// loses the gap handling.
func History(ctx context.Context, q prometheus.Querier, system string, start, end time.Time, step time.Duration) ([]Episode, error) {
	fq, aq := HistoryQueries(system, step)
	firing, err := q.QueryRange(ctx, fq, start, end, step)
	if err != nil {
		return nil, err
	}
	var active model.Matrix
	if len(firing) > 0 {
		active, _ = q.QueryRange(ctx, aq, start, end, step)
	}
	return Episodes(firing, active, start, end, step), nil
}

func seriesKey(m model.Metric) string {
	names := make([]string, 0, len(m))
	for k := range m {
		if k != model.MetricNameLabel && k != "alertstate" {
			names = append(names, string(k))
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k + "\x00" + string(m[model.LabelName(k)]) + "\x00")
	}
	return b.String()
}

// Episodes turns sampled firing series into firings. A sample at t means
// the alert fired within (t-step, t]. end must be aligned to step.
func Episodes(firing, activeAt model.Matrix, start, end time.Time, step time.Duration) []Episode {
	active := map[string]map[model.Time]model.SampleValue{}
	for _, s := range activeAt {
		vals := map[model.Time]model.SampleValue{}
		for _, p := range s.Values {
			vals[p.Timestamp] = p.Value
		}
		active[seriesKey(s.Metric)] = vals
	}
	var out []Episode
	for _, s := range firing {
		lbls := make(map[string]string, len(s.Metric))
		for k, v := range s.Metric {
			if k != model.MetricNameLabel && k != "alertstate" {
				lbls[string(k)] = string(v)
			}
		}
		act := active[seriesKey(s.Metric)]
		var (
			cur       *Episode
			prev      time.Time
			curActive model.SampleValue
			hasActive bool
		)
		closeEp := func() {
			if cur == nil {
				return
			}
			cur.End = prev
			cur.Ongoing = !prev.Before(end)
			out = append(out, *cur)
			cur = nil
		}
		for _, p := range s.Values {
			t := p.Timestamp.Time()
			a, ok := act[p.Timestamp]
			if cur != nil {
				sameActivation := ok && hasActive && a == curActive
				changed := ok && hasActive && a != curActive
				if changed || (t.Sub(prev) > step && !sameActivation) {
					closeEp()
				}
			}
			if cur == nil {
				cur = &Episode{Labels: lbls, Start: t.Add(-step)}
				if !t.After(start) {
					cur.Start, cur.Clipped = start, true
				}
				hasActive = false
			}
			if ok {
				curActive, hasActive = a, true
			}
			prev = t
		}
		closeEp()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}
