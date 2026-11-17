// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package evidence

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
)

// TraceQuery is the TraceQL query of failing traces of a service: spans of
// the service (OpenTelemetry resource attribute service.name, the name the
// span metrics carry) with status error.
func TraceQuery(svc *v1alpha1.Service) string {
	return `{resource.service.name=` + strconv.Quote(description.TelemetryName(svc)) + ` && status=error}`
}

// TraceURL links to failing traces of a service in Grafana Explore with the
// Tempo data source, from shortly before since to now. Empty without a
// Grafana address people can open (Collector.GrafanaURL) or when span
// metrics are turned off for the system.
func (c *Collector) TraceURL(sd *v1alpha1.SystemDescription, svc *v1alpha1.Service, since, now time.Time) string {
	base := strings.TrimRight(strings.TrimSpace(c.GrafanaURL), "/")
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	if description.ResolveTelemetry(sd.Spec.Telemetry).Spans.CallsMetric == description.DisabledMetric {
		return ""
	}
	ds := c.TempoDatasource
	if ds == "" {
		ds = DefaultTempoDatasource
	}
	from := since.Add(-logLead)
	if since.IsZero() || from.Before(now.Add(-c.window())) {
		from = now.Add(-c.window())
	}
	panes := map[string]any{
		"a": map[string]any{
			"datasource": ds,
			"queries": []any{map[string]any{
				"refId":      "A",
				"datasource": map[string]any{"type": "tempo", "uid": ds},
				"queryType":  "traceql",
				"query":      TraceQuery(svc),
			}},
			"range": map[string]any{
				"from": strconv.FormatInt(from.UnixMilli(), 10),
				"to":   strconv.FormatInt(now.UnixMilli(), 10),
			},
		},
	}
	j, err := json.Marshal(panes)
	if err != nil {
		return ""
	}
	q := url.Values{}
	q.Set("schemaVersion", "1")
	q.Set("orgId", "1")
	q.Set("panes", string(j))
	return base + "/explore?" + q.Encode()
}
