// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package deployer turns a generated bundle into Kubernetes objects and
// applies them with server-side apply:
//
//   - PrometheusRule (monitoring.coreos.com/v1) with the alerting rules;
//   - AlertmanagerConfig (monitoring.coreos.com/v1alpha1) with routes and
//     receivers, credentials referenced from Secrets;
//   - ConfigMaps with Grafana dashboards (picked up by the Grafana sidecar)
//     and runbook stubs.
//
// The bring-your-own mode (pushing to external Prometheus/Grafana APIs) is not
// implemented yet.
package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/kamerton-labs/kamerton/internal/generator"
)

// FieldOwner is the server-side apply field manager name.
const FieldOwner = "kamerton"

// Labels put on every managed object.
const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelSystem    = "kamerton.dev/system"
	LabelManaged   = "kamerton.dev/managed"
	// LabelGrafanaDashboard is the default label watched by the Grafana chart's dashboard sidecar.
	LabelGrafanaDashboard = "grafana_dashboard"
)

// Applier applies objects to a cluster.
type Applier interface {
	Apply(ctx context.Context, objs []*unstructured.Unstructured) error
}

// ExtraLabels are labels added to generated objects besides Kamerton's own
// (which always win): on every object, and on the PrometheusRule and the
// AlertmanagerConfig only, so that the right Prometheus Operator instance
// picks them up (labels of the system's set of programs).
type ExtraLabels struct {
	All                map[string]string
	Rule               map[string]string
	AlertmanagerConfig map[string]string
}

// Objects builds the Kubernetes objects for a bundle in namespace.
func Objects(b *generator.Bundle, namespace string, extraLabels map[string]string) ([]*unstructured.Unstructured, error) {
	return ObjectsWithLabels(b, namespace, ExtraLabels{All: extraLabels})
}

// ObjectsWithLabels builds the Kubernetes objects for a bundle in namespace
// with extra labels.
func ObjectsWithLabels(b *generator.Bundle, namespace string, extra ExtraLabels) ([]*unstructured.Unstructured, error) {
	base := fmt.Sprintf("kamerton-%s", b.System)
	own := map[string]any{LabelManagedBy: "kamerton", LabelSystem: b.System, LabelManaged: "true"}
	with := func(sets ...map[string]string) map[string]any {
		out := map[string]any{}
		for _, m := range sets {
			for k, v := range m {
				out[k] = v
			}
		}
		for k, v := range own {
			out[k] = v
		}
		return out
	}
	labels := with(extra.All)
	newObj := func(apiVersion, kind, name string, lbls map[string]any) *unstructured.Unstructured {
		meta := map[string]any{"name": name, "namespace": namespace, "labels": lbls}
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta}}
	}

	var out []*unstructured.Unstructured

	ruleSpec, err := yamlToJSONMap(b.RulesYAML)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	pr := newObj("monitoring.coreos.com/v1", "PrometheusRule", base, with(extra.All, extra.Rule))
	pr.Object["spec"] = ruleSpec
	out = append(out, pr)

	if b.AlertmanagerConfig != nil {
		spec, err := toJSONMap(b.AlertmanagerConfig)
		if err != nil {
			return nil, fmt.Errorf("alertmanager config: %w", err)
		}
		amc := newObj("monitoring.coreos.com/v1alpha1", "AlertmanagerConfig", base, with(extra.All, extra.AlertmanagerConfig))
		amc.Object["spec"] = spec
		out = append(out, amc)
	}

	if len(b.Dashboards) > 0 {
		dl := copyLabels(labels)
		dl[LabelGrafanaDashboard] = "1"
		cm := newObj("v1", "ConfigMap", base+"-dashboards", dl)
		data := map[string]any{}
		for _, k := range sortedKeys(b.Dashboards) {
			data[k] = string(b.Dashboards[k])
		}
		cm.Object["data"] = data
		out = append(out, cm)
	}

	if len(b.Runbooks) > 0 {
		cm := newObj("v1", "ConfigMap", base+"-runbooks", copyLabels(labels))
		data := map[string]any{}
		for _, k := range sortedKeys(b.Runbooks) {
			data[k] = b.Runbooks[k]
		}
		cm.Object["data"] = data
		out = append(out, cm)
	}
	return out, nil
}

// KubernetesApplier applies objects with server-side apply.
//
// TODO: prune objects that are no longer generated (label selector on
// kamerton.dev/system); owner references (set by the controller) only clean
// up when the whole SystemDescription is deleted.
type KubernetesApplier struct {
	Client client.Client
}

// Apply implements Applier.
func (a *KubernetesApplier) Apply(ctx context.Context, objs []*unstructured.Unstructured) error {
	var errs []error
	for _, o := range objs {
		err := a.Client.Apply(ctx, client.ApplyConfigurationFromUnstructured(o), client.FieldOwner(FieldOwner), client.ForceOwnership)
		if err != nil {
			errs = append(errs, fmt.Errorf("apply %s %s/%s: %w", o.GetKind(), o.GetNamespace(), o.GetName(), err))
		}
	}
	return errors.Join(errs...)
}

// DryRunApplier records objects instead of applying them (tests of the
// controller use it).
type DryRunApplier struct {
	Applied []*unstructured.Unstructured
}

// Apply implements Applier.
func (d *DryRunApplier) Apply(_ context.Context, objs []*unstructured.Unstructured) error {
	d.Applied = objs
	return nil
}

func yamlToJSONMap(y []byte) (map[string]any, error) {
	j, err := yaml.YAMLToJSON(y)
	if err != nil {
		return nil, err
	}
	return decodeJSONMap(j)
}

func toJSONMap(v any) (map[string]any, error) {
	j, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSONMap(j)
}

// decodeJSONMap decodes into types accepted by unstructured (json.Number for numbers).
func decodeJSONMap(j []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func copyLabels(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
