// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package evidence

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/rules"
)

const (
	maxPods     = 200
	maxEvents   = 500
	maxWarnings = 3
	maxRestarts = 3
	// revisionAnnotation is the revision the Deployment controller writes
	// on each of its ReplicaSets.
	revisionAnnotation = "deployment.kubernetes.io/revision"
	revisionHashLabel  = "controller-revision-hash"
)

// Workload reports what changed in a service's pods within the window, finding pods
// as the saturation rules do; without a namespace it comes from container metrics.
func (c *Collector) Workload(ctx context.Context, sd *v1alpha1.SystemDescription, svc *v1alpha1.Service, prom prometheus.Querier, now time.Time) (generator.WorkloadChanges, error) {
	var out generator.WorkloadChanges
	pt := generator.PodTarget(sd, svc)
	re, err := regexp.Compile("^(?:" + pt.PodRegex + ")$")
	if err != nil {
		return out, fmt.Errorf("pod regex: %w", err)
	}
	ns := pt.Namespace
	if ns == "" {
		if ns, err = namespaceFromMetrics(ctx, prom, pt); err != nil || ns == "" {
			return out, err
		}
	}
	since := now.Add(-c.window())
	recent := func(t time.Time) bool { return !t.IsZero() && !t.Before(since) && !t.After(now.Add(time.Minute)) }

	var pods corev1.PodList
	if err := c.Kube.List(ctx, &pods, client.InNamespace(ns)); err != nil {
		return out, fmt.Errorf("list pods in %s: %w", ns, err)
	}
	var mine []corev1.Pod
	for _, p := range pods.Items {
		if re.MatchString(p.Name) {
			mine = append(mine, p)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })
	if len(mine) > maxPods {
		mine = mine[:maxPods]
	}
	names := map[string]bool{}
	replicaSets := map[string]bool{}
	statefulSets := map[string]bool{}
	for i := range mine {
		p := &mine[i]
		names[p.Name] = true
		for _, o := range p.OwnerReferences {
			switch o.Kind {
			case "ReplicaSet":
				replicaSets[o.Name] = true
			case "StatefulSet":
				statefulSets[o.Name] = true
			}
		}
		if p.Status.Phase == corev1.PodSucceeded {
			continue
		}
		out.Pods++
		if !podReady(p) {
			out.NotReady++
		}
		out.Restarts = append(out.Restarts, restarts(p, recent)...)
	}
	sort.SliceStable(out.Restarts, func(i, j int) bool { return out.Restarts[i].Count > out.Restarts[j].Count })
	if len(out.Restarts) > maxRestarts {
		out.Restarts = out.Restarts[:maxRestarts]
	}

	var errs []error
	owners := map[string]bool{}
	rollouts, depNames, err := c.deploymentRollouts(ctx, ns, replicaSets, recent)
	if err != nil {
		errs = append(errs, err)
	}
	out.Rollouts = append(out.Rollouts, rollouts...)
	for n := range depNames {
		owners[n] = true
	}
	for n := range replicaSets {
		owners[n] = true
	}
	for n := range statefulSets {
		owners[n] = true
		r, err := c.statefulSetRollout(ctx, ns, n, mine, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if r != nil {
			out.Rollouts = append(out.Rollouts, *r)
		}
	}
	for n := range names {
		owners[n] = true
	}
	warnings, err := c.warnings(ctx, ns, owners, recent)
	if err != nil {
		errs = append(errs, err)
	}
	out.Warnings = warnings
	return out, errors.Join(errs...)
}

// namespaceFromMetrics reads the pods' namespace from restart metrics, else memory metrics.
func namespaceFromMetrics(ctx context.Context, prom prometheus.Querier, pt rules.PodTarget) (string, error) {
	if prom == nil {
		return "", nil
	}
	metric := pt.RestartsMetric
	if metric == "" || metric == description.DisabledMetric {
		metric = pt.MemoryMetric
	}
	if metric == "" || metric == description.DisabledMetric {
		return "", nil
	}
	q := fmt.Sprintf("group by (%s) (%s)", pt.NamespaceLabel, pt.Selector(metric))
	v, err := prom.Query(ctx, q, time.Time{})
	if err != nil {
		return "", fmt.Errorf("namespace of the pods: %w", err)
	}
	vec, _ := v.(model.Vector)
	var found []string
	for _, s := range vec {
		if ns := string(s.Metric[model.LabelName(pt.NamespaceLabel)]); ns != "" {
			found = append(found, ns)
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return "", nil
	}
	return found[0], nil
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// restarts lists the containers of a pod that terminated within the window
// or are crash-looping now.
func restarts(p *corev1.Pod, recent func(time.Time) bool) []generator.Restart {
	var out []generator.Restart
	statuses := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
	for _, cs := range statuses {
		r := generator.Restart{Pod: p.Name, Container: cs.Name}
		term := cs.LastTerminationState.Terminated
		recentTerm := term != nil && recent(term.FinishedAt.Time)
		if w := cs.State.Waiting; w != nil && w.Reason == "CrashLoopBackOff" {
			r.CrashLoop = true
		}
		if !recentTerm && !r.CrashLoop {
			continue
		}
		if term != nil {
			r.Reason, r.ExitCode = term.Reason, term.ExitCode
		}
		// The restart count covers the life of the pod: it is reported as a
		// number only when the pod itself is recent.
		r.Count = 1
		if recent(p.CreationTimestamp.Time) && cs.RestartCount > 1 {
			r.Count = int(cs.RestartCount)
		}
		out = append(out, r)
	}
	return out
}

// deploymentRollouts finds Deployments whose newest ReplicaSet was created within
// the window, with old and new image tags. It also returns the Deployment names.
func (c *Collector) deploymentRollouts(ctx context.Context, ns string, podRS map[string]bool, recent func(time.Time) bool) ([]generator.Rollout, map[string]bool, error) {
	deps := map[string]bool{}
	if len(podRS) == 0 {
		return nil, deps, nil
	}
	var list appsv1.ReplicaSetList
	if err := c.Kube.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, deps, fmt.Errorf("list replicasets in %s: %w", ns, err)
	}
	byDep := map[string][]appsv1.ReplicaSet{}
	for _, rs := range list.Items {
		dep := ownerOf(rs.OwnerReferences, "Deployment")
		if dep == "" {
			continue
		}
		byDep[dep] = append(byDep[dep], rs)
		if podRS[rs.Name] {
			deps[dep] = true
		}
	}
	var out []generator.Rollout
	names := make([]string, 0, len(deps))
	for d := range deps {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		sets := byDep[d]
		sort.Slice(sets, func(i, j int) bool { return revision(&sets[i]) > revision(&sets[j]) })
		newest := &sets[0]
		if !recent(newest.CreationTimestamp.Time) {
			continue
		}
		r := generator.Rollout{Workload: d, At: newest.CreationTimestamp.Time}
		newImages := images(newest.Spec.Template.Spec.Containers)
		if len(sets) > 1 {
			r.OldImage, r.NewImage = changedTags(images(sets[1].Spec.Template.Spec.Containers), newImages)
		} else if len(newImages) > 0 {
			r.NewImage = imageTag(newImages[0].image)
		}
		out = append(out, r)
	}
	return out, deps, nil
}

// statefulSetRollout reports a StatefulSet that is rolling out a new
// revision now. The old image is not known without ControllerRevisions.
func (c *Collector) statefulSetRollout(ctx context.Context, ns, name string, pods []corev1.Pod, now time.Time) (*generator.Rollout, error) {
	var sts appsv1.StatefulSet
	if err := c.Kube.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &sts); err != nil {
		return nil, fmt.Errorf("get statefulset %s/%s: %w", ns, name, err)
	}
	if sts.Status.UpdateRevision == "" || sts.Status.UpdateRevision == sts.Status.CurrentRevision {
		return nil, nil
	}
	r := &generator.Rollout{Workload: name, At: now}
	for i := range pods {
		p := &pods[i]
		if ownerOf(p.OwnerReferences, "StatefulSet") == name && p.Labels[revisionHashLabel] == sts.Status.UpdateRevision && p.CreationTimestamp.Time.Before(r.At) {
			r.At = p.CreationTimestamp.Time
		}
	}
	if imgs := images(sts.Spec.Template.Spec.Containers); len(imgs) > 0 {
		r.NewImage = imageTag(imgs[0].image)
	}
	return r, nil
}

func (c *Collector) warnings(ctx context.Context, ns string, objects map[string]bool, recent func(time.Time) bool) ([]generator.Warning, error) {
	var list corev1.EventList
	if err := c.Kube.List(ctx, &list, client.InNamespace(ns), client.Limit(maxEvents)); err != nil {
		return nil, fmt.Errorf("list events in %s: %w", ns, err)
	}
	type agg struct {
		w    generator.Warning
		last time.Time
	}
	byKey := map[string]*agg{}
	for i := range list.Items {
		e := &list.Items[i]
		if e.Type != corev1.EventTypeWarning || !objects[e.InvolvedObject.Name] {
			continue
		}
		t := eventTime(e)
		if !recent(t) {
			continue
		}
		key := e.Reason + "\x00" + e.Message
		a := byKey[key]
		if a == nil {
			a = &agg{w: generator.Warning{Reason: e.Reason, Message: e.Message}}
			byKey[key] = a
		}
		n := e.Count
		if e.Series != nil && e.Series.Count > n {
			n = e.Series.Count
		}
		if n < 1 {
			n = 1
		}
		a.w.Count += n
		if t.After(a.last) {
			a.last = t
		}
	}
	all := make([]*agg, 0, len(byKey))
	for _, a := range byKey {
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].w.Count != all[j].w.Count {
			return all[i].w.Count > all[j].w.Count
		}
		if !all[i].last.Equal(all[j].last) {
			return all[i].last.After(all[j].last)
		}
		return all[i].w.Reason < all[j].w.Reason
	})
	var out []generator.Warning
	for i := 0; i < len(all) && i < maxWarnings; i++ {
		out = append(out, all[i].w)
	}
	return out, nil
}

func eventTime(e *corev1.Event) time.Time {
	switch {
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.Time
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	default:
		return e.CreationTimestamp.Time
	}
}

func ownerOf(refs []metav1.OwnerReference, kind string) string {
	for _, o := range refs {
		if o.Kind == kind {
			return o.Name
		}
	}
	return ""
}

func revision(rs *appsv1.ReplicaSet) int64 {
	n, _ := strconv.ParseInt(rs.Annotations[revisionAnnotation], 10, 64)
	return n
}

type containerImage struct{ name, image string }

func images(cs []corev1.Container) []containerImage {
	out := make([]containerImage, len(cs))
	for i, c := range cs {
		out[i] = containerImage{c.Name, c.Image}
	}
	return out
}

// changedTags returns the tags of the first container whose image changed, or the
// new tag of the first container when only the configuration changed.
func changedTags(old, cur []containerImage) (string, string) {
	prev := map[string]string{}
	for _, c := range old {
		prev[c.name] = c.image
	}
	for _, c := range cur {
		if p, ok := prev[c.name]; ok && p != c.image {
			return imageTag(p), imageTag(c.image)
		}
	}
	if len(cur) > 0 {
		return "", imageTag(cur[0].image)
	}
	return "", ""
}

// imageTag returns the tag of an image reference, a short digest, or the
// last path element when there is neither.
func imageTag(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		d := ref[at+1:]
		if _, hex, ok := strings.Cut(d, ":"); ok && len(hex) > 12 {
			d = hex[:12]
		}
		return d
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[colon+1:]
	}
	return ref[slash+1:]
}
