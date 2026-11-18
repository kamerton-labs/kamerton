// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/common/model"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/gitsync"
	"github.com/kamerton-labs/kamerton/internal/rulediff"
	"github.com/kamerton-labs/kamerton/internal/rules"
	"github.com/kamerton-labs/kamerton/internal/stack"
	"github.com/kamerton-labs/kamerton/internal/textdiff"
)

type systemSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	Source      string `json:"source"`
	Environment string `json:"environment,omitempty"`
	Description string `json:"description,omitempty"`
	Services    int    `json:"services"`
	Rules       int    `json:"rules"`
	Receivers   int    `json:"receivers"`
	// Draft: made in the UI and not pushed yet; it runs nowhere until its file is on the branch.
	Draft bool `json:"draft,omitempty"`
	// Git is set for a system that comes from a git repository: it is changed there, not here.
	Git *descstore.GitSource `json:"git,omitempty"`
	// ChannelsOff lists receivers whose Secret is missing: their channel gets nothing yet.
	ChannelsOff []channelOff `json:"channelsOff,omitempty"`
	Valid       bool         `json:"valid"`
	Problem     string       `json:"problem,omitempty"`
	// Alerts is nil when Alertmanager is not configured or unreachable.
	Alerts *alertCounts `json:"alerts"`
	// State: ok, warning, firing, invalid or unknown (no alert data).
	State string `json:"state"`
	// Stack is the set of programs the system uses (unknown sets resolve to
	// the built-in one; StackUnknown then names what was asked for).
	Stack        string `json:"stack"`
	StackName    string `json:"stackName"`
	StackUnknown string `json:"stackUnknown,omitempty"`
}

type channelOff struct {
	Receiver string `json:"receiver"`
	// Channel: telegram or email.
	Channel string `json:"channel"`
}

type alertCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
}

const AnnotationDescription = descstore.AnnotationPrefix + "description"

func (s *Server) listSystems(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := s.opts.Descriptions.List(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "description_unavailable", err.Error())
		return
	}
	counts, configured, aerr := s.alertCounts(ctx, list)
	out := make([]systemSummary, 0, len(list))
	for _, snap := range list {
		sum := systemSummary{ID: snap.ID, Name: snap.Name(), Source: snap.Source, Valid: snap.SD != nil}
		c := s.clientsFor(ctx, stackOf(snap))
		sum.Stack, sum.StackName, sum.StackUnknown = c.Set.ID, c.Set.Name, c.Unknown
		if snap.SD == nil {
			sum.State = "invalid"
			if snap.Invalid != nil && len(snap.Invalid.Errors) > 0 {
				sum.Problem = snap.Invalid.Errors[0].Path + ": " + snap.Invalid.Errors[0].Message
			}
			out = append(out, sum)
			continue
		}
		sd := snap.SD
		sum.Namespace, sum.Environment, sum.Services = sd.Namespace, sd.Spec.Environment, len(sd.Spec.Services)
		sum.Description = sd.Annotations[AnnotationDescription]
		sum.Git = descstore.FromGit(sd)
		sum.Draft = isDraft(snap)
		if n := sd.Spec.Notifications; n != nil {
			sum.Receivers = len(n.Receivers)
		}
		gen := s.opts.Generator
		if check := s.opts.SecretMissing; check != nil {
			gen.SecretMissing = func(ref v1alpha1.SecretKeyRef) bool { return check(ctx, sd.Namespace, ref) }
		}
		if b, err := generator.Generate(sd, gen); err == nil {
			sum.Rules = b.RuleCount
			for _, m := range b.MissingSecrets {
				sum.ChannelsOff = append(sum.ChannelsOff, channelOff{Receiver: m.Receiver, Channel: m.Channel})
			}
		} else {
			sum.Problem = err.Error()
		}
		cnt, known := counts[snap.ID]
		switch {
		case sum.Draft:
			sum.State = "unknown"
		case !known:
			sum.State = "unknown"
		default:
			c := cnt
			sum.Alerts = c
			switch {
			case c.Critical > 0:
				sum.State = "firing"
			case c.Warning > 0:
				sum.State = "warning"
			default:
				sum.State = "ok"
			}
		}
		out = append(out, sum)
	}
	nsets := 1
	if sets, err := s.setList(ctx); err == nil {
		nsets = len(sets)
	}
	resp := map[string]any{
		"mode": s.opts.Descriptions.Mode(), "writable": s.opts.Descriptions.Mode() != descstore.ModeNone,
		"systems": out, "alertsConfigured": configured, "starter": starterDescription, "stacks": nsets,
	}
	if s.git() != nil {
		st := s.git().Status()
		resp["git"] = st
		if list, err := s.git().Changes(ctx); err == nil {
			saved := make([]map[string]string, 0, len(list))
			for _, c := range list {
				saved = append(saved, map[string]string{"system": c.System, "kind": c.Kind})
			}
			resp["gitChanges"] = saved
		}
	}
	if aerr != nil {
		resp["alertsError"] = aerr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// alertCounts counts active alerts per system ID from the Alertmanager of its set,
// or without one from Prometheus' ALERTS, asking each source once. A system
// missing from counts has no alert data; err is the first failure.
func (s *Server) alertCounts(ctx context.Context, list []*descstore.Snapshot) (counts map[string]*alertCounts, configured bool, err error) {
	type source struct {
		am     alertmanager.Reader
		prom   prometheus.Querier
		labels []map[string]string
		err    error
	}
	sources := map[string]*source{}
	bySystem := map[string]string{}
	for _, snap := range list {
		if snap.SD == nil {
			continue
		}
		c := s.clientsFor(ctx, stackOf(snap))
		var key string
		switch {
		case c.AM != nil:
			key = "am " + c.Set.URL(stack.KindAlertmanager)
			if sources[key] == nil {
				sources[key] = &source{am: c.AM}
			}
		case c.Prom != nil:
			key = "prom " + c.Set.URL(stack.KindPrometheus)
			if sources[key] == nil {
				sources[key] = &source{prom: c.Prom}
			}
		default:
			continue
		}
		bySystem[snap.ID] = key
	}
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(src *source) {
			defer wg.Done()
			if src.am != nil {
				raw, err := src.am.ActiveAlerts(ctx, map[string]string{rules.LabelManaged: "true"})
				for _, a := range raw {
					src.labels = append(src.labels, a.Labels)
				}
				src.err = err
				return
			}
			m := rules.Matchers{}.Eq(rules.LabelManaged, "true").Eq("alertstate", "firing")
			v, err := src.prom.Query(ctx, m.Selector("ALERTS"), time.Now())
			src.err = err
			if vec, ok := v.(model.Vector); ok {
				for _, smp := range vec {
					lbls := map[string]string{}
					for k, val := range smp.Metric {
						lbls[string(k)] = string(val)
					}
					src.labels = append(src.labels, lbls)
				}
			}
		}(src)
	}
	wg.Wait()
	counts = map[string]*alertCounts{}
	for _, snap := range list {
		key, ok := bySystem[snap.ID]
		if !ok {
			continue
		}
		src := sources[key]
		if src.am != nil {
			configured = true
		}
		if src.err != nil {
			if err == nil && src.am != nil {
				err = src.err
			}
			continue
		}
		configured = true
		c := &alertCounts{}
		for _, l := range src.labels {
			if l[generator.LabelSystem] != snap.SD.Name {
				continue
			}
			if l[rules.LabelSeverity] == "critical" {
				c.Critical++
			} else {
				c.Warning++
			}
		}
		counts[snap.ID] = c
	}
	return counts, configured, err
}

// system writes the error response and returns nil when it cannot load the system.
// requireValid rejects stored documents that do not validate.
func (s *Server) system(w http.ResponseWriter, r *http.Request, requireValid bool) *descstore.Snapshot {
	snap, err := s.opts.Descriptions.Get(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, descstore.ErrNoDescription):
		writeError(w, http.StatusNotFound, "not_found", "no such system: "+r.PathValue("id"))
		return nil
	case err != nil:
		writeError(w, http.StatusBadGateway, "description_unavailable", err.Error())
		return nil
	case requireValid && snap.SD == nil:
		writeError(w, http.StatusConflict, "invalid", "the stored description of this system has errors; fix it in YAML mode")
		return nil
	}
	return snap
}

func (s *Server) getSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	mode := s.opts.Descriptions.Mode()
	c := s.clientsFor(r.Context(), stackOf(snap))
	resp := map[string]any{
		"id": snap.ID, "name": snap.Name(), "yaml": string(snap.Raw), "source": snap.Source,
		"mode": mode, "writable": mode != descstore.ModeNone, "valid": snap.SD != nil,
		"errors": []description.Issue{}, "warnings": []description.Issue{},
		"stack": map[string]any{"id": c.Set.ID, "name": c.Set.Name, "unknown": c.Unknown, "grafanaURL": c.Set.URL(stack.KindGrafana)},
	}
	if isDraft(snap) {
		// A draft is edited freely; every save keeps it as a change to push.
		resp["draft"], resp["writable"], resp["gitSaved"] = true, true, gitsync.KindNew
		if g := s.git(); g != nil {
			resp["gitWrite"] = g.WriteMode()
		}
	}
	if from := descstore.FromGit(snap.SD); from != nil {
		resp["git"] = from
		// Read-only unless Kamerton may write to the repository; then a save is a proposal there.
		resp["writable"] = false
		if g := s.git(); g != nil {
			if mode := g.WriteMode(); mode != "" {
				resp["writable"], resp["gitWrite"] = true, mode
			}
			// The file as people wrote it, comments included, so a save changes only what was edited.
			if raw, ok := g.Raw(from.Path); ok && snap.SD != nil {
				resp["yaml"] = string(raw)
			}
			if u := g.FileURL(from.Path); u != "" {
				resp["gitFileURL"] = u
			}
			if c, ok := g.Saved(r.Context(), snap.Name()); ok {
				// The saved, not pushed version is what the person goes on editing.
				resp["gitSaved"] = c.Kind
				if c.Content != nil {
					resp["yaml"] = string(c.Content)
				}
			}
		}
	}
	if snap.Invalid != nil {
		resp["errors"], resp["warnings"] = snap.Invalid.Errors, snap.Invalid.Warnings
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) createSystem(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !s.allowSave(ctx, w, body, nil) {
		return
	}
	snap, res, err := s.opts.Descriptions.Create(s.gitNote(ctx, r), body)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.log.Info("system created", "system", snap.ID, "source", snap.Source)
	writeJSON(w, http.StatusCreated, map[string]any{"saved": true, "id": snap.ID, "source": snap.Source, "warnings": append(res.Warnings, s.stackWarnings(ctx, stackOf(snap))...)})
}

func (s *Server) putSystem(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// The stored version decides which Secret references may stay; Save reports a missing system.
	stored, _ := s.opts.Descriptions.Get(ctx, r.PathValue("id"))
	if !s.allowSave(ctx, w, body, stored) {
		return
	}
	snap, res, err := s.opts.Descriptions.Save(s.gitNote(ctx, r), r.PathValue("id"), body)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.log.Info("system saved", "system", snap.ID, "source", snap.Source)
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "id": snap.ID, "source": snap.Source, "mode": s.opts.Descriptions.Mode(), "warnings": append(res.Warnings, s.stackWarnings(ctx, stackOf(snap))...)})
}

// git returns the repository in use, or nil.
func (s *Server) git() *gitsync.Service {
	if r := s.opts.GitRunner; r != nil {
		return r.Current(context.Background())
	}
	return s.opts.Git
}

type gitSettingsBody struct {
	gitsync.Settings
	// Token is write-only: it goes to the Secret and is never returned.
	Token string `json:"token"`
	// Create makes the folder a new project instead of opening one, called Name (empty: after
	// the repository).
	Create bool   `json:"create"`
	Name   string `json:"name"`
}

func (s *Server) gitSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.GitRunner == nil {
		v := gitsync.View{}
		if g := s.opts.Git; g != nil {
			v = gitsync.View{Locked: true, Connected: true, Settings: gitsync.Settings{URL: g.Repo(), Write: g.WriteMode()}}
		}
		writeJSON(w, http.StatusOK, v)
		return
	}
	writeJSON(w, http.StatusOK, s.opts.GitRunner.View(r.Context()))
}

func (s *Server) readGitSettings(w http.ResponseWriter, r *http.Request) (*gitSettingsBody, bool) {
	if s.opts.GitRunner == nil {
		writeError(w, http.StatusConflict, "git_unavailable", "a repository can be connected only in a cluster")
		return nil, false
	}
	body, ok := readBody(w, r)
	if !ok {
		return nil, false
	}
	var in gitSettingsBody
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "the request body is not JSON")
		return nil, false
	}
	return &in, true
}

// testGitSettings reads the repository with the given settings; nothing is stored or changed.
func (s *Server) testGitSettings(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readGitSettings(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.opts.GitRunner.Try(r.Context(), in.Settings, in.Token))
}

func (s *Server) putGitSettings(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readGitSettings(w, r)
	if !ok {
		return
	}
	// A repository that cannot be read is not connected: the mistake shows now, not a minute later.
	c := s.opts.GitRunner.Try(r.Context(), in.Settings, in.Token)
	if !c.OK {
		writeError(w, http.StatusUnprocessableEntity, "git_unreachable", c.Error)
		return
	}
	switch {
	case in.Create && c.HasProject:
		writeError(w, http.StatusConflict, "git_has_project", "this folder already holds a project; open it instead")
		return
	case in.Create && !c.CanPush:
		writeError(w, http.StatusUnprocessableEntity, "git_needs_token", "creating a project needs an access token with write access")
		return
	case !in.Create && c.Empty:
		// An empty repository has nothing to open; following it would show nothing until someone
		// thought of creating a project there.
		writeError(w, http.StatusConflict, "git_empty", "the repository is empty; create a project in it")
		return
	}
	if in.Create {
		author := ""
		if a := s.opts.Auth; a != nil {
			author, _ = a.session(r)
		}
		p, err := s.opts.GitRunner.Create(r.Context(), in.Settings, in.Token, author, in.Name)
		if err != nil {
			writeError(w, http.StatusConflict, "git_create_failed", err.Error())
			return
		}
		s.log.Info("git: project created from the UI", "branch", p.Branch)
		writeJSON(w, http.StatusOK, map[string]any{"settings": s.opts.GitRunner.View(r.Context()), "proposal": p})
		return
	}
	if err := s.opts.GitRunner.Connect(r.Context(), in.Settings, in.Token); err != nil {
		writeError(w, http.StatusConflict, "git_connect_failed", err.Error())
		return
	}
	s.log.Info("git: settings changed from the UI")
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.opts.GitRunner.View(r.Context())})
}

func (s *Server) deleteGitSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.GitRunner == nil {
		writeError(w, http.StatusConflict, "git_unavailable", "a repository can be connected only in a cluster")
		return
	}
	if err := s.opts.GitRunner.Disconnect(r.Context()); err != nil {
		writeError(w, http.StatusConflict, "git_disconnect_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.opts.GitRunner.View(r.Context()))
}

// gitNote carries the commit message a person wrote (header X-Kamerton-Message, percent-encoded)
// and their login into a save that may end up in the repository.
func (s *Server) gitNote(ctx context.Context, r *http.Request) context.Context {
	msg, err := url.PathUnescape(r.Header.Get("X-Kamerton-Message"))
	if err != nil || len(msg) > 2000 {
		msg = ""
	}
	author := ""
	if a := s.opts.Auth; a != nil {
		author, _ = a.session(r)
	}
	return descstore.WithGitNote(ctx, msg, author)
}

// gitSync looks at the repository now instead of waiting for the next tick.
func (s *Server) gitSync(w http.ResponseWriter, r *http.Request) {
	if s.git() == nil {
		writeError(w, http.StatusConflict, "git_off", "system descriptions are not read from git")
		return
	}
	writeJSON(w, http.StatusOK, s.git().Sync(r.Context()))
}

// gitPreview says what a description would change if it were written to the repository: where
// it goes and which rules differ from what is there now. Nothing is written.
func (s *Server) gitPreview(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	g := s.git()
	if g == nil || g.WriteMode() == "" {
		writeError(w, http.StatusConflict, "git_off", "writing to git is off")
		return
	}
	res := description.Validate(body)
	if !res.Valid || res.Document == nil {
		writeInvalid(w, res)
		return
	}
	after := res.Document
	st := g.Status()
	out := map[string]any{"system": after.Name, "mode": g.WriteMode(), "branch": st.Branch, "repo": st.Repo, "repoURL": st.RepoURL}
	var before *v1alpha1.SystemDescription
	file := path.Join(st.Path, after.Name+".yaml")
	if stored := s.storedByName(r.Context(), after); stored != nil && stored.SD != nil {
		before = stored.SD
		if from := descstore.FromGit(stored.SD); from != nil {
			file = from.Path
			// Compare with the file, not the cluster: the two differ while a file has an error.
			if raw, ok := g.Raw(from.Path); ok {
				if doc := description.Validate(raw).Document; doc != nil {
					before = doc
				}
			}
		} else {
			// A system made by hand: its description moves to git unchanged.
			out["move"] = true
		}
	}
	out["file"], out["new"] = file, before == nil
	d, err := rulediff.Compare(before, after)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "generation_failed", err.Error())
		return
	}
	out["diff"] = d
	writeJSON(w, http.StatusOK, out)
}

// gitChange is a saved change with what it does to the rules.
type gitChange struct {
	System  string           `json:"system"`
	Kind    string           `json:"kind"`
	Path    string           `json:"path,omitempty"`
	SavedAt time.Time        `json:"savedAt"`
	Author  string           `json:"author,omitempty"`
	Diff    *rulediff.Result `json:"diff,omitempty"`
	// Text is the change of the description file, line by line.
	Text  []textdiff.Line `json:"text,omitempty"`
	Error string          `json:"error,omitempty"`
}

// gitChanges lists the changes saved in the UI and not pushed, each with its effect on the rules.
func (s *Server) gitChanges(w http.ResponseWriter, r *http.Request) {
	g := s.git()
	if g == nil {
		writeJSON(w, http.StatusOK, map[string]any{"changes": []gitChange{}})
		return
	}
	list, err := g.Changes(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "changes_unavailable", err.Error())
		return
	}
	out := make([]gitChange, 0, len(list))
	for _, c := range list {
		one := gitChange{System: c.System, Kind: c.Kind, Path: c.Path, SavedAt: c.SavedAt, Author: c.Author}
		was := ""
		if c.Kind == gitsync.KindChange || c.Kind == gitsync.KindDelete {
			if raw, ok := g.Raw(c.Path); ok {
				was = string(raw)
			}
		}
		one.Text = textdiff.Lines(was, string(c.Content))
		if c.Kind != gitsync.KindDelete {
			after := description.Validate(c.Content).Document
			if after == nil {
				one.Error = "the saved description has errors"
			} else {
				var before *v1alpha1.SystemDescription
				if c.Kind == gitsync.KindChange {
					if raw, ok := g.Raw(c.Path); ok {
						before = description.Validate(raw).Document
					}
					if before == nil {
						if stored := s.storedByName(r.Context(), after); stored != nil {
							before = stored.SD
						}
					}
				}
				if d, err := rulediff.Compare(before, after); err == nil {
					one.Diff = &d
				} else {
					one.Error = err.Error()
				}
			}
		}
		out = append(out, one)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

func (s *Server) gitDiscard(w http.ResponseWriter, r *http.Request) {
	if s.git() == nil {
		writeError(w, http.StatusConflict, "git_off", "system descriptions are not read from git")
		return
	}
	if err := s.git().Discard(r.Context(), r.PathValue("system")); err != nil {
		writeError(w, http.StatusBadGateway, "discard_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"discarded": true})
}

// gitPush writes the saved changes to the repository in one commit.
func (s *Server) gitPush(w http.ResponseWriter, r *http.Request) {
	g := s.git()
	if g == nil || g.WriteMode() == "" {
		writeError(w, http.StatusConflict, "git_off", "writing to git is off")
		return
	}
	var in struct {
		Message string   `json:"message"`
		Systems []string `json:"systems"`
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "the request body is not JSON")
			return
		}
	}
	author := ""
	if a := s.opts.Auth; a != nil {
		author, _ = a.session(r)
	}
	p, err := g.Push(r.Context(), in.Systems, in.Message, author)
	if err != nil {
		writeError(w, http.StatusConflict, "push_failed", err.Error())
		return
	}
	s.log.Info("git: pushed from the UI", "branch", p.Branch, "commit", p.Commit)
	writeJSON(w, http.StatusOK, map[string]any{"proposal": p})
}

// gitCommit opens one commit: for each description file it changes, what happens to the rules
// and the change of the text.
func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request) {
	g := s.git()
	if g == nil {
		writeError(w, http.StatusNotFound, "not_found", "no repository")
		return
	}
	d, err := g.CommitDetail(r.Context(), r.PathValue("hash"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such commit among the last ones of the branch")
		return
	}
	type fileView struct {
		Path   string           `json:"path"`
		System string           `json:"system,omitempty"`
		Kind   string           `json:"kind"`
		Diff   *rulediff.Result `json:"diff,omitempty"`
		Text   []textdiff.Line  `json:"text,omitempty"`
	}
	files := make([]fileView, 0, len(d.Files))
	for _, f := range d.Files {
		v := fileView{Path: f.Path, Kind: gitsync.KindChange, Text: textdiff.Lines(f.Before, f.After)}
		before, after := description.Validate([]byte(f.Before)).Document, description.Validate([]byte(f.After)).Document
		switch {
		case f.After == "":
			v.Kind = gitsync.KindDelete
			if before != nil {
				v.System = before.Name
			}
		case f.Before == "":
			v.Kind = gitsync.KindNew
		}
		if after != nil {
			v.System = after.Name
			if res, err := rulediff.Compare(before, after); err == nil {
				v.Diff = &res
			}
		}
		files = append(files, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"commit": d.Commit, "message": d.Message, "files": files, "others": d.Others})
}

func (s *Server) gitCommits(w http.ResponseWriter, r *http.Request) {
	if s.git() == nil {
		writeJSON(w, http.StatusOK, map[string]any{"commits": []gitsync.Commit{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	// One more than asked, so the UI knows whether there is more to show.
	list, err := s.git().Commits(r.Context(), limit+1)
	if err != nil {
		writeError(w, http.StatusBadGateway, "commits_unavailable", err.Error())
		return
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": list, "more": more})
}

// moveSystemToGit puts a system made by hand under the repository: its description becomes a
// file there, and the file takes the system over once it is merged.
func (s *Server) moveSystemToGit(w http.ResponseWriter, r *http.Request) {
	mover, ok := s.opts.Descriptions.(interface {
		MoveToGit(ctx context.Context, id string) error
	})
	if !ok || s.git() == nil || s.git().WriteMode() == "" {
		writeError(w, http.StatusConflict, "git_off", "writing to git is off: set git.url and git.write")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	err := mover.MoveToGit(s.gitNote(ctx, r), r.PathValue("id"))
	if errors.Is(err, descstore.ErrNoDescription) {
		writeError(w, http.StatusNotFound, "not_found", "no such system")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "move_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"staged": true})
}

func (s *Server) deleteSystem(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.opts.Descriptions.Delete(s.gitNote(ctx, r), r.PathValue("id")); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.log.Info("system deleted", "system", r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) validateSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	res := description.Validate(body)
	if res.Document != nil && res.Document.Name != snap.Name() {
		res.Errors = append(res.Errors, description.Issue{Path: "/metadata/name", Message: "the name of a system cannot be changed (was " + snap.Name() + ")", Severity: description.SeverityError})
		res.Valid = false
	}
	if res.Document != nil {
		s.checkDraftSecrets(r.Context(), &res, snap)
		res.Warnings = append(res.Warnings, s.stackWarnings(r.Context(), res.Document.Spec.Stack)...)
	}
	writeJSON(w, http.StatusOK, res)
}

// draft returns the request body, or the stored description when it is empty.
func draft(body []byte, snap *descstore.Snapshot) []byte {
	if len(body) == 0 {
		return snap.Raw
	}
	return body
}

func (s *Server) previewSystemRules(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.preview(w, r, draft(body, snap), snap)
}

func (s *Server) discoverSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	s.discoverFor(w, r, snap)
}

func (s *Server) alertsSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	s.alertsFor(w, r, snap)
}

type systemBacktestRequest struct {
	backtestRequest
	YAML      string `json:"yaml"`
	Service   string `json:"service"`
	Alert     string `json:"alert"`
	Operation string `json:"operation"`
}

func (s *Server) backtestSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req systemBacktestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	prom := s.clientsFor(r.Context(), stackOf(snap)).Prom
	if req.Alert == "" {
		s.runBacktest(w, r, prom, req.backtestRequest, nil)
		return
	}
	res := description.Validate(draft([]byte(req.YAML), snap))
	if !res.Valid {
		writeInvalid(w, res)
		return
	}
	b, err := generator.Generate(res.Document, s.opts.Generator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generation_failed", err.Error())
		return
	}
	for _, g := range b.RuleGroups.Groups {
		for _, rl := range g.Rules {
			if rl.Alert != req.Alert || rl.Labels[rules.LabelService] != req.Service || rl.Labels[rules.LabelOperation] != req.Operation {
				continue
			}
			bt := req.backtestRequest
			bt.Expr, bt.For = rl.Expr, model.Duration(rl.For).String()
			s.runBacktest(w, r, prom, bt, map[string]any{
				"expr": rl.Expr, "for": bt.For, "title": rl.Annotations[rules.AnnotationTitle], "threshold": rl.Annotations[rules.AnnotationThreshold],
			})
			return
		}
	}
	writeError(w, http.StatusNotFound, "no_rule", "the description does not produce this rule (is it turned off?)")
}

type ruleState struct {
	Alert     string `json:"alert"`
	Service   string `json:"service"`
	Operation string `json:"operation,omitempty"`
	Rule      string `json:"rule,omitempty"`
	// State: pending (the condition holds, waiting for "for") or firing.
	State    string            `json:"state"`
	Severity string            `json:"severity"`
	Labels   map[string]string `json:"labels"`
}

func (s *Server) ruleStates(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, false)
	if snap == nil {
		return
	}
	prom := s.clientsFor(r.Context(), stackOf(snap)).Prom
	if prom == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "reachable": false, "states": []any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	m := rules.Matchers{}.Eq(generator.LabelSystem, snap.Name()).Eq(rules.LabelManaged, "true")
	v, err := prom.Query(ctx, m.Selector("ALERTS"), time.Now())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "reachable": false, "message": err.Error(), "states": []any{}})
		return
	}
	out := []ruleState{}
	if vec, ok := v.(model.Vector); ok {
		for _, smp := range vec {
			lbls := map[string]string{}
			for k, val := range smp.Metric {
				lbls[string(k)] = string(val)
			}
			out = append(out, ruleState{
				Alert: lbls["alertname"], Service: lbls[rules.LabelService], Operation: lbls[rules.LabelOperation], Rule: lbls[rules.LabelRule],
				State: lbls["alertstate"], Severity: lbls[rules.LabelSeverity], Labels: lbls,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "reachable": true, "states": out})
}

func (s *Server) runbookSystem(w http.ResponseWriter, r *http.Request) {
	snap := s.system(w, r, true)
	if snap == nil {
		return
	}
	s.runbookFor(r.Context(), w, snap, r.PathValue("service"))
}

func (s *Server) telegramStatusSystem(w http.ResponseWriter, r *http.Request) {
	if snap := s.system(w, r, false); snap == nil {
		return
	}
	s.telegramStatusFor(w, r, r.PathValue("id"))
}

func (s *Server) telegramRoutingSystem(w http.ResponseWriter, r *http.Request) {
	if snap := s.system(w, r, false); snap == nil {
		return
	}
	s.telegramRoutingFor(w, r, r.PathValue("id"))
}
