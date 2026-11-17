// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package description loads and validates system descriptions, in their YAML
// file form or as SystemDescription custom resources.
package description

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	sjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/schema"
)

// Severity of a validation issue.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one validation problem.
type Issue struct {
	// Path is a JSON pointer into the document, for example /spec/services/2/dependsOn/0.
	Path     string   `json:"path"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
}

// Result is the outcome of validating a description document.
type Result struct {
	Valid    bool    `json:"valid"`
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
	// Document is the decoded description. It is set when the document passed
	// schema validation, even if semantic checks found errors.
	Document *v1alpha1.SystemDescription `json:"-"`
}

func (r *Result) add(is ...Issue) {
	for _, i := range is {
		if i.Severity == SeverityWarning {
			r.Warnings = append(r.Warnings, i)
		} else {
			r.Errors = append(r.Errors, i)
		}
	}
}

// LoadFile reads, validates and decodes a description file. It fails if the
// description has validation errors (warnings are ignored).
func LoadFile(path string) (*v1alpha1.SystemDescription, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	res := Validate(data)
	if !res.Valid {
		return nil, data, fmt.Errorf("description %s is invalid: %s", path, summarize(res.Errors))
	}
	return res.Document, data, nil
}

func summarize(issues []Issue) string {
	var b bytes.Buffer
	for i, is := range issues {
		if i > 0 {
			b.WriteString("; ")
		}
		if i == 5 {
			fmt.Fprintf(&b, "and %d more", len(issues)-5)
			break
		}
		fmt.Fprintf(&b, "%s: %s", displayPath(is.Path), is.Message)
	}
	return b.String()
}

func displayPath(p string) string {
	if p == "" {
		return "(document)"
	}
	return p
}

// Validate validates a YAML or JSON description document: syntax, JSON
// Schema, then semantic rules (references, regular expressions, durations).
func Validate(data []byte) Result {
	res := Result{Errors: []Issue{}, Warnings: []Issue{}}
	if len(bytes.TrimSpace(data)) == 0 {
		res.add(Issue{Message: "document is empty", Severity: SeverityError})
		return res
	}
	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		res.add(Issue{Message: fmt.Sprintf("YAML syntax: %v", err), Severity: SeverityError})
		return res
	}
	instance, err := sjsonschema.UnmarshalJSON(bytes.NewReader(jsonData))
	if err != nil {
		res.add(Issue{Message: fmt.Sprintf("decode: %v", err), Severity: SeverityError})
		return res
	}
	violations, err := schema.Validate(instance)
	if err != nil {
		res.add(Issue{Message: fmt.Sprintf("schema: %v", err), Severity: SeverityError})
		return res
	}
	for _, v := range violations {
		res.add(Issue{Path: v.Path, Message: v.Message, Severity: SeverityError})
	}
	if len(violations) > 0 {
		return res
	}

	var doc v1alpha1.SystemDescription
	dec := json.NewDecoder(bytes.NewReader(jsonData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		res.add(Issue{Message: fmt.Sprintf("decode: %v", err), Severity: SeverityError})
		return res
	}
	res.Document = &doc
	res.add(ValidateSpec(&doc.Spec)...)
	res.Valid = len(res.Errors) == 0
	return res
}

// ToYAML serializes a description to YAML in its file form (no status).
func ToYAML(sd *v1alpha1.SystemDescription) ([]byte, error) {
	doc := struct {
		APIVersion string                         `json:"apiVersion"`
		Kind       string                         `json:"kind"`
		Metadata   map[string]any                 `json:"metadata"`
		Spec       v1alpha1.SystemDescriptionSpec `json:"spec"`
	}{
		APIVersion: v1alpha1.APIVersion(),
		Kind:       v1alpha1.Kind,
		Metadata:   map[string]any{"name": sd.Name},
		Spec:       sd.Spec,
	}
	if sd.Namespace != "" {
		doc.Metadata["namespace"] = sd.Namespace
	}
	if len(sd.Labels) > 0 {
		doc.Metadata["labels"] = sd.Labels
	}
	ann := map[string]string{}
	for k, v := range sd.Annotations {
		// kubectl's copy of the whole object is noise in the file form.
		if k != "kubectl.kubernetes.io/last-applied-configuration" {
			ann[k] = v
		}
	}
	if len(ann) > 0 {
		doc.Metadata["annotations"] = ann
	}
	return yaml.Marshal(doc)
}
