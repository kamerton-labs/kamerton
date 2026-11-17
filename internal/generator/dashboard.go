// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"encoding/json"
	"fmt"

	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

func (g *gen) dashboard() ([]byte, error) {
	s := g.tel.Spans
	w := rules.FormatDuration(g.window)
	ds := map[string]any{"type": "prometheus", "uid": "${datasource}"}
	var panels []any
	id := 1
	y := 0
	panel := func(title, expr, legend, unit string, x int) map[string]any {
		p := map[string]any{
			"id":         id,
			"type":       "timeseries",
			"title":      title,
			"datasource": ds,
			"gridPos":    map[string]any{"h": 8, "w": 8, "x": x, "y": y},
			"fieldConfig": map[string]any{
				"defaults":  map[string]any{"unit": unit},
				"overrides": []any{},
			},
			"targets": []any{map[string]any{"refId": "A", "expr": expr, "legendFormat": legend, "datasource": ds}},
		}
		id++
		return p
	}
	for i := range g.spec.Services {
		svc := &g.spec.Services[i]
		panels = append(panels, map[string]any{
			"id": id, "type": "row", "title": svc.Name, "collapsed": false,
			"gridPos": map[string]any{"h": 1, "w": 24, "x": 0, "y": y},
		})
		id++
		y++
		if hasRequestRules(svc) && s.CallsMetric != description.DisabledMetric {
			t := g.spanTarget(svc)
			sel := rules.Matchers{}.Eq(t.ServiceLabel, t.ServiceValue).Re(t.SpanKindLabel, rules.QuoteValues(t.SpanKinds)...).NotRe(t.OperationLabel, t.ExcludeOperations...)
			errSel := append(append(rules.Matchers{}, sel...), rules.Matchers{}.Eq(t.StatusCodeLabel, t.ErrorStatusValue)...)
			legend := fmt.Sprintf("{{%s}}", s.OperationLabel)
			panels = append(panels,
				panel("Requests/s", fmt.Sprintf("sum by (%s) (rate(%s[%s]))", s.OperationLabel, sel.Selector(s.CallsMetric), w), legend, "reqps", 0),
				panel("Error ratio", fmt.Sprintf("sum by (%[1]s) (rate(%[2]s[%[4]s])) / sum by (%[1]s) (rate(%[3]s[%[4]s]))",
					s.OperationLabel, errSel.Selector(s.CallsMetric), sel.Selector(s.CallsMetric), w), legend, "percentunit", 8),
			)
			unit := "ms"
			if s.DurationUnit == "s" {
				unit = "s"
			}
			q := mergeLatency(g.defaults().Latency, svc.Latency).quantile
			panels = append(panels, panel(fmt.Sprintf("Latency p%s", rules.FormatFloat(q*100)),
				fmt.Sprintf("histogram_quantile(%s, sum by (%s, le) (rate(%s[%s])))", rules.FormatFloat(q), s.OperationLabel, sel.Selector(s.DurationBucketMetric), w),
				legend, unit, 16))
			y += 8
		}
		pt := g.podTarget(svc)
		if pt.MemoryMetric != description.DisabledMetric {
			sel := rules.Matchers{}
			if pt.Namespace != "" {
				sel = sel.Eq(pt.NamespaceLabel, pt.Namespace)
			}
			sel = sel.Re(pt.PodLabel, pt.PodRegex)
			panels = append(panels,
				panel("Memory", fmt.Sprintf("max by (%s) (%s)", pt.PodLabel, sel.Selector(pt.MemoryMetric)), fmt.Sprintf("{{%s}}", pt.PodLabel), "bytes", 0),
				panel("CPU", fmt.Sprintf("max by (%s) (rate(%s[%s]))", pt.PodLabel, sel.Selector(pt.CPUMetric), w), fmt.Sprintf("{{%s}}", pt.PodLabel), "short", 8),
			)
			y += 8
		}
	}
	uid := "kamerton-" + g.sd.Name
	if len(uid) > 40 {
		uid = uid[:40]
	}
	d := map[string]any{
		"uid":           uid,
		"title":         "Kamerton / " + g.sd.Name,
		"tags":          []any{"kamerton", "generated"},
		"editable":      false,
		"schemaVersion": 39,
		"time":          map[string]any{"from": "now-6h", "to": "now"},
		"refresh":       "1m",
		"templating": map[string]any{"list": []any{map[string]any{
			"name": "datasource", "label": "Prometheus", "type": "datasource", "query": "prometheus",
		}}},
		"panels": panels,
	}
	return json.MarshalIndent(d, "", "  ")
}
