// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql/promqltest"
)

// Times in these scenarios are minutes from the start of the data (a midnight).
const day = 24 * 60

// stretch is a stretch of one span-metric counter, one sample a minute as the
// Collector flushes it. perMinute 0 is a service without calls: with
// metrics_expiration: 2m its series gets two more flat samples and then ends,
// and the next call starts a new series from zero. pipelineDown ends the
// samples at once, as when the Collector or remote write stops.
type stretch struct {
	minutes   int
	perMinute float64
}

const pipelineDown = -1

func counter(ss ...stretch) string {
	var merged []stretch
	for _, s := range ss {
		if n := len(merged); n > 0 && merged[n-1].perMinute == s.perMinute {
			merged[n-1].minutes += s.minutes
			continue
		}
		merged = append(merged, s)
	}
	var out []string
	c, alive := 0.0, false
	for _, s := range merged {
		n := s.minutes
		if s.perMinute > 0 {
			out = append(out, fmt.Sprintf("%s+%sx%d", num(c+s.perMinute), num(s.perMinute), n-1))
			c += s.perMinute * float64(n)
			alive = true
			continue
		}
		if alive && s.perMinute == 0 {
			k := min(2, n)
			out = append(out, fmt.Sprintf("%sx%d", num(c), k-1))
			n -= k
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("_x%d", n))
		}
		c, alive = 0, false
	}
	return strings.Join(out, " ")
}

// daily has calls from the hour from to the hour to of each of n days.
func daily(n, from, to int, perMinute float64) []stretch {
	var out []stretch
	for range n {
		out = append(out, stretch{from * 60, 0}, stretch{(to - from) * 60, perMinute}, stretch{(24 - to) * 60, 0})
	}
	return out
}

func series(service string, ss ...stretch) string {
	return fmt.Sprintf("  calls{service_name=%q, span_kind=\"SPAN_KIND_SERVER\"} %s\n", service, counter(ss...))
}

// episode is one firing of an alert, in minutes; end is -1 while it still fires.
type episode struct{ start, end float64 }

const evalEvery = 30 * time.Second

// episodes evaluates expr every 30s between from and to and plays Prometheus'
// alert states on it: firing once the condition held at every evaluation for
// forD, resolved at the first evaluation without it.
func episodes(t *testing.T, data, expr string, from, to int, forD time.Duration) []episode {
	t.Helper()
	// The head of the storage starts at the first sample appended, and a load
	// appends its series in random order: an anchor at 0 comes first.
	st := promqltest.LoadedStorage(t, "load 1m\n  anchor 0\n\nload 1m\n"+data)
	eng := promqltest.NewTestEngine(t, false, 0, 1e9)
	q, err := eng.NewRangeQuery(context.Background(), st, nil, expr,
		time.Unix(int64(from*60), 0), time.Unix(int64(to*60), 0), evalEvery)
	if err != nil {
		t.Fatal(err)
	}
	res := q.Exec(context.Background())
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	m, err := res.Matrix()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) > 1 {
		t.Fatalf("more than one series: %v", m)
	}
	on := map[int64]bool{}
	for _, s := range m {
		for _, p := range s.Floats {
			on[p.T] = true
		}
	}
	var out []episode
	var activeAt int64 = -1
	firing := false
	for ts := int64(from * 60 * 1000); ts <= int64(to*60*1000); ts += evalEvery.Milliseconds() {
		if !on[ts] {
			if firing {
				out[len(out)-1].end = float64(ts) / 60000
			}
			activeAt, firing = -1, false
			continue
		}
		if activeAt < 0 {
			activeAt = ts
		}
		if !firing && ts-activeAt >= forD.Milliseconds() {
			firing = true
			out = append(out, episode{start: float64(ts) / 60000, end: -1})
		}
	}
	return out
}

// The rules of "pay" as generated for a critical service (traffic rule on) and
// a non-critical one (no-data alone), and the system rule; defaults otherwise.
type payRules struct{ stopped, noData, noDataAlone, system string }

const trafficFor = 2 * time.Minute

func newPayRules(t *testing.T) payRules {
	t.Helper()
	tp := TrafficParams{
		Meta: Meta{Service: "pay", Severity: "critical"}, Target: noDataTarget(),
		Threshold: 0.1, MinRequestsPerSecond: 0.02, SystemServices: []string{"pay", "web", "cart"},
	}
	nd := ServiceNoDataParams{
		Meta: Meta{Service: "pay", Severity: "critical"}, Target: noDataTarget(), After: 3 * time.Minute,
		Others: []string{"web", "cart"}, MinRequestsPerSecond: 0.02, Traffic: &tp,
	}
	stopped, noData := TrafficStopped(tp), ServiceNoData(nd)
	nd.Traffic = nil
	alone := ServiceNoData(nd)
	system := SystemNoData(SystemNoDataParams{
		Meta: Meta{Severity: "critical"}, Target: noDataTarget(), After: 3 * time.Minute,
		Services: []string{"pay", "web", "cart"}, MinRequestsPerSecond: 0.02,
	})
	mustValidate(t, stopped, noData, alone, system)
	return payRules{stopped.Expr, noData.Expr, alone.Expr, system.Expr}
}

// expect checks the alerts of every rule over from..to: want holds the
// firings of a rule (nil: none). A firing must start within its window
// [start, start+slack] and end within [end, end+slack] (end -1: still firing).
type firing struct{ start, end int }

const slack = 4

func (r payRules) expect(t *testing.T, data string, from, to int, want map[string][]firing) {
	t.Helper()
	for name, rule := range map[string]struct {
		expr string
		forD time.Duration
	}{
		"requests stopped":        {r.stopped, trafficFor},
		"no data next to traffic": {r.noData, NoDataPending},
		"no data alone":           {r.noDataAlone, NoDataPending},
		"system no data":          {r.system, NoDataPending},
	} {
		got := episodes(t, data, rule.expr, from, to, rule.forD)
		w := want[name]
		t.Logf("%s: %v", name, got)
		ok := len(got) == len(w)
		for i := 0; ok && i < len(w); i++ {
			ok = got[i].start >= float64(w[i].start) && got[i].start <= float64(w[i].start+slack) &&
				((w[i].end < 0 && got[i].end < 0) || (w[i].end >= 0 && got[i].end >= float64(w[i].end) && got[i].end <= float64(w[i].end+slack)))
		}
		if !ok {
			t.Errorf("%s: firings %v (minutes), want %v within %d minutes", name, got, w, slack)
		}
	}
}

// web and cart, the rest of the system, send all the time.
func othersAllDay(minutes int) string {
	return series("web", stretch{minutes, 60}) + series("cart", stretch{minutes, 30})
}

func TestNoDataScenarios(t *testing.T) {
	t.Parallel()
	r := newPayRules(t)
	noon := day + 12*60

	t.Run("steady traffic, then the service stops for good: one alert, no resolve in the middle", func(t *testing.T) {
		t.Parallel()
		data := series("pay", stretch{noon, 6}, stretch{4 * 60, 0}) + othersAllDay(noon+4*60)
		// Its last calls are in the sample at noon-1; the series ends after noon+1.
		r.expect(t, data, noon-30, noon+4*60-1, map[string][]firing{
			"requests stopped": {{noon + 2, -1}},
			"no data alone":    {{noon + 5, -1}},
		})
	})

	t.Run("the service stops, then recovers: one alert, resolved when calls return", func(t *testing.T) {
		t.Parallel()
		data := series("pay", stretch{noon, 6}, stretch{60, 0}, stretch{2 * 60, 6}) + othersAllDay(noon+3*60)
		r.expect(t, data, noon-30, noon+3*60-1, map[string][]firing{
			"requests stopped": {{noon + 2, noon + 60}},
			"no data alone":    {{noon + 5, noon + 60}},
		})
	})

	t.Run("idle at night, traffic by day: nothing at night, an alert when it stops during the day", func(t *testing.T) {
		t.Parallel()
		pay := append(daily(2, 8, 20, 6), stretch{14 * 60, 6}, stretch{10 * 60, 0})
		data := series("pay", pay...) + series("web", daily(3, 8, 20, 60)...) + series("cart", daily(3, 8, 20, 30)...)
		stop := 2*day + 14*60
		// The alerts end an hour before the usual end of traffic: from then on
		// the window around this time yesterday is no longer busy throughout.
		quiet := 2*day + 19*60 + 7
		r.expect(t, data, day+19*60, 2*day+23*60, map[string][]firing{
			"requests stopped": {{stop + 2, quiet}},
			"no data alone":    {{stop + 5, quiet}},
		})
	})

	t.Run("one service idle at night while the others run", func(t *testing.T) {
		t.Parallel()
		data := series("pay", daily(3, 8, 20, 6)...) + othersAllDay(3*day)
		r.expect(t, data, day+19*60, 2*day+12*60, nil)
	})

	t.Run("a service that always has very low traffic: never alerts", func(t *testing.T) {
		t.Parallel()
		data := series("pay", stretch{noon, 0.6}, stretch{4 * 60, 0}) + othersAllDay(noon+4*60)
		r.expect(t, data, noon-30, noon+4*60-1, nil)
	})

	t.Run("a brand-new service that stops: no history, no alert", func(t *testing.T) {
		t.Parallel()
		data := series("pay", stretch{day + 9*60, 0}, stretch{3 * 60, 6}, stretch{4 * 60, 0}) + othersAllDay(noon+4*60)
		r.expect(t, data, noon-30, noon+4*60-1, nil)
	})

	t.Run("a brand-new install: nothing fires, not even when everything stops", func(t *testing.T) {
		t.Parallel()
		data := series("pay", stretch{9 * 60, 0}, stretch{3 * 60, 6}, stretch{4 * 60, 0}) +
			series("web", stretch{9 * 60, 0}, stretch{4 * 60, 60}, stretch{3 * 60, pipelineDown}) +
			series("cart", stretch{9 * 60, 0}, stretch{4 * 60, 30}, stretch{3 * 60, pipelineDown})
		r.expect(t, data, 9*60, 16*60-1, nil)
	})

	t.Run("the whole pipeline stops: only the system alert", func(t *testing.T) {
		t.Parallel()
		data := series("pay", stretch{noon, 6}, stretch{4 * 60, pipelineDown}) +
			series("web", stretch{noon, 60}, stretch{4 * 60, pipelineDown}) +
			series("cart", stretch{noon, 30}, stretch{4 * 60, pipelineDown})
		r.expect(t, data, noon-30, noon+4*60-1, map[string][]firing{
			"system no data": {{noon + 3, -1}},
		})
	})

	t.Run("collector restart re-creates every series under a new label: nothing", func(t *testing.T) {
		t.Parallel()
		var data string
		for _, s := range []struct {
			name string
			rate float64
		}{{"pay", 6}, {"web", 60}, {"cart", 30}} {
			data += fmt.Sprintf("  calls{service_name=%q, span_kind=\"SPAN_KIND_SERVER\", collector=\"a\"} %s\n", s.name,
				counter(stretch{noon, s.rate}, stretch{3 * 60, pipelineDown}))
			data += fmt.Sprintf("  calls{service_name=%q, span_kind=\"SPAN_KIND_SERVER\", collector=\"b\"} %s\n", s.name,
				counter(stretch{noon + 1, pipelineDown}, stretch{3*60 - 1, s.rate}))
		}
		r.expect(t, data, noon-30, noon+3*60-1, nil)
	})
}

// A weekday system: idle on Saturday although Friday was busy, and with a week
// of history the same day a week ago decides. Day 0 is a Monday.
func TestNoDataWeekdaySystem(t *testing.T) {
	t.Parallel()
	r := newPayRules(t)
	week := func(perMinute float64) []stretch {
		return append(daily(5, 9, 19, perMinute), stretch{2 * day, 0})
	}
	var pay, web, cart []stretch
	for range 2 {
		pay, web, cart = append(pay, week(6)...), append(web, week(60)...), append(cart, week(30)...)
	}
	monday := 14 * day
	web, cart = append(web, daily(1, 9, 19, 60)...), append(cart, daily(1, 9, 19, 30)...)
	pay = append(pay, stretch{9 * 60, 0}, stretch{5 * 60, 6}, stretch{10 * 60, 0})
	data := series("pay", pay...) + series("web", web...) + series("cart", cart...)

	t.Run("second Saturday: nothing, although Friday had traffic", func(t *testing.T) {
		t.Parallel()
		r.expect(t, data, 12*day+8*60, 12*day+20*60, nil)
	})
	t.Run("third Monday: a stop at 14:00 alerts", func(t *testing.T) {
		t.Parallel()
		stop := monday + 14*60
		r.expect(t, data, stop-30, stop+3*60, map[string][]firing{
			"requests stopped": {{stop + 2, -1}},
			"no data alone":    {{stop + 5, -1}},
		})
	})
}
