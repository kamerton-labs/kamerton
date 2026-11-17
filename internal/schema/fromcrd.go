// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
)

const SchemaID = "https://kamerton.dev/schemas/v1alpha1/systemdescription.schema.json"

// FromCRD converts the CRD into a draft-07 JSON Schema for the file form. It drops
// status and x-kubernetes-* extensions and forbids unknown properties so typos are reported.
func FromCRD(crdYAML []byte) ([]byte, error) {
	var crd map[string]any
	if err := yaml.Unmarshal(crdYAML, &crd); err != nil {
		return nil, fmt.Errorf("parse CRD: %w", err)
	}
	spec, _ := crd["spec"].(map[string]any)
	versions, _ := spec["versions"].([]any)
	var root map[string]any
	for _, v := range versions {
		vm, _ := v.(map[string]any)
		if vm["name"] != v1alpha1.Version {
			continue
		}
		s, _ := vm["schema"].(map[string]any)
		root, _ = s["openAPIV3Schema"].(map[string]any)
	}
	if root == nil {
		return nil, fmt.Errorf("CRD has no openAPIV3Schema for version %s", v1alpha1.Version)
	}

	props, _ := root["properties"].(map[string]any)
	if props == nil || props["spec"] == nil {
		return nil, fmt.Errorf("CRD schema has no spec property")
	}
	delete(props, "status")

	props["apiVersion"] = map[string]any{
		"description": "API version of the description.",
		"type":        "string",
		"enum":        []any{v1alpha1.APIVersion()},
	}
	props["kind"] = map[string]any{
		"description": "Kind of the description.",
		"type":        "string",
		"enum":        []any{v1alpha1.Kind},
	}
	props["metadata"] = map[string]any{
		"description": "Name and labels of the description. When applied to Kubernetes this is the object metadata.",
		"type":        "object",
		"required":    []any{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"description": "Name of the system.",
				"type":        "string",
				"pattern":     `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`,
				"maxLength":   253,
			},
			"namespace": map[string]any{
				"description": "Kubernetes namespace of the description.",
				"type":        "string",
			},
			"labels": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"annotations": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
		},
	}
	root["required"] = []any{"apiVersion", "kind", "metadata", "spec"}
	root["description"] = "Kamerton system description: services, thresholds, SLOs and notification routing."

	convert(root)

	root["$schema"] = "http://json-schema.org/draft-07/schema#"
	root["$id"] = SchemaID
	root["title"] = "SystemDescription"

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func convert(node any) {
	switch n := node.(type) {
	case map[string]any:
		for k := range n {
			if strings.HasPrefix(k, "x-kubernetes-") || k == "nullable" {
				delete(n, k)
			}
		}
		convertExclusive(n, "exclusiveMinimum", "minimum")
		convertExclusive(n, "exclusiveMaximum", "maximum")
		if n["type"] == "object" {
			if _, hasProps := n["properties"]; hasProps {
				if _, set := n["additionalProperties"]; !set {
					n["additionalProperties"] = false
				}
			}
		}
		for _, v := range n {
			convert(v)
		}
	case []any:
		for _, v := range n {
			convert(v)
		}
	}
}

// convertExclusive turns OpenAPI 3.0 {minimum: x, exclusiveMinimum: true}
// into draft-07 {exclusiveMinimum: x}.
func convertExclusive(n map[string]any, exclusiveKey, boundKey string) {
	ex, ok := n[exclusiveKey].(bool)
	if !ok {
		return
	}
	delete(n, exclusiveKey)
	if ex {
		if bound, ok := n[boundKey]; ok {
			n[exclusiveKey] = bound
			delete(n, boundKey)
		}
	}
}
