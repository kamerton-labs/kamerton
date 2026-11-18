// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"
	"time"

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
