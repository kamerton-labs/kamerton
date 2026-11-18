// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/tailmute"
)

func TestParseTailMute(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"":    tailmute.DefaultGrace, // a chart upgraded with --reuse-values passes nothing
		" ":   tailmute.DefaultGrace,
		"0":   0,
		"0s":  0,
		"5m":  5 * time.Minute,
		"90s": 90 * time.Second,
	} {
		got, err := parseTailMute(in)
		if err != nil || got != want {
			t.Errorf("parseTailMute(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseTailMute("soon"); err == nil {
		t.Error("a malformed duration must be an error")
	}
}

func TestDiffNamesWhatAChangeDoes(t *testing.T) {
	load := func(mutate func(*v1alpha1.SystemDescription)) map[string]rulefmt.Rule {
		t.Helper()
		sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			mutate(sd)
		}
		out, _, err := rulesByKey(sd)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := load(nil)
	after := load(func(sd *v1alpha1.SystemDescription) {
		for i := range sd.Spec.Services {
			if sd.Spec.Services[i].Name == "ad" {
				sd.Spec.Services[i].Critical = !sd.Spec.Services[i].Critical
			}
		}
	})

	var same strings.Builder
	if printDiff(&same, "otel-demo", before, before) || !strings.Contains(same.String(), "no rule changes") {
		t.Errorf("unchanged: %s", same.String())
	}
	var out strings.Builder
	if !printDiff(&out, "otel-demo", before, after) {
		t.Fatal("a change of severity must be reported")
	}
	text := out.String()
	if !strings.Contains(text, "ad · KamertonErrorRateHigh: severity ") || strings.Contains(text, "cart · KamertonErrorRateHigh") {
		t.Errorf("diff:\n%s", text)
	}
	var fresh strings.Builder
	if !printDiff(&fresh, "otel-demo", nil, after) || !strings.Contains(fresh.String(), "is new") {
		t.Errorf("new system: %s", fresh.String())
	}
}
