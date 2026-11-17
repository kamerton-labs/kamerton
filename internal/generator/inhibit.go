// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"sort"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// inhibitRules mutes the symptom alerts of a service's direct callers while it has one.
// A muted alert still counts as a source, so a chain collapses to the alert of its root.
// Matchers name kamerton_managed and kamerton_system because inhibit rules apply to every
// alert of the Alertmanager. A warning mutes only warnings, since a warning may be routed
// nowhere. Span-metric alerts have no namespace label, so the chart sets
// alertmanagerConfigMatcherStrategy to None.
func (g *gen) inhibitRules() []any {
	if !g.spec.Notifications.GroupsByCause() {
		return nil
	}
	callers := triage.NewGraph(g.spec).MutingEdges()
	deps := make([]string, 0, len(callers))
	for d := range callers {
		deps = append(deps, d)
	}
	sort.Strings(deps)
	symptoms := rules.SymptomAlerts()
	base := func(service any) []any {
		return []any{
			matcher(rules.LabelManaged, "true"),
			matcher(LabelSystem, g.sd.Name),
			service,
			regexMatcher("alertname", symptoms),
		}
	}
	var out []any
	for _, d := range deps {
		cs := callers[d]
		src := matcher(rules.LabelService, d)
		dst := regexMatcher(rules.LabelService, cs)
		out = append(out,
			map[string]any{
				"sourceMatch": append(base(src), matcher(rules.LabelSeverity, string(v1alpha1.SeverityCritical))),
				"targetMatch": base(dst),
			},
			map[string]any{
				"sourceMatch": append(base(src), matcher(rules.LabelSeverity, string(v1alpha1.SeverityWarning))),
				"targetMatch": append(base(dst), matcher(rules.LabelSeverity, string(v1alpha1.SeverityWarning))),
			},
		)
	}
	return out
}
