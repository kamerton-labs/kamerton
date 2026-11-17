// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"sort"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/triage"
)

// inhibitRules groups notifications by cause (spec.notifications.groupByCause,
// on by default): while a service has a symptom alert (rules.SymptomAlerts:
// errors, latency, requests stopped), the symptom alerts of the services that
// call it directly are muted. A muted alert still counts as a source in
// Alertmanager, so a chain frontend -> checkout -> payment collapses to the
// alert of payment. Resource, queue, SLO and custom alerts are neither muted
// nor muting.
//
// How the rules match:
//
//   - Every matcher names kamerton_managed and kamerton_system: inhibit rules
//     of an AlertmanagerConfig apply to all alerts of the Alertmanager, and
//     must not touch alerts of other systems.
//   - Source and target name different services (a service never depends on
//     itself, and links inside a dependency cycle are dropped, see
//     triage.MutingEdges), so no alert can inhibit itself and two services in
//     a cycle cannot mute each other.
//   - Severity: a critical source mutes callers of any severity, a warning
//     source mutes only warnings. A warning that may go nowhere (routing
//     sends only critical alerts) must not hide a critical alert.
//   - The Prometheus Operator adds a namespace="<namespace>" matcher to both
//     sides unless alertmanagerConfigMatcherStrategy is None. Span-metric
//     alerts carry no namespace label, so with that strategy neither these
//     rules nor the routes of the same AlertmanagerConfig match; the bundled
//     chart sets None for this reason.
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
