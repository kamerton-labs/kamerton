// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package stack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Check states.
const (
	// StateOK: the address answers and it is the expected program.
	StateOK = "ok"
	// StateNotReady: it is the program, but its health check fails.
	StateNotReady = "not_ready"
	// StateNoAnswer: nothing answers at the address.
	StateNoAnswer = "no_answer"
	// StateWrongProgram: something answers, but not the expected program.
	StateWrongProgram = "wrong_program"
	// StateNoAccess: the program asks for a login.
	StateNoAccess = "no_access"
	// StateBadURL: the address is not an http/https URL.
	StateBadURL = "bad_url"
)

// CheckResult says whether an address is the program it should be.
type CheckResult struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
	// State is one of the State* constants.
	State      string `json:"state"`
	Identified bool   `json:"identified"`
	Healthy    bool   `json:"healthy"`
	Version    string `json:"version,omitempty"`
	// Message is a plain-words explanation for the UI.
	Message string `json:"message"`
	// Error is the technical reason, when there is one.
	Error     string `json:"error,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
}

// identity describes how to recognise a program over HTTP.
type identity struct {
	// health is the readiness path; empty when the identity answer says it.
	health string
	// path is the identity endpoint.
	path string
	// match decodes the identity answer: ok when it is this program, the
	// version when known, and healthy for programs without a health path.
	match func(body []byte) (ok bool, version string, healthy bool)
}

func jsonField(body []byte, v any) bool { return json.Unmarshal(body, v) == nil }

var identities = map[string]identity{
	KindPrometheus: {health: "/-/ready", path: "/api/v1/status/buildinfo", match: func(b []byte) (bool, string, bool) {
		var r struct {
			Status string `json:"status"`
			Data   struct {
				Version string `json:"version"`
			} `json:"data"`
		}
		return jsonField(b, &r) && r.Status == "success" && r.Data.Version != "", r.Data.Version, false
	}},
	KindAlertmanager: {health: "/-/ready", path: "/api/v2/status", match: func(b []byte) (bool, string, bool) {
		var r struct {
			VersionInfo *struct {
				Version string `json:"version"`
			} `json:"versionInfo"`
			Cluster *json.RawMessage `json:"cluster"`
		}
		if !jsonField(b, &r) || r.VersionInfo == nil || r.Cluster == nil {
			return false, "", false
		}
		return true, r.VersionInfo.Version, false
	}},
	KindGrafana: {path: "/api/health", match: func(b []byte) (bool, string, bool) {
		var r struct {
			Database *string `json:"database"`
			Version  string  `json:"version"`
		}
		if !jsonField(b, &r) || r.Database == nil {
			return false, "", false
		}
		return true, r.Version, *r.Database == "ok"
	}},
	KindLoki:  {health: "/ready", path: "/loki/api/v1/status/buildinfo", match: buildInfo},
	KindTempo: {health: "/ready", path: "/api/status/buildinfo", match: buildInfo},
	// The health_check extension of the collector answers on its own port.
	KindOTelCollector: {path: "/", match: func(b []byte) (bool, string, bool) {
		var r struct {
			Status  string  `json:"status"`
			UpSince *string `json:"upSince"`
		}
		if !jsonField(b, &r) || (r.UpSince == nil && !strings.Contains(r.Status, "Server")) {
			return false, "", false
		}
		return true, "", r.Status == "Server available"
	}},
}

// buildInfo matches the build-info answer of Loki and Tempo. Real builds may
// leave goVersion empty (Loki 3.7 does), so the keys must be present, not filled.
func buildInfo(b []byte) (bool, string, bool) {
	var r struct {
		Version   string  `json:"version"`
		Revision  *string `json:"revision"`
		GoVersion *string `json:"goVersion"`
	}
	return jsonField(b, &r) && r.Version != "" && (r.Revision != nil || r.GoVersion != nil), r.Version, false
}

const (
	maxIdentityBody = 1 << 20
	// maxVersionLen bounds the version string taken from an answer: it is
	// the only part of a response body that reaches the API.
	maxVersionLen = 64
	maxRedirects  = 3
)

// sameHostRedirects lets a check follow a few redirects within the same host
// (a program behind a path prefix, http to https) and nowhere else: the
// address is checked, not whatever it points to. Any other redirect ends the
// check with the redirect status (not the expected program).
func sameHostRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects ||
		(req.URL.Scheme != "http" && req.URL.Scheme != "https") ||
		req.URL.Hostname() != via[0].URL.Hostname() {
		return http.ErrUseLastResponse
	}
	return nil
}

// cleanVersion keeps a version string short and printable.
func cleanVersion(v string) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(v))
	if r := []rune(v); len(r) > maxVersionLen {
		v = string(r[:maxVersionLen])
	}
	return v
}

// Check probes one address for one program kind: is it reachable, is it
// really that program, is it ready, which version. Only GET requests are
// sent; of the answers only the status and a short version string are kept.
func Check(ctx context.Context, c *http.Client, kind, rawURL string) CheckResult {
	res := CheckResult{Kind: kind, URL: rawURL}
	title := Titles[kind]
	id, ok := identities[kind]
	if !ok {
		res.State, res.Message = StateBadURL, "Unknown program: "+kind
		return res
	}
	base, err := NormalizeURL(rawURL)
	if err != nil || base == "" {
		res.State, res.Message = StateBadURL, "The address must start with http:// or https://"
		if err != nil {
			res.Message = err.Error()
		}
		return res
	}
	res.URL = base
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	if c.CheckRedirect == nil {
		cc := *c
		cc.CheckRedirect = sameHostRedirects
		c = &cc
	}
	start := time.Now()
	defer func() { res.LatencyMs = time.Since(start).Milliseconds() }()

	status, body, err := get(ctx, c, base+id.path)
	if err != nil {
		res.State, res.Error = StateNoAnswer, err.Error()
		res.Message = "No answer: nothing responds at this address."
		return res
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		res.State = StateNoAccess
		res.Message = fmt.Sprintf("Responds, but asks for a login. Kamerton connects to %s without a password.", title)
		return res
	}
	if status == http.StatusOK {
		res.Identified, res.Version, res.Healthy = id.match(body)
		res.Version = cleanVersion(res.Version)
	}
	if !res.Identified {
		res.State = StateWrongProgram
		res.Error = fmt.Sprintf("GET %s: status %d", id.path, status)
		res.Message = fmt.Sprintf("Responds, but it is not %s.", title)
		return res
	}
	if id.health != "" {
		hs, _, err := get(ctx, c, base+id.health)
		res.Healthy = err == nil && hs == http.StatusOK
		if err != nil {
			res.Error = err.Error()
		} else if hs != http.StatusOK {
			res.Error = fmt.Sprintf("GET %s: status %d", id.health, hs)
		}
	}
	if res.Healthy {
		res.State, res.Message = StateOK, "Working"
	} else {
		res.State, res.Message = StateNotReady, fmt.Sprintf("This is %s, but it reports that it is not ready.", title)
	}
	return res
}

func get(ctx context.Context, c *http.Client, u string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) && ue.Unwrap() != nil {
			return 0, nil, ue.Unwrap()
		}
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxIdentityBody))
	return resp.StatusCode, b, err
}
