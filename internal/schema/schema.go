// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package schema holds the JSON Schema of the system description and validates
// documents against it.
//
// The schema file is generated (make generate) from the CRD, which in turn is
// generated from the Go types in api/v1alpha1. Do not edit it by hand.
package schema

//go:generate go run ../../hack/schemagen -crd ../../deploy/helm/kamerton/crds/kamerton.dev_systemdescriptions.yaml -out systemdescription.schema.json

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed systemdescription.schema.json
var schemaJSON []byte

// JSON returns the JSON Schema document.
func JSON() []byte { return schemaJSON }

// Violation is a single schema validation failure.
type Violation struct {
	// Path is a JSON pointer into the document, for example /spec/services/0/type.
	Path string `json:"path"`
	// Message is a human-readable description of the problem.
	Message string `json:"message"`
}

var (
	compileOnce sync.Once
	compiled    *jsonschema.Schema
	compileErr  error
)

func compiledSchema() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
		if err != nil {
			compileErr = fmt.Errorf("parse embedded schema: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(SchemaID, doc); err != nil {
			compileErr = err
			return
		}
		compiled, compileErr = c.Compile(SchemaID)
	})
	return compiled, compileErr
}

// Validate checks a decoded JSON document (as produced by
// jsonschema.UnmarshalJSON or encoding/json into any) against the schema.
// It returns nil when the document is valid.
func Validate(instance any) ([]Violation, error) {
	sch, err := compiledSchema()
	if err != nil {
		return nil, err
	}
	err = sch.Validate(instance)
	if err == nil {
		return nil, nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return nil, err
	}
	var out []Violation
	collect(ve.BasicOutput(), &out)
	if len(out) == 0 {
		out = append(out, Violation{Path: "", Message: ve.Error()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return dedupe(out), nil
}

func collect(u *jsonschema.OutputUnit, out *[]Violation) {
	if u == nil {
		return
	}
	if u.Error != nil && len(u.Errors) == 0 {
		*out = append(*out, Violation{Path: u.InstanceLocation, Message: u.Error.String()})
	}
	for i := range u.Errors {
		collect(&u.Errors[i], out)
	}
}

func dedupe(in []Violation) []Violation {
	seen := make(map[Violation]bool, len(in))
	out := in[:0]
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
