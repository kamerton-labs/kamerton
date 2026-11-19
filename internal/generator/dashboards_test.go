// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kamerton-labs/kamerton/internal/description"
)

func TestFiveDashboardsAnswerTheirQuestions(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(sd, Options{RunbookBaseURL: "https://kamerton.example"})
	if err != nil {
		t.Fatal(err)
	}
	type panel struct {
		Type    string `json:"type"`
		Title   string `json:"title"`
		Targets []struct {
			Expr            string `json:"expr"`
			QueryType       string `json:"queryType"`
			ServiceMapQuery string `json:"serviceMapQuery"`
		} `json:"targets"`
		Links []struct {
			URL string `json:"url"`
		} `json:"links"`
	}
	read := func(file string) (string, []panel) {
		t.Helper()
		raw, ok := b.Dashboards[file]
		if !ok {
			t.Fatalf("no %s in %v", file, len(b.Dashboards))
		}
		var d struct {
			UID    string  `json:"uid"`
			Panels []panel `json:"panels"`
			Links  []struct {
				Tags []string `json:"tags"`
			} `json:"links"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Links) != 1 || d.Links[0].Tags[0] != "kamerton-"+sd.Name {
			t.Errorf("%s: no links to the other dashboards: %+v", file, d.Links)
		}
		return d.UID, d.Panels
	}
	name := sd.Name
	if len(b.Dashboards) != 5 {
		t.Errorf("dashboards: %d", len(b.Dashboards))
	}

	// The services dashboard keeps its UID, so links to it still work.
	uid, ps := read("kamerton-" + name + ".json")
	if uid != "kamerton-"+name {
		t.Errorf("services UID: %s", uid)
	}
	var latency, linked bool
	for _, p := range ps {
		if len(p.Targets) == 3 && strings.HasPrefix(p.Targets[0].Expr, "histogram_quantile(0.5,") && strings.HasPrefix(p.Targets[2].Expr, "histogram_quantile(0.99,") {
			latency = true
		}
		for _, l := range p.Links {
			if strings.Contains(l.URL, "/explore?") && strings.Contains(l.URL, "${__from}") && strings.Contains(l.URL, "status%3Derror") {
				linked = true
			}
		}
	}
	if !latency || !linked {
		t.Errorf("services: p50/p95/p99 %v, link to failed traces %v", latency, linked)
	}

	_, ps = read("kamerton-" + name + "-overview.json")
	var graph bool
	for _, p := range ps {
		if p.Type == "nodeGraph" && p.Targets[0].QueryType == "serviceMap" && strings.Contains(p.Targets[0].ServiceMapQuery, "checkout") {
			graph = true
		}
	}
	if !graph {
		t.Error("overview: no map of calls")
	}

	_, ps = read("kamerton-" + name + "-queues.json")
	var written, lag bool
	for _, p := range ps {
		for _, tg := range p.Targets {
			written = written || strings.Contains(tg.Expr, "kafka_partition_current_offset")
			lag = lag || strings.Contains(tg.Expr, "lag")
		}
	}
	if !written || !lag {
		t.Errorf("queues: written %v, lag %v", written, lag)
	}

	_, ps = read("kamerton-" + name + "-infra.json")
	if len(ps) < 4 || !strings.Contains(ps[1].Targets[0].Expr, `mode="idle"`) {
		t.Errorf("infra: %+v", ps)
	}

	_, ps = read("kamerton-" + name + "-slo.json")
	var burn bool
	for _, p := range ps {
		for _, tg := range p.Targets {
			burn = burn || strings.Contains(tg.Expr, "[6h]")
		}
	}
	if !burn {
		t.Error("slo: no burn rate")
	}
}
