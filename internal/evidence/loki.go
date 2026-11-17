// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/description"
)

// Log search.
const (
	// logLead is how long before the incident start the log search begins.
	logLead = 5 * time.Minute
	// maxLogBytes caps the Loki response read.
	maxLogBytes = 1 << 20
	// maxRawLine caps a log line before it is trimmed for the message.
	maxRawLine = 2000
	// errorPattern selects error lines (case-insensitive RE2).
	errorPattern = `(?i)(error|exception|fail|fatal|panic|timeout|refused)`
)

// LogQuery returns the LogQL query of error lines of a service. Logs that
// reach Loki over OTLP carry the OpenTelemetry service name as the stream
// label service_name, the same name and label the span metrics use
// (spec.telemetry.spans.serviceLabel, the service's telemetryName).
func LogQuery(sd *v1alpha1.SystemDescription, svc *v1alpha1.Service) string {
	tel := description.ResolveTelemetry(sd.Spec.Telemetry)
	label := tel.Spans.ServiceLabel
	if label == "" {
		label = "service_name"
	}
	return fmt.Sprintf(`{%s=%s} |~ %s`, label, strconv.Quote(description.TelemetryName(svc)), strconv.Quote(errorPattern))
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Values [][2]string `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// LogLine returns the newest error line of a service between shortly
// before since and now, from the Loki at base; "" when there is none.
func (c *Collector) LogLine(ctx context.Context, base string, sd *v1alpha1.SystemDescription, svc *v1alpha1.Service, since, now time.Time) (string, error) {
	start := since.Add(-logLead)
	if since.IsZero() || start.Before(now.Add(-c.window())) {
		start = now.Add(-c.window())
	}
	q := url.Values{}
	q.Set("query", LogQuery(sd, svc))
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(now.UnixNano(), 10))
	q.Set("limit", "1")
	q.Set("direction", "backward")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/loki/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("loki: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))
	if err != nil {
		return "", fmt.Errorf("loki: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("loki: %s", resp.Status)
	}
	var lr lokiResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return "", fmt.Errorf("loki: decode: %w", err)
	}
	var newest int64
	line := ""
	for _, r := range lr.Data.Result {
		for _, v := range r.Values {
			ts, _ := strconv.ParseInt(v[0], 10, 64)
			if line == "" || ts > newest {
				newest, line = ts, v[1]
			}
		}
	}
	if len(line) > maxRawLine {
		line = line[:maxRawLine]
	}
	return strings.Join(strings.Fields(line), " "), nil
}
