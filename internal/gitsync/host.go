// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Hosting services Kamerton can open requests on and mark commits at.
const (
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
)

// host is the API of the repository's hosting service.
type host struct {
	provider string
	// api is the API root: https://api.github.com, https://ghe.example/api/v3, https://gitlab.com/api/v4.
	api string
	// repo is owner/name, or group/subgroup/name on GitLab.
	repo string
	http *http.Client
}

// host works out the hosting service from the settings and the URL; nil when it is not one
// Kamerton knows, and then only plain git is used.
func (s *Service) host() *host {
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(s.Provider))
	if provider == "" {
		switch h := strings.ToLower(u.Hostname()); {
		case h == "github.com":
			provider = ProviderGitHub
		case h == "gitlab.com" || strings.HasPrefix(h, "gitlab."):
			provider = ProviderGitLab
		}
	}
	repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if repo == "" {
		return nil
	}
	api := strings.TrimRight(strings.TrimSpace(s.APIURL), "/")
	switch provider {
	case ProviderGitHub:
		if api == "" {
			api = "https://api.github.com"
			if !strings.EqualFold(u.Hostname(), "github.com") {
				api = u.Scheme + "://" + u.Host + "/api/v3"
			}
		}
	case ProviderGitLab:
		if api == "" {
			api = u.Scheme + "://" + u.Host + "/api/v4"
		}
	default:
		return nil
	}
	return &host{provider: provider, api: api, repo: repo, http: &http.Client{Timeout: 20 * time.Second}}
}

// openRequest opens a pull request (GitHub) or a merge request (GitLab) and returns its address.
func (h *host) openRequest(ctx context.Context, token, from, into, title, body string) (string, error) {
	var out struct {
		HTMLURL string `json:"html_url"`
		WebURL  string `json:"web_url"`
	}
	var err error
	if h.provider == ProviderGitHub {
		err = h.call(ctx, token, "/repos/"+h.repo+"/pulls", map[string]any{"title": title, "head": from, "base": into, "body": body}, &out)
	} else {
		err = h.call(ctx, token, "/projects/"+url.PathEscape(h.repo)+"/merge_requests",
			map[string]any{"title": title, "source_branch": from, "target_branch": into, "description": body, "remove_source_branch": true}, &out)
	}
	if err != nil {
		return "", err
	}
	return out.HTMLURL + out.WebURL, nil
}

// setStatus marks a commit as applied or not, the way a build system does.
func (h *host) setStatus(ctx context.Context, token, commit string, ok bool, description string) error {
	if len(description) > 140 {
		description = description[:137] + "..."
	}
	if h.provider == ProviderGitHub {
		state := "failure"
		if ok {
			state = "success"
		}
		return h.call(ctx, token, "/repos/"+h.repo+"/statuses/"+commit, map[string]any{"state": state, "context": "kamerton", "description": description}, nil)
	}
	state := "failed"
	if ok {
		state = "success"
	}
	return h.call(ctx, token, "/projects/"+url.PathEscape(h.repo)+"/statuses/"+commit, map[string]any{"state": state, "name": "kamerton", "description": description}, nil)
}

func (h *host) call(ctx context.Context, token, path string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.api+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.provider == ProviderGitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", h.provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s: %s", h.provider, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
