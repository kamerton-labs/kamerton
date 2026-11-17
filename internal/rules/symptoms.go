// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"time"
)

// Symptom alerts fire on callers when a dependency is in trouble; an inhibit rule
// mutes them while the dependency alerts. Resource, queue, SLO and custom alerts
// are not symptoms: they must reach people even while a dependency is down.
var symptomAlerts = []string{
	AlertErrorRate,
	AlertOperationErrorRate,
	AlertLatency,
	AlertOperationLatency,
	AlertLatencyAboveNormal,
	AlertRequestsStopped,
}

func SymptomAlerts() []string { return append([]string(nil), symptomAlerts...) }

func IsSymptom(alertname string) bool {
	for _, a := range symptomAlerts {
		if a == alertname {
			return true
		}
	}
	return false
}

func FailingOperations(t SpanTarget, window time.Duration, k int) string {
	failed := t.matchers().clone().Eq(t.StatusCodeLabel, t.ErrorStatusValue)
	return fmt.Sprintf("topk(%d, %s > 0)", k, t.callIncrease(failed, []string{t.ServiceLabel, t.OperationLabel}, window))
}

func (t PodTarget) Selector(metric string) string { return t.matchers().Selector(metric) }
