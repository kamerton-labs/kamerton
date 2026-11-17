// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package deployer

import (
	"testing"

	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/generator"
)

func TestObjects(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := generator.Generate(sd, generator.Options{})
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Objects(b, "kamerton", nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, o := range objs {
		kinds[o.GetKind()]++
		if o.GetNamespace() != "kamerton" || o.GetLabels()[LabelSystem] != "otel-demo" {
			t.Errorf("%s: bad metadata %v", o.GetKind(), o.Object["metadata"])
		}
		// DeepCopy panics on non-JSON types; this guards the conversion.
		_ = o.DeepCopy()
	}
	if kinds["PrometheusRule"] != 1 || kinds["AlertmanagerConfig"] != 1 || kinds["ConfigMap"] != 2 {
		t.Fatalf("unexpected kinds: %v", kinds)
	}
	groups, ok := objs[0].Object["spec"].(map[string]any)["groups"].([]any)
	if !ok || len(groups) == 0 {
		t.Fatalf("PrometheusRule has no groups: %v", objs[0].Object["spec"])
	}
}

// Labels of the system's set of programs go on the PrometheusRule and the
// AlertmanagerConfig only, and never replace Kamerton's own labels.
func TestObjectsWithSetLabels(t *testing.T) {
	sd, _, err := description.LoadFile("../../testdata/otel-demo/system.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := generator.Generate(sd, generator.Options{})
	if err != nil {
		t.Fatal(err)
	}
	objs, err := ObjectsWithLabels(b, "kamerton", ExtraLabels{
		All:                map[string]string{"team": "shop"},
		Rule:               map[string]string{"release": "kps-team-a", LabelSystem: "spoofed"},
		AlertmanagerConfig: map[string]string{"alertmanager": "team-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		l := o.GetLabels()
		if l["team"] != "shop" || l[LabelSystem] != "otel-demo" || l[LabelManaged] != "true" || l[LabelManagedBy] != "kamerton" {
			t.Errorf("%s %s: labels %v", o.GetKind(), o.GetName(), l)
		}
		switch o.GetKind() {
		case "PrometheusRule":
			if l["release"] != "kps-team-a" || l["alertmanager"] != "" {
				t.Errorf("PrometheusRule labels: %v", l)
			}
		case "AlertmanagerConfig":
			if l["alertmanager"] != "team-a" || l["release"] != "" {
				t.Errorf("AlertmanagerConfig labels: %v", l)
			}
		default:
			if l["release"] != "" || l["alertmanager"] != "" || (o.GetName() == "kamerton-otel-demo-dashboards" && l[LabelGrafanaDashboard] != "1") {
				t.Errorf("%s %s labels: %v", o.GetKind(), o.GetName(), l)
			}
		}
	}
}
