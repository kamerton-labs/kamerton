// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/rulefmt"
	"github.com/prometheus/prometheus/promql/parser"
)

// Each custom rule gets its own alert name so runbook links and silences can address it.
const AlertCustomPrefix = "KamertonCustom"

const SignalCustom Signal = "custom"

const LabelRule = "kamerton_rule"

type CustomCondition struct {
	Metric string
	// Labels are exact-match selectors.
	Labels map[string]string
	// Function: "" or "none" (value as is), "rate" or "increase".
	Function string
	// Aggregation: max (default), min, avg, sum or count.
	Aggregation string
	By          []string
	// Operator: gt, gte, lt, lte, eq, ne.
	Operator  string
	Threshold float64
	// Window for rate/increase; 5m when zero.
	Window time.Duration
}

var operatorSymbols = map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<=", "eq": "==", "ne": "!="}

func OperatorSymbol(op string) string {
	if s, ok := operatorSymbols[op]; ok {
		return s
	}
	return ">"
}

func (c CustomCondition) Expr() string {
	names := make([]string, 0, len(c.Labels))
	for k := range c.Labels {
		names = append(names, k)
	}
	sort.Strings(names)
	m := Matchers{}
	for _, k := range names {
		m = m.Eq(k, c.Labels[k])
	}
	inner := m.Selector(c.Metric)
	if len(c.Labels) == 0 {
		inner = c.Metric
	}
	switch c.Function {
	case "rate", "increase":
		w := c.Window
		if w <= 0 {
			w = 5 * time.Minute
		}
		inner = fmt.Sprintf("%s(%s[%s])", c.Function, inner, dur(w))
	}
	agg := c.Aggregation
	if agg == "" {
		agg = "max"
	}
	group := ""
	if len(c.By) > 0 {
		group = " " + by(c.By...)
	}
	return fmt.Sprintf("%s%s (%s) %s %s", agg, group, inner, OperatorSymbol(c.Operator), num(c.Threshold))
}

type CustomParams struct {
	Meta
	// Name is the rule identifier (lowercase, dashes).
	Name  string
	Title string
	// Expr is the PromQL expression; when empty, Condition is rendered.
	Expr      string
	Condition *CustomCondition
	Unit      string
}

func CustomAlertName(name string) string {
	var b strings.Builder
	b.WriteString(AlertCustomPrefix)
	upper := true
	for _, r := range name {
		switch {
		case r == '-':
			upper = true
		case upper && r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
			upper = false
		default:
			b.WriteRune(r)
			upper = false
		}
	}
	return b.String()
}

func Custom(p CustomParams) rulefmt.Rule {
	expr := p.Expr
	threshold := ""
	unit := ""
	if p.Unit != "" {
		unit = " " + p.Unit
	}
	if expr == "" && p.Condition != nil {
		expr = p.Condition.Expr()
		threshold = OperatorSymbol(p.Condition.Operator) + " " + num(p.Condition.Threshold) + unit
	}
	r := p.rule(CustomAlertName(p.Name), SignalCustom, expr, map[string]string{LabelRule: p.Name})
	p.annotate(&r,
		fmt.Sprintf("%s: %s", p.Title, p.Service),
		fmt.Sprintf(`Custom rule %s: the value is {{ $value | printf "%%.4g" }}%s.`, p.Name, unit))
	r.Annotations[AnnotationTitle] = p.Title
	r.Annotations[AnnotationValue] = `{{ $value | printf "%.4g" }}` + unit
	if threshold != "" {
		r.Annotations[AnnotationThreshold] = threshold
	}
	return r
}

// ValidateExpr also requires an instant vector, which an alerting rule needs.
func ValidateExpr(expr string) error {
	e, err := parser.NewParser(parser.Options{}).ParseExpr(expr)
	if err != nil {
		return err
	}
	if t := e.Type(); t != parser.ValueTypeVector {
		return fmt.Errorf("the expression returns a %s, but an alert needs a vector (a list of series)", t)
	}
	return nil
}
