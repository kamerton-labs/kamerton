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

// Matchers builds a PromQL selector. Values are quoted the way Prometheus does,
// so any label value is safe.
type Matchers []*labels.Matcher

func (m Matchers) Eq(name, value string) Matchers {
	return append(m, labels.MustNewMatcher(labels.MatchEqual, name, value))
}

func (m Matchers) Re(name string, alternatives ...string) Matchers {
	if len(alternatives) == 0 {
		return m
	}
	return append(m, labels.MustNewMatcher(labels.MatchRegexp, name, strings.Join(alternatives, "|")))
}

func (m Matchers) NotRe(name string, alternatives ...string) Matchers {
	if len(alternatives) == 0 {
		return m
	}
	return append(m, labels.MustNewMatcher(labels.MatchNotRegexp, name, strings.Join(alternatives, "|")))
}

func (m Matchers) Selector(metric string) string {
	parts := make([]string, len(m))
	for i, x := range m {
		parts[i] = x.String()
	}
	return metric + "{" + strings.Join(parts, ", ") + "}"
}

func (m Matchers) clone() Matchers { return append(Matchers(nil), m...) }

func by(lbls ...string) string { return "by (" + strings.Join(lbls, ", ") + ")" }

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func dur(d time.Duration) string { return model.Duration(d).String() }

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

func FormatFloat(f float64) string { return num(f) }

func QuoteValues(values []string) []string { return quoteAll(values) }
