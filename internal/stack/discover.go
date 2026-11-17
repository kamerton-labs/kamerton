// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package stack

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Found struct {
	Kind      string            `json:"kind"`
	Namespace string            `json:"namespace"`
	Service   string            `json:"service"`
	URL       string            `json:"url"`
	Version   string            `json:"version,omitempty"`
	Healthy   bool              `json:"healthy"`
	State     string            `json:"state"`
	Message   string            `json:"message"`
	Operator  *OperatorInstance `json:"operator,omitempty"`
	// UsedBy is filled by the API, not by discovery.
	UsedBy []string `json:"usedBy"`

	headless bool
}

type OperatorInstance struct {
	// Kind is "Prometheus" or "Alertmanager".
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Labels are the matchLabels that Kamerton's PrometheusRule (AlertmanagerConfig)
	// objects need for this instance to pick them up.
	Labels map[string]string `json:"labels"`
	// SelectsAll: the selector is empty and takes every object.
	SelectsAll bool `json:"selectsAll"`
	// SelectsNone: there is no selector, so the instance takes no objects at all.
	SelectsNone bool `json:"selectsNone"`
	// HasExpressions: matchExpressions cannot be turned into labels automatically.
	HasExpressions bool   `json:"hasExpressions"`
	Namespaces     string `json:"namespaces"`

	selector labels.Selector
}

// Picks ignores namespace selection.
func (o *OperatorInstance) Picks(lbls map[string]string) bool {
	return o.selector != nil && o.selector.Matches(labels.Set(lbls))
}

// dropHeadlessTwins drops a headless service (prometheus-operated) when a normal
// service of the same instance was found.
func dropHeadlessTwins(in []Found) []Found {
	same := func(a, b *Found) bool {
		if a.Kind != b.Kind || a.Namespace != b.Namespace || a.Version != b.Version {
			return false
		}
		if (a.Operator == nil) != (b.Operator == nil) {
			return false
		}
		return a.Operator == nil || a.Operator.Name == b.Operator.Name
	}
	out := in[:0:0]
	for i := range in {
		twin := false
		if in[i].headless {
			for j := range in {
				if j != i && !in[j].headless && in[j].URL != "" && same(&in[i], &in[j]) {
					twin = true
					break
				}
			}
		}
		if !twin {
			out = append(out, in[i])
		}
	}
	return out
}

type Operator struct {
	Available     bool               `json:"available"`
	Reason        string             `json:"reason,omitempty"`
	Prometheuses  []OperatorInstance `json:"prometheuses"`
	Alertmanagers []OperatorInstance `json:"alertmanagers"`
}

type Discovery struct {
	Found    []Found  `json:"found"`
	Operator Operator `json:"operator"`
	Warnings []string `json:"warnings"`
}

// Discoverer finds programs through the Kubernetes API only and confirms each
// with an HTTP check. It never scans the network.
type Discoverer struct {
	// Client should be uncached.
	Client client.Reader
	// HTTP defaults to a client with a 3s timeout.
	HTTP *http.Client
	// Concurrency defaults to 8.
	Concurrency int
	// Timeout bounds the whole discovery; default 25s.
	Timeout time.Duration
}

var (
	gvkPrometheusList   = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "PrometheusList"}
	gvkAlertmanagerList = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "AlertmanagerList"}
)

func (d *Discoverer) Discover(ctx context.Context) (*Discovery, error) {
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 25 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var svcs corev1.ServiceList
	if err := d.Client.List(ctx, &svcs); err != nil {
		if apierrors.IsForbidden(err) {
			return nil, &DiscoveryError{Message: "Kamerton is not allowed to list the Services of the cluster (it needs the list services permission; in the Helm chart: stacks.discovery.enabled=true).", Err: err}
		}
		return nil, &DiscoveryError{Message: "Cannot list the Services of the cluster.", Err: err}
	}
	type cand struct {
		svc     *corev1.Service
		kind    string
		port    int32
		byPort  bool
		problem string
	}
	var cands []cand
	for i := range svcs.Items {
		svc := &svcs.Items[i]
		kind, byPort := Recognize(svc)
		if kind == "" {
			continue
		}
		port, problem := pickPort(svc, kind)
		cands = append(cands, cand{svc: svc, kind: kind, port: port, byPort: byPort, problem: problem})
	}

	out := &Discovery{Found: []Found{}, Warnings: []string{}}
	out.Operator = d.Operator(ctx)

	httpc := d.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 3 * time.Second}
	}
	conc := d.Concurrency
	if conc <= 0 {
		conc = 8
	}
	results := make([]*Found, len(cands))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, c := range cands {
		f := &Found{Kind: c.kind, Namespace: c.svc.Namespace, Service: c.svc.Name, UsedBy: []string{}}
		results[i] = f
		if c.problem != "" {
			f.State, f.Message = StateNoAnswer, c.problem
			continue
		}
		f.URL = fmt.Sprintf("http://%s.%s.svc:%d", c.svc.Name, c.svc.Namespace, c.port)
		wg.Add(1)
		go func(c cand, f *Found) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				f.State, f.Message = StateNoAnswer, "Not checked in time."
				return
			}
			defer func() { <-sem }()
			r := Check(ctx, httpc, c.kind, f.URL)
			f.State, f.Message, f.Version, f.Healthy = r.State, r.Message, r.Version, r.Healthy
		}(c, f)
	}
	wg.Wait()
	for i, f := range results {
		c := cands[i]
		// Something else answers here: not a candidate after all. Candidates
		// found only by their port are kept only when confirmed.
		if f.State == StateWrongProgram || (c.byPort && f.State != StateOK && f.State != StateNotReady) {
			continue
		}
		f.Operator = matchOperator(c.svc, c.kind, &out.Operator)
		f.headless = c.svc.Spec.ClusterIP == corev1.ClusterIPNone
		out.Found = append(out.Found, *f)
	}
	out.Found = dropHeadlessTwins(out.Found)
	sort.SliceStable(out.Found, func(i, j int) bool {
		a, b := out.Found[i], out.Found[j]
		if a.Kind != b.Kind {
			return kindIndex(a.Kind) < kindIndex(b.Kind)
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Service < b.Service
	})
	out.Warnings = append(out.Warnings, OperatorWarnings(&out.Operator)...)
	return out, nil
}

// Operator reports missing CRDs or permissions in Reason; they are never fatal.
func (d *Discoverer) Operator(ctx context.Context) Operator {
	op := Operator{Prometheuses: []OperatorInstance{}, Alertmanagers: []OperatorInstance{}}
	proms, err := d.listCRs(ctx, gvkPrometheusList, "Prometheus", "ruleSelector", "ruleNamespaceSelector")
	if err != nil {
		op.Reason = crReason(err)
		return op
	}
	ams, err := d.listCRs(ctx, gvkAlertmanagerList, "Alertmanager", "alertmanagerConfigSelector", "alertmanagerConfigNamespaceSelector")
	if err != nil {
		op.Reason = crReason(err)
		return op
	}
	op.Available, op.Prometheuses, op.Alertmanagers = true, proms, ams
	return op
}

func crReason(err error) string {
	switch {
	case meta.IsNoMatchError(err) || apierrors.IsNotFound(err):
		return "Prometheus Operator is not found in the cluster: there is nothing to pick rule labels from."
	case apierrors.IsForbidden(err):
		return "Not allowed to read Prometheus Operator resources (prometheuses, alertmanagers): enter the rule labels by hand."
	default:
		return "Cannot read Prometheus Operator resources: " + err.Error()
	}
}

func (d *Discoverer) listCRs(ctx context.Context, gvk schema.GroupVersionKind, kind, selField, nsField string) ([]OperatorInstance, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := d.Client.List(ctx, list); err != nil {
		return nil, err
	}
	out := []OperatorInstance{}
	for _, it := range list.Items {
		inst := OperatorInstance{Kind: kind, Namespace: it.GetNamespace(), Name: it.GetName(), Labels: map[string]string{}}
		sel, found, _ := unstructured.NestedMap(it.Object, "spec", selField)
		if !found || sel == nil {
			inst.SelectsNone = true
		} else {
			var ls metav1.LabelSelector
			if err := runtimeConvert(sel, &ls); err == nil {
				for k, v := range ls.MatchLabels {
					inst.Labels[k] = v
				}
				inst.HasExpressions = len(ls.MatchExpressions) > 0
				inst.SelectsAll = len(ls.MatchLabels) == 0 && len(ls.MatchExpressions) == 0
				if s, err := metav1.LabelSelectorAsSelector(&ls); err == nil {
					inst.selector = s
				}
			}
		}
		nsSel, nsFound, _ := unstructured.NestedMap(it.Object, "spec", nsField)
		switch {
		case !nsFound || nsSel == nil:
			inst.Namespaces = "only from its own namespace (" + inst.Namespace + ")"
		case len(nsSel) == 0:
			inst.Namespaces = "from all namespaces"
		default:
			var ls metav1.LabelSelector
			if runtimeConvert(nsSel, &ls) == nil {
				inst.Namespaces = "from namespaces with labels " + metav1.FormatLabelSelector(&ls)
			} else {
				inst.Namespaces = "from namespaces matching a selector"
			}
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type DiscoveryError struct {
	Message string
	Err     error
}

func (e *DiscoveryError) Error() string { return e.Message + " (" + e.Err.Error() + ")" }

func (e *DiscoveryError) Unwrap() error { return e.Err }

var runtimeDefaultConverter = runtime.DefaultUnstructuredConverter

func runtimeConvert(in map[string]any, out *metav1.LabelSelector) error {
	return runtimeDefaultConverter.FromUnstructured(in, out)
}

// OperatorWarnings reports two or more instances that take every object: Kamerton
// cannot tell them apart.
func OperatorWarnings(op *Operator) []string {
	var out []string
	all := func(list []OperatorInstance, what, objects string) {
		var names []string
		for _, in := range list {
			if in.SelectsAll {
				names = append(names, in.Namespace+"/"+in.Name)
			}
		}
		if len(names) > 1 {
			out = append(out, fmt.Sprintf("%s %s all take every %s: objects of any system reach each of them, and sets of programs cannot separate them. To separate them, give them different selectors.", what, strings.Join(names, ", "), objects))
		}
	}
	all(op.Prometheuses, "Prometheus", "rule (PrometheusRule)")
	all(op.Alertmanagers, "Alertmanager", "route configuration (AlertmanagerConfig)")
	return out
}

// Recognize sets byPort when only a typical port suggested the kind; such
// candidates are kept only if confirmed.
func Recognize(svc *corev1.Service) (kind string, byPort bool) {
	name := svc.Name
	switch name {
	case "prometheus-operated":
		return KindPrometheus, false
	case "alertmanager-operated":
		return KindAlertmanager, false
	}
	if skipService(name) {
		return "", false
	}
	for _, key := range []string{"app.kubernetes.io/name", "app", "k8s-app", "app.kubernetes.io/instance"} {
		if k := kindFromName(svc.Labels[key]); k != "" {
			return k, false
		}
	}
	if k := kindFromName(name); k != "" {
		return k, false
	}
	for _, p := range svc.Spec.Ports {
		for _, k := range Kinds {
			if p.Port == typicalPorts[k][0] {
				return k, true
			}
		}
	}
	return "", false
}

func skipService(name string) bool {
	for _, suf := range []string{"-headless", "-memberlist", "-discovery", "-gossip-ring", "-thanos-discovery", "loki-gateway"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

func kindFromName(v string) string {
	v = strings.ToLower(v)
	if v == "" {
		return ""
	}
	for _, skip := range []string{"operator", "exporter", "kube-state-metrics", "pushgateway", "adapter", "canary", "promtail", "grafana-agent", "alloy", "blackbox", "webhook", "admission", "image-renderer", "query-frontend-discovery", "vulture",
		// Scrape targets of kube-prometheus-stack, not Prometheus itself.
		"kube-etcd", "kube-scheduler", "kube-controller-manager", "kube-proxy", "kube-dns", "coredns", "kubelet"} {
		if strings.Contains(v, skip) {
			return ""
		}
	}
	switch {
	case strings.Contains(v, "alertmanager"):
		return KindAlertmanager
	case strings.Contains(v, "opentelemetry-collector"), strings.Contains(v, "otel-collector"), strings.Contains(v, "otelcol"):
		return KindOTelCollector
	case strings.Contains(v, "grafana"):
		return KindGrafana
	case strings.Contains(v, "loki"):
		return KindLoki
	case strings.Contains(v, "tempo"):
		return KindTempo
	case strings.Contains(v, "prometheus"):
		return KindPrometheus
	}
	return ""
}

// typicalPorts lists the usual ports of each program, best first.
var typicalPorts = map[string][]int32{
	KindPrometheus:    {9090},
	KindAlertmanager:  {9093},
	KindGrafana:       {3000, 80},
	KindLoki:          {3100, 80},
	KindTempo:         {3200},
	KindOTelCollector: {13133},
}

// pickPort returns a problem text when the service has no usable port.
func pickPort(svc *corev1.Service, kind string) (int32, string) {
	ports := svc.Spec.Ports
	for _, want := range typicalPorts[kind] {
		for _, p := range ports {
			if p.Port == want && p.Protocol != corev1.ProtocolUDP {
				return p.Port, ""
			}
		}
	}
	if kind == KindOTelCollector {
		return 0, "The Service has no health check port 13133 (health_check extension): Kamerton cannot check the collector."
	}
	for _, name := range []string{"web", "http-web", "http", "service", "http-metrics"} {
		for _, p := range ports {
			if p.Name == name {
				return p.Port, ""
			}
		}
	}
	for _, p := range ports {
		if p.Protocol != corev1.ProtocolUDP {
			return p.Port, ""
		}
	}
	return 0, "The Service has no TCP port."
}

// matchOperator matches by the operator's pod selector label, else takes the only
// resource in the namespace.
func matchOperator(svc *corev1.Service, kind string, op *Operator) *OperatorInstance {
	var list []OperatorInstance
	var keys []string
	switch kind {
	case KindPrometheus:
		list, keys = op.Prometheuses, []string{"operator.prometheus.io/name", "prometheus"}
	case KindAlertmanager:
		list, keys = op.Alertmanagers, []string{"operator.prometheus.io/name", "alertmanager"}
	default:
		return nil
	}
	var inNS []int
	for i := range list {
		if list[i].Namespace != svc.Namespace {
			continue
		}
		inNS = append(inNS, i)
		for _, k := range keys {
			if v := svc.Spec.Selector[k]; v != "" && v == list[i].Name {
				in := list[i]
				return &in
			}
		}
	}
	if len(inNS) == 1 {
		in := list[inNS[0]]
		return &in
	}
	return nil
}

func kindIndex(k string) int {
	for i, x := range Kinds {
		if x == k {
			return i
		}
	}
	return len(Kinds)
}

var ObjectLabels = map[string]string{"app.kubernetes.io/managed-by": "kamerton", "kamerton.dev/managed": "true"}

// SetWarnings: Kamerton cannot keep systems apart when several instances pick up
// their objects, and nothing is loaded when none does.
func SetWarnings(set *Resolved, op *Operator) []string {
	if op == nil || !op.Available {
		return nil
	}
	var out []string
	check := func(list []OperatorInstance, extra map[string]string, kind, objects string) {
		if len(list) == 0 || set.URL(kind) == "" {
			return
		}
		lbls := map[string]string{"kamerton.dev/system": "x"}
		for k, v := range extra {
			lbls[k] = v
		}
		for k, v := range ObjectLabels {
			lbls[k] = v
		}
		var names []string
		for i := range list {
			if list[i].Picks(lbls) {
				names = append(names, list[i].Namespace+"/"+list[i].Name)
			}
		}
		title := Titles[kind]
		switch {
		case len(names) > 1:
			out = append(out, fmt.Sprintf("%s of the systems in this set will be taken by several %s instances: %s. Kamerton cannot separate them: under Advanced, set labels that only the right one selects.", objects, title, strings.Join(names, ", ")))
		case len(names) == 0 && managedNamespace(set.URL(kind), list):
			out = append(out, fmt.Sprintf("No %s managed by Prometheus Operator will take the %s of the systems in this set: check the labels under Advanced.", title, strings.ToLower(objects)))
		}
	}
	check(op.Prometheuses, set.RuleLabels, KindPrometheus, "Rules")
	check(op.Alertmanagers, set.AlertmanagerConfigLabels, KindAlertmanager, "Alert routes")
	return out
}

func managedNamespace(raw string, list []OperatorInstance) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	parts := strings.Split(u.Hostname(), ".")
	if len(parts) < 2 || net.ParseIP(u.Hostname()) != nil {
		return false
	}
	for i := range list {
		if list[i].Namespace == parts[1] {
			return true
		}
	}
	return false
}
