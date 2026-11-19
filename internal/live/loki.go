// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package live

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Log is one line a service wrote.
type Log struct {
	Time    time.Time `json:"time"`
	Service string    `json:"service"`
	Level   string    `json:"level,omitempty"`
	SpanID  string    `json:"spanId,omitempty"`
	Line    string    `json:"line"`
}

// TraceLogs reads every line written while the request ran, from all services, oldest first.
// serviceLabel is the stream label that names the service (service_name with OTLP).
func TraceLogs(ctx context.Context, base, serviceLabel, traceID string, start, end time.Time) ([]Log, error) {
	if !ValidTraceID(traceID) {
		return nil, ErrNotFound
	}
	if serviceLabel == "" {
		serviceLabel = "service_name"
	}
	v := url.Values{}
	v.Set("query", "{"+serviceLabel+`=~".+"} | trace_id="`+NormalTraceID(traceID)+`"`)
	v.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	v.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	v.Set("limit", "500")
	v.Set("direction", "forward")
	var r struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := getJSON(ctx, strings.TrimRight(base, "/")+"/loki/api/v1/query_range?"+v.Encode(), &r); err != nil {
		return nil, err
	}
	out := []Log{}
	for _, s := range r.Data.Result {
		level := s.Stream["severity_text"]
		if level == "" {
			level = s.Stream["detected_level"]
		}
		for _, val := range s.Values {
			ns, _ := strconv.ParseInt(val[0], 10, 64)
			line := val[1]
			if len(line) > 4000 {
				line = line[:4000] + "…"
			}
			out = append(out, Log{Time: time.Unix(0, ns).UTC(), Service: s.Stream[serviceLabel], Level: strings.ToLower(level), SpanID: strings.ToLower(s.Stream["span_id"]), Line: line})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}
