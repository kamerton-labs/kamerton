// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"bytes"
	"errors"
	"io"
	"log/slog"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/rulefmt"
	"github.com/prometheus/prometheus/promql/parser"
	"go.yaml.in/yaml/v3"
)

// Render serializes rule groups to a Prometheus rule file.
func Render(groups rulefmt.RuleGroups) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(groups); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Validate parses a rule file with Prometheus' own rule file parser
// (model/rulefmt), which checks YAML structure, PromQL expressions, label
// names and annotation templates exactly as Prometheus does on load.
func Validate(ruleFile []byte) error {
	p := parser.NewParser(parser.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, errs := rulefmt.Parse(ruleFile, false, model.UTF8Validation, p, logger)
	return errors.Join(errs...)
}

// RenderAndValidate renders groups and validates the result.
func RenderAndValidate(groups rulefmt.RuleGroups) ([]byte, error) {
	out, err := Render(groups)
	if err != nil {
		return nil, err
	}
	if err := Validate(out); err != nil {
		return out, err
	}
	return out, nil
}

// Count returns the number of rules in groups.
func Count(groups rulefmt.RuleGroups) int {
	n := 0
	for _, g := range groups.Groups {
		n += len(g.Rules)
	}
	return n
}
