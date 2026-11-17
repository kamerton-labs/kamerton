// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"
	"time"
)

// Symptom alerts are what a caller sees when a service it depends on is in
// trouble: its own calls fail, slow down, or stop arriving because the
// requests never get through. They are the alerts grouped by cause (an
// Alertmanager inhibit rule mutes them on the callers while the dependency
// has one of them) and the alerts that link services into one incident.
//
// Resource alerts (memory, CPU, restarts, out of memory), queue alerts and
// SLO burn-rate alerts are not symptoms: they say something about the
// service itself (its pods, its consumers, its error budget over hours) and
// must reach people even while a dependency is down. Custom rules can mean
// anything and are never treated as symptoms.
var symptomAlerts = []string{
	AlertErrorRate,
	AlertOperationErrorRate,
	AlertLatency,
	AlertOperationLatency,
	AlertLatencyAboveNormal,
	AlertRequestsStopped,
}

// SymptomAlerts returns the alert names of the symptom rules (see above).
func SymptomAlerts() []string { return append([]string(nil), symptomAlerts...) }

// IsSymptom reports whether an alert name is a symptom alert.
func IsSymptom(alertname string) bool {
	for _, a := range symptomAlerts {
		if a == alertname {
			return true
		}
	}
	return false
}

// FailingOperations renders a query for the operations of the target with
// the most failed calls over window: topk(k, failed calls per operation),
// only operations with at least one failed call. The value is the number of
// failed calls in the window. The labels are those of the error-rate rules
// (Target.ServiceLabel, Target.OperationLabel).
func FailingOperations(t SpanTarget, window time.Duration, k int) string {
	failed := t.matchers().clone().Eq(t.StatusCodeLabel, t.ErrorStatusValue)
	return fmt.Sprintf("topk(%d, %s > 0)", k, t.callIncrease(failed, []string{t.ServiceLabel, t.OperationLabel}, window))
}

// Selector renders metric{namespace, pod regex} for the pods of the target,
// the selector used by the saturation rules.
func (t PodTarget) Selector(metric string) string { return t.matchers().Selector(metric) }
