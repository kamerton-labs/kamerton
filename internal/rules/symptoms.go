// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"slices"
	"time"
)

// Symptom sources are what a failing dependency fires; while one fires, an
// inhibit rule mutes the symptom targets of its callers. "No ready pods" only
// ever explains others, and SLO burn alerts are only ever explained: a caller's
// budget burns because of the dependency, but a burning budget proves nothing
// about the services it calls. Resource, queue, "no data" and custom alerts are
// neither: they must reach people even while a dependency is down.
var (
	symptomSources = []string{
		AlertErrorRate,
		AlertOperationErrorRate,
		AlertLatency,
		AlertOperationLatency,
		AlertLatencyAboveNormal,
		AlertRequestsStopped,
		AlertNoReadyPods,
	}
	symptomTargets = []string{
		AlertErrorRate,
		AlertOperationErrorRate,
		AlertLatency,
		AlertOperationLatency,
		AlertLatencyAboveNormal,
		AlertRequestsStopped,
		AlertSLOBurnFast,
		AlertSLOBurnSlow,
	}
)

func SymptomSources() []string { return slices.Clone(symptomSources) }

func SymptomTargets() []string { return slices.Clone(symptomTargets) }

func IsSymptomSource(alertname string) bool { return slices.Contains(symptomSources, alertname) }

func IsSymptomTarget(alertname string) bool { return slices.Contains(symptomTargets, alertname) }

func FailingOperations(t SpanTarget, window time.Duration, k int) string {
	failed := t.matchers().clone().Eq(t.StatusCodeLabel, t.ErrorStatusValue)
	return fmt.Sprintf("topk(%d, %s > 0)", k, t.callIncrease(failed, []string{t.ServiceLabel, t.OperationLabel}, window))
}

func (t PodTarget) Selector(metric string) string { return t.matchers().Selector(metric) }
