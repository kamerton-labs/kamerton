// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"slices"
	"sort"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// inhibitRules mutes the symptom targets of a service's direct callers while it has a
// symptom source. A muted alert still counts as a source, so a chain collapses to the
// alert of its root. Matchers name kamerton_managed and kamerton_system because inhibit
// rules apply to every alert of the Alertmanager. A warning mutes only warnings, since a
// warning may be routed nowhere. Span-metric alerts have no namespace label, so the chart
// sets alertmanagerConfigMatcherStrategy to None.
func (g *gen) inhibitRules() []any {
	out := g.noDataInhibitRule()
	if !g.spec.Notifications.GroupsByCause() {
		return out
	}
	callers := triage.NewGraph(g.spec).MutingEdges()
	deps := make([]string, 0, len(callers))
	for d := range callers {
		deps = append(deps, d)
	}
	sort.Strings(deps)
	side := func(service any, alerts []string) []any {
		return []any{
			matcher(rules.LabelManaged, "true"),
			matcher(LabelSystem, g.sd.Name),
			service,
			regexMatcher("alertname", alerts),
		}
	}
	sources, targets := rules.SymptomSources(), rules.SymptomTargets()
	for _, d := range deps {
		src := side(matcher(rules.LabelService, d), sources)
		dst := side(regexMatcher(rules.LabelService, callers[d]), targets)
		out = append(out,
			map[string]any{
				"sourceMatch": append(src, matcher(rules.LabelSeverity, string(v1alpha1.SeverityCritical))),
				"targetMatch": dst,
			},
			map[string]any{
				"sourceMatch": append(slices.Clone(src), matcher(rules.LabelSeverity, string(v1alpha1.SeverityWarning))),
				"targetMatch": append(slices.Clone(dst), matcher(rules.LabelSeverity, string(v1alpha1.SeverityWarning))),
			},
		)
	}
	return out
}

// noDataInhibitRule: while the whole system sends nothing, the per-service "no
// data" alerts say the same thing again. It does not depend on groupByCause,
// which is about dependsOn.
func (g *gen) noDataInhibitRule() []any {
	if len(g.systemRules()) == 0 {
		return nil
	}
	base := func(alert string) []any {
		return []any{matcher(rules.LabelManaged, "true"), matcher(LabelSystem, g.sd.Name), matcher("alertname", alert)}
	}
	return []any{map[string]any{
		"sourceMatch": base(rules.AlertTelemetryMissing),
		"targetMatch": base(rules.AlertServiceTelemetryMissing),
	}}
}
