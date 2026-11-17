// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
)

// Matchers builds a PromQL label selector. Values are quoted with the same
// rules Prometheus itself uses, so arbitrary label values are safe.
type Matchers []*labels.Matcher

// Eq adds label="value".
func (m Matchers) Eq(name, value string) Matchers {
	return append(m, labels.MustNewMatcher(labels.MatchEqual, name, value))
}

// Re adds label=~"regex". An empty regex list adds nothing.
func (m Matchers) Re(name string, alternatives ...string) Matchers {
	if len(alternatives) == 0 {
		return m
	}
	return append(m, labels.MustNewMatcher(labels.MatchRegexp, name, strings.Join(alternatives, "|")))
}

// NotRe adds label!~"regex". An empty regex list adds nothing.
func (m Matchers) NotRe(name string, alternatives ...string) Matchers {
	if len(alternatives) == 0 {
		return m
	}
	return append(m, labels.MustNewMatcher(labels.MatchNotRegexp, name, strings.Join(alternatives, "|")))
}

// Selector renders metric{matchers}.
func (m Matchers) Selector(metric string) string {
	parts := make([]string, len(m))
	for i, x := range m {
		parts[i] = x.String()
	}
	return metric + "{" + strings.Join(parts, ", ") + "}"
}

// clone returns a copy that can be extended without aliasing.
func (m Matchers) clone() Matchers { return append(Matchers(nil), m...) }

// by renders "by (a, b)".
func by(lbls ...string) string { return "by (" + strings.Join(lbls, ", ") + ")" }

// num renders a float without exponent noise.
func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// dur renders a duration in Prometheus notation.
func dur(d time.Duration) string { return model.Duration(d).String() }

// ParseDuration parses a Prometheus duration, returning def for an empty string.
func ParseDuration(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := model.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return time.Duration(d), nil
}

// FormatDuration renders a duration in Prometheus notation (5m, 1h30m).
func FormatDuration(d time.Duration) string { return dur(d) }

// FormatFloat renders a float the way the templates do (no exponent).
func FormatFloat(f float64) string { return num(f) }

// QuoteValues escapes exact values for use as alternatives in a regex matcher.
func QuoteValues(values []string) []string { return quoteAll(values) }
