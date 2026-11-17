// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"strings"
	"testing"
	"time"
)

func TestNoReadyPodsRule(t *testing.T) {
	pt := podTarget()
	pt.ReadyMetric = "kube_pod_status_ready"
	m := meta()
	m.For = 30 * time.Second
	r := NoReadyPods(NoReadyPodsParams{Meta: m, Target: pt})
	mustValidate(t, r)
	if r.Alert != AlertNoReadyPods || r.Labels[LabelService] != "checkout" || r.Labels[LabelSignal] != string(SignalAvailability) ||
		r.Labels[LabelSeverity] != "critical" || time.Duration(r.For) != 30*time.Second {
		t.Errorf("alert %s for %s labels %v", r.Alert, r.For, r.Labels)
	}
	sel := `kube_pod_status_ready{namespace="otel-demo", pod=~"checkout-.*", condition="true"}`
	want := "(\n    count by (namespace) (" + sel + ")\n  unless on (namespace)\n    sum by (namespace) (" + sel + ") > 0\n)\n" +
		"or on (namespace)\n(\n    0 * (max_over_time((sum by (namespace) (" + sel + "))[1h:1m]) > 0)\n  unless on (namespace)\n    count by (namespace) (" + sel + ")\n)"
	if r.Expr != want {
		t.Errorf("expr:\n%s\nwant:\n%s", r.Expr, want)
	}
	if r.Annotations[AnnotationTitle] != "No ready pods" || r.Annotations[AnnotationValue] != `0 of {{ $value | printf "%.0f" }} pods ready` ||
		r.Annotations[AnnotationThreshold] != "at least one ready pod" || r.Annotations["summary"] != "No ready pods: checkout" {
		t.Errorf("english annotations: %v", r.Annotations)
	}
	m.Language = LanguageRussian
	ru := NoReadyPods(NoReadyPodsParams{Meta: m, Target: pt})
	if ru.Annotations[AnnotationTitle] != "Нет готовых подов" || ru.Annotations[AnnotationValue] != `готовы 0 из {{ $value | printf "%.0f" }} подов` ||
		ru.Annotations[AnnotationThreshold] != "хотя бы один готовый под" {
		t.Errorf("russian annotations: %v", ru.Annotations)
	}

	pt.Namespace = ""
	anyNS := NoReadyPods(NoReadyPodsParams{Meta: meta(), Target: pt})
	mustValidate(t, anyNS)
	if strings.Contains(anyNS.Expr, "namespace=") {
		t.Errorf("no namespace set, but the selector names one: %s", anyNS.Expr)
	}
}

func noReadyPodsExpr(t *testing.T) string {
	t.Helper()
	r := NoReadyPods(NoReadyPodsParams{Meta: Meta{Service: "pay", Severity: "critical"}, Target: PodTarget{
		ReadyMetric: "ready", NamespaceLabel: "namespace", PodLabel: "pod", Namespace: "shop", PodRegex: `pay-([a-z0-9]+-[a-z0-9]{5}|[0-9]+)`,
	}})
	mustValidate(t, r)
	return oneLine(r.Expr)
}

// kube-state-metrics has three series per pod (condition true, false,
// unknown); only condition="true" matters. "stale" is the marker Prometheus
// writes when a pod's series vanishes from a scrape.
func TestNoReadyPodsScenarios(t *testing.T) {
	expr := noReadyPodsExpr(t)
	cart := `
  ready{namespace="shop", pod="cart-7d9f8b6c4-abcde", condition="true"} 1x300
`
	for _, tc := range []struct{ name, script string }{
		{"ready pod: no alert", `
load 1m
  ready{namespace="shop", pod="pay-7d9f8b6c4-x2k9q", condition="true"} 1x300
  ready{namespace="shop", pod="pay-7d9f8b6c4-x2k9q", condition="false"} 0x300` + cart + `
eval range from 0 to 5h step 1m ` + expr + `
`},
		{"scaled to zero: fires at once with 0 pods, stops an hour after the last ready pod", `
load 1m
  ready{namespace="shop", pod="pay-7d9f8b6c4-x2k9q", condition="true"} 1x60 stale` + cart + `
eval instant at 60m ` + expr + `

eval instant at 61m ` + expr + `
  {namespace="shop"} 0

eval instant at 119m ` + expr + `
  {namespace="shop"} 0

eval instant at 125m ` + expr + `
`},
		{"pods exist but none is ready (crash loop): fires for as long as it lasts", `
load 1m
  ready{namespace="shop", pod="pay-7d9f8b6c4-x2k9q", condition="true"} 1x30 0x270
  ready{namespace="shop", pod="pay-7d9f8b6c4-q8w7e", condition="true"} 1x30 0x270` + cart + `
eval instant at 31m ` + expr + `
  {namespace="shop"} 2

eval instant at 5h ` + expr + `
  {namespace="shop"} 2
`},
		{"new service whose only pod never becomes ready: fires", `
load 1m
  ready{namespace="shop", pod="pay-0", condition="true"} 0x60` + cart + `
eval instant at 10m ` + expr + `
  {namespace="shop"} 1
`},
		{"at zero since before the rule existed, or not in Kubernetes: no alert", "\nload 1m" + cart + `
eval range from 0 to 5h step 1m ` + expr + `
`},
		{"rolling update: the new pod is ready before the old one goes, no alert", `
load 1m
  ready{namespace="shop", pod="pay-7d9f8b6c4-x2k9q", condition="true"} 1x62 stale
  ready{namespace="shop", pod="pay-5b6c7d8e9-zzzzz", condition="true"} _x55 0 0 0 1x240` + cart + `
eval range from 0 to 5h step 1m ` + expr + `
`},
		{"another namespace with the same pod names does not hide the failure", `
load 1m
  ready{namespace="shop", pod="pay-7d9f8b6c4-x2k9q", condition="true"} 0x60
  ready{namespace="other", pod="pay-7d9f8b6c4-x2k9q", condition="true"} 1x60` + cart + `
eval instant at 10m ` + expr + `
  {namespace="shop"} 1
`},
	} {
		t.Run(tc.name, func(t *testing.T) { runPromQL(t, tc.script) })
	}
}
