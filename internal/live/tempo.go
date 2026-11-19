// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package live

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HTTP is the client for Tempo and Loki; tests replace it.
var HTTP = &http.Client{Timeout: 20 * time.Second}

// ErrNotFound: Tempo does not have the trace (any more).
var ErrNotFound = errors.New("trace not found")

var traceIDRe = regexp.MustCompile(`^[0-9a-fA-F]{16,32}$`)

// ValidTraceID says whether id looks like a trace ID, so it is safe in a URL and a query.
func ValidTraceID(id string) bool { return traceIDRe.MatchString(id) }

// NormalTraceID is the 32 hex digits OpenTelemetry writes: Tempo drops the leading zeros of an
// ID in its answers, logs keep them.
func NormalTraceID(id string) string {
	id = strings.ToLower(id)
	if len(id) < 32 {
		id = strings.Repeat("0", 32-len(id)) + id
	}
	return id
}

func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s %s", u[:strings.IndexByte(u+"?", '?')], resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

// TraceQLString quotes a value for TraceQL.
func TraceQLString(s string) string { return strconv.Quote(s) }

// Hit is a request found by a search: the trace, and the span of the service that matched.
type Hit struct {
	TraceID    string    `json:"traceId"`
	Start      time.Time `json:"start"`
	DurationMs float64   `json:"durationMs"`
	// Start is when the matched step started (the trace's start when none matched).
	// Root is where the request came in.
	RootService string `json:"rootService,omitempty"`
	RootName    string `json:"rootName,omitempty"`
	// Span is the operation of the service the search was about.
	Span   string  `json:"span,omitempty"`
	SpanID string  `json:"spanId,omitempty"`
	SpanMs float64 `json:"spanMs,omitempty"`
}

type tempoSearch struct {
	Traces []struct {
		TraceID           string  `json:"traceID"`
		RootServiceName   string  `json:"rootServiceName"`
		RootTraceName     string  `json:"rootTraceName"`
		StartTimeUnixNano string  `json:"startTimeUnixNano"`
		DurationMs        float64 `json:"durationMs"`
		SpanSet           *struct {
			Spans []struct {
				SpanID            string `json:"spanID"`
				Name              string `json:"name"`
				StartTimeUnixNano string `json:"startTimeUnixNano"`
				DurationNanos     string `json:"durationNanos"`
			} `json:"spans"`
		} `json:"spanSet"`
	} `json:"traces"`
}

// Search finds requests by a TraceQL query in [start, end], newest first.
func Search(ctx context.Context, base, traceQL string, start, end time.Time, limit int) ([]Hit, error) {
	v := url.Values{}
	v.Set("q", traceQL)
	v.Set("start", strconv.FormatInt(start.Unix(), 10))
	v.Set("end", strconv.FormatInt(end.Unix()+1, 10))
	v.Set("limit", strconv.Itoa(limit))
	v.Set("spss", "1")
	var r tempoSearch
	if err := getJSON(ctx, strings.TrimRight(base, "/")+"/api/search?"+v.Encode(), &r); err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(r.Traces))
	for _, t := range r.Traces {
		ns, _ := strconv.ParseInt(t.StartTimeUnixNano, 10, 64)
		h := Hit{TraceID: t.TraceID, Start: time.Unix(0, ns).UTC(), DurationMs: t.DurationMs}
		if !strings.HasPrefix(t.RootServiceName, "<") {
			h.RootService, h.RootName = t.RootServiceName, t.RootTraceName
		}
		if t.SpanSet != nil && len(t.SpanSet.Spans) > 0 {
			sp := t.SpanSet.Spans[0]
			h.Span, h.SpanID = sp.Name, strings.ToLower(sp.SpanID)
			d, _ := strconv.ParseInt(sp.DurationNanos, 10, 64)
			h.SpanMs = float64(d) / 1e6
			// A trace can run for hours (a session of the load generator): the time that
			// matters is the step's own.
			if at, _ := strconv.ParseInt(sp.StartTimeUnixNano, 10, 64); at > 0 {
				h.Start = time.Unix(0, at).UTC()
			}
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out, nil
}

// Attr is an attribute of a span or an event, its value as text.
type Attr struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Event is something a span wrote down, an exception most often.
type Event struct {
	Name  string  `json:"name"`
	AtMs  float64 `json:"atMs"`
	Attrs []Attr  `json:"attrs"`
}

// Span is one step of a request: StartMs counts from the start of the trace.
type Span struct {
	ID         string  `json:"id"`
	Parent     string  `json:"parent,omitempty"`
	Service    string  `json:"service"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	StartMs    float64 `json:"startMs"`
	DurationMs float64 `json:"durationMs"`
	Error      bool    `json:"error"`
	Status     string  `json:"status,omitempty"`
	Attrs      []Attr  `json:"attrs"`
	Events     []Event `json:"events"`
}

// Trace is a request through the services.
type Trace struct {
	ID         string    `json:"id"`
	Start      time.Time `json:"start"`
	DurationMs float64   `json:"durationMs"`
	Spans      []Span    `json:"spans"`
}

type otlpValue struct {
	StringValue *string  `json:"stringValue"`
	IntValue    *string  `json:"intValue"`
	DoubleValue *float64 `json:"doubleValue"`
	BoolValue   *bool    `json:"boolValue"`
	ArrayValue  *struct {
		Values []otlpValue `json:"values"`
	} `json:"arrayValue"`
}

func (v otlpValue) String() string {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return *v.IntValue
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'g', -1, 64)
	case v.BoolValue != nil:
		return strconv.FormatBool(*v.BoolValue)
	case v.ArrayValue != nil:
		parts := make([]string, len(v.ArrayValue.Values))
		for i, x := range v.ArrayValue.Values {
			parts[i] = x.String()
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return ""
}

type otlpKV struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

func attrs(kvs []otlpKV) []Attr {
	out := make([]Attr, 0, len(kvs))
	for _, kv := range kvs {
		out = append(out, Attr{Key: kv.Key, Value: kv.Value.String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

type otlpTrace struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpKV `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				SpanID            string   `json:"spanId"`
				ParentSpanID      string   `json:"parentSpanId"`
				Name              string   `json:"name"`
				Kind              string   `json:"kind"`
				StartTimeUnixNano string   `json:"startTimeUnixNano"`
				EndTimeUnixNano   string   `json:"endTimeUnixNano"`
				Attributes        []otlpKV `json:"attributes"`
				Status            struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"status"`
				Events []struct {
					Name         string   `json:"name"`
					TimeUnixNano string   `json:"timeUnixNano"`
					Attributes   []otlpKV `json:"attributes"`
				} `json:"events"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

// spanID reads an ID the way Tempo writes it in JSON: base64 of the bytes, or hex already.
func spanID(s string) string {
	if s == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && (len(b) == 8 || len(b) == 16) {
		return hex.EncodeToString(b)
	}
	return strings.ToLower(s)
}

func nanos(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

// GetTrace reads one request from Tempo.
func GetTrace(ctx context.Context, base, id string) (*Trace, error) {
	if !ValidTraceID(id) {
		return nil, ErrNotFound
	}
	var r struct {
		Trace *otlpTrace `json:"trace"`
		otlpTrace
	}
	id = NormalTraceID(id)
	if err := getJSON(ctx, strings.TrimRight(base, "/")+"/api/v2/traces/"+id, &r); err != nil {
		return nil, err
	}
	t := r.Trace
	if t == nil {
		t = &r.otlpTrace
	}
	return flatten(id, t)
}

func flatten(id string, t *otlpTrace) (*Trace, error) {
	type raw struct {
		Span
		start, end int64
		events     []struct {
			name  string
			at    int64
			attrs []Attr
		}
	}
	var spans []raw
	var first, last int64
	for _, rs := range t.ResourceSpans {
		svc := ""
		for _, kv := range rs.Resource.Attributes {
			if kv.Key == "service.name" {
				svc = kv.Value.String()
			}
		}
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				x := raw{Span: Span{
					ID: spanID(sp.SpanID), Parent: spanID(sp.ParentSpanID), Service: svc, Name: sp.Name,
					Kind: strings.ToLower(strings.TrimPrefix(sp.Kind, "SPAN_KIND_")), Attrs: attrs(sp.Attributes),
					Error: sp.Status.Code == "STATUS_CODE_ERROR", Status: sp.Status.Message, Events: []Event{},
				}, start: nanos(sp.StartTimeUnixNano), end: nanos(sp.EndTimeUnixNano)}
				for _, e := range sp.Events {
					x.events = append(x.events, struct {
						name  string
						at    int64
						attrs []Attr
					}{e.Name, nanos(e.TimeUnixNano), attrs(e.Attributes)})
				}
				if first == 0 || x.start < first {
					first = x.start
				}
				if x.end > last {
					last = x.end
				}
				spans = append(spans, x)
			}
		}
	}
	if len(spans) == 0 {
		return nil, ErrNotFound
	}
	out := &Trace{ID: id, Start: time.Unix(0, first).UTC(), DurationMs: float64(last-first) / 1e6, Spans: make([]Span, 0, len(spans))}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for _, x := range spans {
		s := x.Span
		s.StartMs = float64(x.start-first) / 1e6
		s.DurationMs = float64(x.end-x.start) / 1e6
		for _, e := range x.events {
			s.Events = append(s.Events, Event{Name: e.name, AtMs: float64(e.at-first) / 1e6, Attrs: e.attrs})
		}
		out.Spans = append(out.Spans, s)
	}
	return out, nil
}
