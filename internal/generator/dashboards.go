// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

// The Grafana dashboards of a system, built from its description like its rules, so a panel
// shows exactly what is alerted on. Five of them, each for one question:
//
//	overview  is everything all right, which alerts fire now (the map is Kamerton's Now screen)
//	services  each service: requests, errors, latency p50/p95/p99, memory, CPU, restarts
//	queues    write and read rates and consumer lag of each queue
//	infra     nodes and the pods of the system: CPU, memory, restarts, readiness
//	slo       availability against the objective, error budget left, burn rate
//
// Every service panel links to the failed requests of its time range in Tempo; a trace links
// to its logs through the Tempo data source (tracesToLogsV2 in the chart).

// Data source UIDs of the bundled Grafana (deploy/helm/kamerton/values.yaml).
const (
	TempoDatasourceUID = "tempo"
	LokiDatasourceUID  = "loki"
)

// Node metrics of node-exporter: the names are the exporter's own.
const (
	nodeCPUMetric       = "node_cpu_seconds_total"
	nodeMemAvailMetric  = "node_memory_MemAvailable_bytes"
	nodeMemTotalMetric  = "node_memory_MemTotal_bytes"
	nodeInstanceLabel   = "instance"
	kafkaGroupOffsetPfx = "kafka_consumer_group_offset"
	kafkaTopicOffsetPfx = "kafka_partition_current_offset"
)

type board struct {
	g      *gen
	panels []any
	id     int
	x, y   int
	rowH   int
}

var promDS = map[string]any{"type": "prometheus", "uid": "${datasource}"}

func (b *board) next() int { b.id++; return b.id }

// place puts a panel of width w and height h after the previous one, wrapping at 24 columns.
func (b *board) place(p map[string]any, w, h int) {
	if b.x+w > 24 {
		b.x, b.y = 0, b.y+b.rowH
		b.rowH = 0
	}
	p["id"] = b.next()
	p["gridPos"] = map[string]any{"x": b.x, "y": b.y, "w": w, "h": h}
	b.x += w
	b.rowH = max(b.rowH, h)
	b.panels = append(b.panels, p)
}

// row starts a titled row on a line of its own.
func (b *board) row(title string) {
	if b.x > 0 {
		b.x, b.y = 0, b.y+b.rowH
		b.rowH = 0
	}
	b.panels = append(b.panels, map[string]any{"id": b.next(), "type": "row", "title": title, "collapsed": false,
		"gridPos": map[string]any{"x": 0, "y": b.y, "w": 24, "h": 1}})
	b.y++
}

type target struct {
	expr, legend string
	instant      bool
}

func targets(ts ...target) []any {
	out := make([]any, 0, len(ts))
	for i, t := range ts {
		m := map[string]any{"refId": string(rune('A' + i)), "expr": t.expr, "legendFormat": t.legend, "datasource": promDS}
		if t.instant {
			m["instant"], m["range"], m["format"] = true, false, "table"
		}
		out = append(out, m)
	}
	return out
}

func timeseries(title, unit string, ts ...target) map[string]any {
	return map[string]any{
		"type": "timeseries", "title": title, "datasource": promDS, "targets": targets(ts...),
		"fieldConfig": map[string]any{"defaults": map[string]any{"unit": unit, "min": 0, "custom": map[string]any{"fillOpacity": 0, "lineWidth": 1}}, "overrides": []any{}},
		"options":     map[string]any{"legend": map[string]any{"displayMode": "list", "placement": "bottom"}, "tooltip": map[string]any{"mode": "multi"}},
	}
}

// thresholdLine draws a dashed line at v on a timeseries panel.
func thresholdLine(p map[string]any, v float64, color string) map[string]any {
	d := p["fieldConfig"].(map[string]any)["defaults"].(map[string]any)
	d["thresholds"] = map[string]any{"mode": "absolute", "steps": []any{map[string]any{"color": "transparent", "value": nil}, map[string]any{"color": color, "value": v}}}
	d["custom"].(map[string]any)["thresholdsStyle"] = map[string]any{"mode": "dashed"}
	return p
}

func stat(title, unit string, steps []any, ts ...target) map[string]any {
	return map[string]any{
		"type": "stat", "title": title, "datasource": promDS, "targets": targets(ts...),
		"fieldConfig": map[string]any{"defaults": map[string]any{"unit": unit, "decimals": 2,
			"thresholds": map[string]any{"mode": "absolute", "steps": steps}}, "overrides": []any{}},
		"options": map[string]any{"colorMode": "value", "graphMode": "none", "reduceOptions": map[string]any{"calcs": []any{"lastNotNull"}}},
	}
}

func steps(base string, levels ...any) []any {
	out := []any{map[string]any{"color": base, "value": nil}}
	for i := 0; i+1 < len(levels); i += 2 {
		out = append(out, map[string]any{"color": levels[i+1], "value": levels[i]})
	}
	return out
}

func text(title, markdown string) map[string]any {
	return map[string]any{"type": "text", "title": title, "options": map[string]any{"mode": "markdown", "content": markdown}}
}

// links adds panel links: the failed requests of the panel's range in Tempo, and the service in
// Kamerton when its address is known.
func (b *board) links(p map[string]any, svc *v1alpha1.Service) map[string]any {
	var ls []any
	if b.g.tel.Spans.CallsMetric != description.DisabledMetric {
		ls = append(ls, map[string]any{"title": b.g.word("failedTraces"), "url": exploreTraces(fmt.Sprintf(`{resource.service.name=%q && status=error}`, description.TelemetryName(svc))), "targetBlank": false})
	}
	if base := strings.TrimRight(b.g.opts.RunbookBaseURL, "/"); base != "" {
		ls = append(ls, map[string]any{"title": b.g.word("inKamerton"), "url": fmt.Sprintf("%s/systems/%s/services/%s", base, url.PathEscape(b.g.sd.Name), url.PathEscape(svc.Name)), "targetBlank": true})
	}
	if len(ls) > 0 {
		p["links"] = ls
	}
	return p
}

// exploreTraces is a Grafana Explore link to a TraceQL search over the range the panel shows:
// Grafana fills ${__from} and ${__to} when the link is followed.
func exploreTraces(traceQL string) string {
	panes := map[string]any{"a": map[string]any{
		"datasource": TempoDatasourceUID,
		"queries":    []any{map[string]any{"refId": "A", "datasource": map[string]any{"type": "tempo", "uid": TempoDatasourceUID}, "queryType": "traceql", "query": traceQL, "limit": 20}},
		"range":      map[string]any{"from": "FROMX", "to": "TOX"},
	}}
	j, _ := json.Marshal(panes)
	enc := url.QueryEscape(string(j))
	enc = strings.Replace(enc, "FROMX", "${__from}", 1)
	enc = strings.Replace(enc, "TOX", "${__to}", 1)
	return "/explore?schemaVersion=1&orgId=1&panes=" + enc
}

// dashboard wraps panels with the variables, the time range and links to the system's other
// dashboards.
func (g *gen) wrap(kind, title string, b *board) ([]byte, error) {
	uid := "kamerton-" + g.sd.Name
	if kind != "" {
		uid += "-" + kind
	}
	if len(uid) > 40 {
		uid = uid[:40]
	}
	tag := "kamerton-" + g.sd.Name
	d := map[string]any{
		"uid":           uid,
		"title":         "Kamerton / " + g.sd.Name + " / " + title,
		"tags":          []any{"kamerton", "generated", tag},
		"editable":      false,
		"schemaVersion": 39,
		"time":          map[string]any{"from": "now-6h", "to": "now"},
		"refresh":       "1m",
		"links":         []any{map[string]any{"type": "dashboards", "tags": []any{tag}, "asDropdown": false, "keepTime": true, "title": g.sd.Name}},
		"templating": map[string]any{"list": []any{map[string]any{
			"name": "datasource", "label": "Prometheus", "type": "datasource", "query": "prometheus",
		}}},
		"panels": b.panels,
	}
	return json.MarshalIndent(d, "", "  ")
}

func (g *gen) dashboards() (map[string][]byte, error) {
	out := map[string][]byte{}
	add := func(file string, data []byte, err error) error {
		if err != nil {
			return err
		}
		if data != nil {
			out[file] = data
		}
		return nil
	}
	name := g.sd.Name
	for _, d := range []struct {
		file string
		f    func() ([]byte, error)
	}{
		{"kamerton-" + name + ".json", g.servicesBoard},
		{"kamerton-" + name + "-overview.json", g.overviewBoard},
		{"kamerton-" + name + "-queues.json", g.queuesBoard},
		{"kamerton-" + name + "-infra.json", g.infraBoard},
		{"kamerton-" + name + "-slo.json", g.sloBoard},
	} {
		data, err := d.f()
		if err := add(d.file, data, err); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (g *gen) rate() string { return rules.FormatDuration(g.window) }

func (g *gen) requestSel(svc *v1alpha1.Service) (rules.Matchers, rules.Matchers) {
	t := g.spanTarget(svc)
	sel := rules.Matchers{}.Eq(t.ServiceLabel, t.ServiceValue).Re(t.SpanKindLabel, rules.QuoteValues(t.SpanKinds)...).NotRe(t.OperationLabel, t.ExcludeOperations...)
	errSel := append(append(rules.Matchers{}, sel...), rules.Matchers{}.Eq(t.StatusCodeLabel, t.ErrorStatusValue)...)
	return sel, errSel
}

func (g *gen) errorRatio(svc *v1alpha1.Service, w string) string {
	sel, errSel := g.requestSel(svc)
	m := g.tel.Spans.CallsMetric
	return fmt.Sprintf("(sum(rate(%[1]s[%[3]s])) or 0 * sum(rate(%[2]s[%[3]s]))) / sum(rate(%[2]s[%[3]s]))", errSel.Selector(m), sel.Selector(m), w)
}

func (g *gen) latency(svc *v1alpha1.Service, q float64, w string) string {
	sel, _ := g.requestSel(svc)
	expr := fmt.Sprintf("histogram_quantile(%s, sum by (le) (rate(%s[%s])))", rules.FormatFloat(q), sel.Selector(g.tel.Spans.DurationBucketMetric), w)
	if g.tel.Spans.DurationUnit == "s" {
		expr = "1000 * " + expr
	}
	return expr
}

func (g *gen) requests(svc *v1alpha1.Service, w string) string {
	sel, _ := g.requestSel(svc)
	return fmt.Sprintf("sum(rate(%s[%s]))", sel.Selector(g.tel.Spans.CallsMetric), w)
}

func (g *gen) hasRequests(svc *v1alpha1.Service) bool {
	return hasRequestRules(svc) && g.tel.Spans.CallsMetric != description.DisabledMetric
}

func (g *gen) podSel(svc *v1alpha1.Service) (rules.PodTarget, rules.Matchers) {
	pt := g.podTarget(svc)
	sel := rules.Matchers{}
	if pt.Namespace != "" {
		sel = sel.Eq(pt.NamespaceLabel, pt.Namespace)
	}
	return pt, sel.Re(pt.PodLabel, pt.PodRegex)
}

func off(m string) bool { return m == "" || m == description.DisabledMetric }

func (g *gen) servicesBoard() ([]byte, error) {
	b := &board{g: g}
	w := g.rate()
	for i := range g.spec.Services {
		svc := &g.spec.Services[i]
		b.row(svc.Name)
		if g.hasRequests(svc) {
			sel, _ := g.requestSel(svc)
			op := g.tel.Spans.OperationLabel
			b.place(b.links(timeseries(g.word("requests"), "reqps", target{expr: fmt.Sprintf("sum by (%s) (rate(%s[%s]))", op, sel.Selector(g.tel.Spans.CallsMetric), w), legend: "{{" + op + "}}"}), svc), 8, 8)
			b.place(b.links(timeseries(g.word("errors"), "percentunit", target{expr: g.errorRatio(svc, w), legend: g.word("errors")}), svc), 8, 8)
			if !off(g.tel.Spans.DurationBucketMetric) {
				b.place(b.links(timeseries(g.word("latency"), "ms",
					target{expr: g.latency(svc, 0.5, w), legend: "p50"},
					target{expr: g.latency(svc, 0.95, w), legend: "p95"},
					target{expr: g.latency(svc, 0.99, w), legend: "p99"}), svc), 8, 8)
			}
		}
		pt, psel := g.podSel(svc)
		if !off(pt.MemoryMetric) {
			mem := []target{{expr: fmt.Sprintf("max by (%s) (%s)", pt.PodLabel, psel.Selector(pt.MemoryMetric)), legend: "{{" + pt.PodLabel + "}}"}}
			if !off(pt.LimitsMetric) {
				mem = append(mem, target{expr: fmt.Sprintf("max by (%s) (%s)", pt.PodLabel, pt.Limit("memory")), legend: g.word("limit") + " {{" + pt.PodLabel + "}}"})
			}
			b.place(timeseries(g.word("memory"), "bytes", mem...), 8, 8)
			if !off(pt.CPUMetric) {
				b.place(timeseries(g.word("cpu"), "short", target{expr: fmt.Sprintf("max by (%s) (rate(%s[%s]))", pt.PodLabel, psel.Selector(pt.CPUMetric), w), legend: "{{" + pt.PodLabel + "}}"}), 8, 8)
			}
			if !off(pt.RestartsMetric) {
				b.place(timeseries(g.word("restarts"), "short", target{expr: fmt.Sprintf("sum by (%s) (increase(%s[1h]))", pt.PodLabel, psel.Selector(pt.RestartsMetric)), legend: "{{" + pt.PodLabel + "}}"}), 8, 8)
			}
		}
	}
	return g.wrap("", g.word("boardServices"), b)
}

func (g *gen) overviewBoard() ([]byte, error) {
	b := &board{g: g}
	sys := rules.Matchers{}.Eq(LabelSystem, g.sd.Name).Eq(rules.LabelManaged, "true").Eq("alertstate", "firing")
	b.place(stat(g.word("firingCritical"), "none", steps("green", 1, "red"), target{expr: fmt.Sprintf("count(%s) or vector(0)", append(append(rules.Matchers{}, sys...), rules.Matchers{}.Eq(rules.LabelSeverity, string(v1alpha1.SeverityCritical))...).Selector("ALERTS"))}), 6, 5)
	b.place(stat(g.word("firingWarning"), "none", steps("green", 1, "orange"), target{expr: fmt.Sprintf("count(%s) or vector(0)", append(append(rules.Matchers{}, sys...), rules.Matchers{}.Eq(rules.LabelSeverity, string(v1alpha1.SeverityWarning))...).Selector("ALERTS"))}), 6, 5)
	md := g.word("mapText")
	if base := strings.TrimRight(g.opts.RunbookBaseURL, "/"); base != "" {
		md = fmt.Sprintf("**[%s](%s/systems/%s)**\n\n%s", g.word("mapLink"), base, url.PathEscape(g.sd.Name), g.word("mapText"))
	}
	b.place(text(g.word("cause"), md), 12, 5)

	// The map of calls as Tempo counts them from the traces (traces_service_graph_*), the
	// services of this system only.
	names := make([]string, 0, len(g.spec.Services))
	for i := range g.spec.Services {
		names = append(names, regexp.QuoteMeta(description.TelemetryName(&g.spec.Services[i])))
	}
	tempo := map[string]any{"type": "tempo", "uid": TempoDatasourceUID}
	b.place(map[string]any{
		"type": "nodeGraph", "title": g.word("map"), "datasource": tempo,
		"targets": []any{map[string]any{"refId": "A", "datasource": tempo, "queryType": "serviceMap",
			"serviceMapQuery": fmt.Sprintf(`{server=~"%s"}`, strings.Join(names, "|"))}},
		"options": map[string]any{},
	}, 24, 14)

	// One table: a row per service, its numbers now; each query names its service in a label.
	var ts []target
	for i := range g.spec.Services {
		svc := &g.spec.Services[i]
		if !g.hasRequests(svc) {
			continue
		}
		name := fmt.Sprintf("%q", svc.Name)
		ts = append(ts,
			target{expr: fmt.Sprintf(`label_replace(%s, "service", %s, "", "")`, g.requests(svc, w5), name), instant: true},
			target{expr: fmt.Sprintf(`label_replace(%s, "service", %s, "", "")`, g.errorRatio(svc, w5), name), instant: true},
		)
		if !off(g.tel.Spans.DurationBucketMetric) {
			ts = append(ts, target{expr: fmt.Sprintf(`label_replace(%s, "service", %s, "", "")`, g.latency(svc, 0.95, w5), name), instant: true})
		}
	}
	if len(ts) > 0 {
		b.place(serviceTable(g, ts), 14, 14)
	}
	alerts := map[string]any{
		"type": "table", "title": g.word("alertsNow"), "datasource": promDS,
		"targets": targets(target{expr: sys.Selector("ALERTS"), instant: true}),
		"transformations": []any{map[string]any{"id": "organize", "options": map[string]any{
			"excludeByName": map[string]any{"Time": true, "Value": true, "__name__": true, "alertstate": true, LabelSystem: true, rules.LabelManaged: true, "job": true, "instance": true, rules.LabelRule: true, "signal": true, LabelEnvironment: true},
			"indexByName":   map[string]any{rules.LabelSeverity: 0, rules.LabelService: 1, "alertname": 2},
		}}},
		"options": map[string]any{"showHeader": true, "footer": map[string]any{"show": false}},
	}
	b.place(alerts, 10, 14)
	return g.wrap("overview", g.word("boardOverview"), b)
}

const w5 = "5m"

// serviceTable joins the per-service queries into one row per service: requests, errors, p95.
func serviceTable(g *gen, ts []target) map[string]any {
	return map[string]any{
		"type": "table", "title": g.word("servicesNow"), "datasource": promDS, "targets": targets(ts...),
		"transformations": []any{
			map[string]any{"id": "merge", "options": map[string]any{}},
			map[string]any{"id": "groupBy", "options": map[string]any{"fields": map[string]any{
				"service":  map[string]any{"aggregations": []any{}, "operation": "groupby"},
				"Value #A": map[string]any{"aggregations": []any{"max"}, "operation": "aggregate"},
			}}},
		},
		"fieldConfig": map[string]any{"defaults": map[string]any{}, "overrides": []any{}},
		"options":     map[string]any{"showHeader": true, "footer": map[string]any{"show": false}, "sortBy": []any{map[string]any{"displayName": "service"}}},
	}
}

func (g *gen) queuesBoard() ([]byte, error) {
	b := &board{g: g}
	w := g.rate()
	for i := range g.spec.Services {
		svc := &g.spec.Services[i]
		qt, ok := QueueTarget(g.sd, svc)
		if !ok {
			continue
		}
		b.row(svc.Name + " · " + qt.Topic)
		topic := rules.Matchers{}.Eq(qt.TopicLabel, qt.Topic)
		groups := append(append(rules.Matchers{}, topic...), rules.Matchers{}.Re(qt.GroupLabel, rules.QuoteValues(qt.Groups)...)...)
		// Messages written: the topic's end offset grows by one per message. Its metric is the
		// group offset's sibling in the same exporter.
		if strings.HasPrefix(qt.OffsetMetric, kafkaGroupOffsetPfx) {
			written := kafkaTopicOffsetPfx + strings.TrimPrefix(qt.OffsetMetric, kafkaGroupOffsetPfx)
			b.place(timeseries(g.word("written"), "short", target{expr: fmt.Sprintf("sum(rate(%s[%s]))", topic.Selector(written), w), legend: qt.Topic}), 8, 8)
		}
		if !off(qt.OffsetMetric) {
			b.place(timeseries(g.word("read"), "short", target{expr: fmt.Sprintf("sum by (%[1]s) (rate(%[2]s[%[3]s]))", qt.GroupLabel, groups.Selector(qt.OffsetMetric), w), legend: "{{" + qt.GroupLabel + "}}"}), 8, 8)
		}
		lag := timeseries(g.word("lag"), "short", target{expr: fmt.Sprintf("max by (%s) (%s)", qt.GroupLabel, groups.Selector(qt.LagMetric)), legend: "{{" + qt.GroupLabel + "}}"})
		if svc.Queue.MaxLag > 0 {
			lag = thresholdLine(lag, svc.Queue.MaxLag, "red")
		}
		b.place(lag, 8, 8)
	}
	if len(b.panels) == 0 {
		return nil, nil
	}
	return g.wrap("queues", g.word("boardQueues"), b)
}

func (g *gen) infraBoard() ([]byte, error) {
	b := &board{g: g}
	w := g.rate()
	b.row(g.word("nodes"))
	b.place(timeseries(g.word("nodeCPU"), "percentunit", target{expr: fmt.Sprintf(`1 - avg by (%[1]s) (rate(%[2]s{mode="idle"}[%[3]s]))`, nodeInstanceLabel, nodeCPUMetric, w), legend: "{{" + nodeInstanceLabel + "}}"}), 12, 8)
	b.place(timeseries(g.word("nodeMemory"), "percentunit", target{expr: fmt.Sprintf(`1 - %s / %s`, nodeMemAvailMetric, nodeMemTotalMetric), legend: "{{" + nodeInstanceLabel + "}}"}), 12, 8)

	c := g.tel.Containers
	nsSet := map[string]bool{}
	var pods []string
	for i := range g.spec.Services {
		pt := g.podTarget(&g.spec.Services[i])
		if pt.Namespace != "" {
			nsSet[pt.Namespace] = true
		}
		pods = append(pods, pt.PodRegex)
	}
	if off(c.MemoryMetric) {
		return g.wrap("infra", g.word("boardInfra"), b)
	}
	sel := rules.Matchers{}
	if len(nsSet) > 0 {
		nss := make([]string, 0, len(nsSet))
		for ns := range nsSet {
			nss = append(nss, regexp.QuoteMeta(ns))
		}
		sort.Strings(nss)
		sel = sel.Re(c.NamespaceLabel, nss...)
	}
	sel = sel.Re(c.PodLabel, pods...)
	b.row(g.word("pods"))
	b.place(timeseries(g.word("podMemory"), "bytes", target{expr: fmt.Sprintf("topk(10, max by (%[1]s) (%[2]s))", c.PodLabel, sel.Selector(c.MemoryMetric)), legend: "{{" + c.PodLabel + "}}"}), 12, 9)
	if !off(c.CPUMetric) {
		b.place(timeseries(g.word("podCPU"), "short", target{expr: fmt.Sprintf("topk(10, max by (%[1]s) (rate(%[2]s[%[3]s])))", c.PodLabel, sel.Selector(c.CPUMetric), w), legend: "{{" + c.PodLabel + "}}"}), 12, 9)
	}
	if !off(c.RestartsMetric) {
		b.place(timeseries(g.word("restarts"), "short", target{expr: fmt.Sprintf("sum by (%[1]s) (increase(%[2]s[1h])) > 0", c.PodLabel, sel.Selector(c.RestartsMetric)), legend: "{{" + c.PodLabel + "}}"}), 12, 8)
	}
	if !off(c.ReadyMetric) {
		b.place(timeseries(g.word("notReady"), "short", target{expr: fmt.Sprintf(`sum by (%[1]s) (%[2]s) > 0`, c.PodLabel, append(append(rules.Matchers{}, sel...), rules.Matchers{}.Eq("condition", "false")...).Selector(c.ReadyMetric)), legend: "{{" + c.PodLabel + "}}"}), 12, 8)
	}
	return g.wrap("infra", g.word("boardInfra"), b)
}

func (g *gen) sloBoard() ([]byte, error) {
	b := &board{g: g}
	for i := range g.spec.Services {
		svc := &g.spec.Services[i]
		if svc.SLO == nil || !g.hasRequests(svc) || svc.SLO.Objective <= 0 || svc.SLO.Objective >= 100 {
			continue
		}
		window := mustDuration(string(svc.SLO.Window), builtinSLOWindow)
		win := rules.FormatDuration(window)
		obj := svc.SLO.Objective / 100
		budget := 1 - obj
		b.row(fmt.Sprintf("%s · %s %s%% / %s", svc.Name, g.word("goal"), rules.FormatFloat(svc.SLO.Objective), win))
		avail := fmt.Sprintf("1 - (%s)", g.errorRatio(svc, win))
		b.place(stat(g.word("availability"), "percentunit", steps("red", obj, "green"), target{expr: avail}), 6, 6)
		b.place(stat(g.word("budgetLeft"), "percentunit", steps("red", 0.0, "orange", 0.25, "green"), target{expr: fmt.Sprintf("1 - (%s) / %s", g.errorRatio(svc, win), rules.FormatFloat(budget))}), 6, 6)
		// The burn rate: how many times faster than the window allows the budget goes. 14.4 over
		// an hour spends 2% of a 30-day budget; 6 over six hours 5%: the usual paging pair.
		burn := timeseries(g.word("burn"), "short",
			target{expr: fmt.Sprintf("(%s) / %s", g.errorRatio(svc, "1h"), rules.FormatFloat(budget)), legend: "1h"},
			target{expr: fmt.Sprintf("(%s) / %s", g.errorRatio(svc, "6h"), rules.FormatFloat(budget)), legend: "6h"})
		b.place(b.links(thresholdLine(burn, 14.4, "red"), svc), 12, 6)
		if !off(g.tel.Spans.DurationBucketMetric) {
			lat := mergeLatency(g.defaults().Latency, svc.Latency)
			p := timeseries(g.word("latency"), "ms", target{expr: g.latency(svc, 0.95, g.rate()), legend: "p95"}, target{expr: g.latency(svc, 0.99, g.rate()), legend: "p99"})
			if lat.thresholdMs > 0 {
				p = thresholdLine(p, lat.thresholdMs, "red")
			}
			b.place(b.links(p, svc), 12, 7)
		}
		b.place(timeseries(g.word("availability1h"), "percentunit", target{expr: fmt.Sprintf("1 - (%s)", g.errorRatio(svc, "1h")), legend: svc.Name}), 12, 7)
	}
	if len(b.panels) == 0 {
		return nil, nil
	}
	return g.wrap("slo", g.word("boardSLO"), b)
}

var boardWords = map[string]map[string]string{
	"en": {
		"boardServices": "Services", "boardOverview": "Overview", "boardQueues": "Queues", "boardInfra": "Infrastructure", "boardSLO": "SLO",
		"requests": "Requests/s", "errors": "Error ratio", "latency": "Latency, ms", "memory": "Memory", "cpu": "CPU, cores", "restarts": "Restarts per hour", "limit": "limit",
		"failedTraces": "Failed requests in this range (Tempo)", "inKamerton": "Open in Kamerton",
		"firingCritical": "Critical alerts now", "firingWarning": "Warnings now", "map": "Map of calls (Tempo)", "cause": "Source of a failure",
		"mapLink": "Open the system in Kamerton", "mapText": "Which service is the cause and which only suffer from it: the Now screen of the system in Kamerton.",
		"servicesNow": "Services now (5m)", "alertsNow": "Alerts firing now",
		"written": "Messages written/s", "read": "Messages read/s", "lag": "Consumer lag",
		"nodes": "Nodes", "nodeCPU": "Node CPU", "nodeMemory": "Node memory used", "pods": "Pods of the system", "podMemory": "Memory, top 10", "podCPU": "CPU, top 10", "notReady": "Pods not ready",
		"goal": "goal", "availability": "Availability over the window", "budgetLeft": "Error budget left", "burn": "Error budget burn rate", "availability1h": "Availability, 1h",
	},
	"ru": {
		"boardServices": "Сервисы", "boardOverview": "Обзор", "boardQueues": "Очереди", "boardInfra": "Инфраструктура", "boardSLO": "SLO",
		"requests": "Запросов в секунду", "errors": "Доля ошибок", "latency": "Задержка, мс", "memory": "Память", "cpu": "CPU, ядер", "restarts": "Перезапусков за час", "limit": "лимит",
		"failedTraces": "Запросы с ошибкой за этот период (Tempo)", "inKamerton": "Открыть в Kamerton",
		"firingCritical": "Критичных тревог сейчас", "firingWarning": "Предупреждений сейчас", "map": "Карта вызовов (Tempo)", "cause": "Источник сбоя",
		"mapLink": "Открыть систему в Kamerton", "mapText": "Какой сервис причина, а какие только страдают из-за него, — на экране «Сейчас» системы в Kamerton.",
		"servicesNow": "Сервисы сейчас (5 мин)", "alertsNow": "Тревоги сейчас",
		"written": "Записано сообщений/с", "read": "Прочитано сообщений/с", "lag": "Отставание потребителей",
		"nodes": "Узлы", "nodeCPU": "CPU узлов", "nodeMemory": "Память узлов занята", "pods": "Поды системы", "podMemory": "Память, топ-10", "podCPU": "CPU, топ-10", "notReady": "Не готовые поды",
		"goal": "цель", "availability": "Доступность за окно", "budgetLeft": "Осталось бюджета ошибок", "burn": "Скорость расхода бюджета", "availability1h": "Доступность за час",
	},
}

func (g *gen) word(k string) string {
	if w, ok := boardWords[g.language()][k]; ok {
		return w
	}
	return boardWords["en"][k]
}
