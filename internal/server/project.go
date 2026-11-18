// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kamerton-labs/kamerton/internal/gitsync"
	"github.com/kamerton-labs/kamerton/internal/project"
)

func (s *Server) projectInfo(w http.ResponseWriter, r *http.Request) {
	if s.opts.Projects == nil {
		writeJSON(w, http.StatusOK, project.Info{Exists: true})
		return
	}
	writeJSON(w, http.StatusOK, s.opts.Projects.Info(r.Context()))
}

// readProjectBody decodes a request about the project into v.
func readProjectBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, ok := readBody(w, r)
	if !ok {
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "the request body is not JSON")
		return false
	}
	return true
}

func (s *Server) user(r *http.Request) string {
	if a := s.opts.Auth; a != nil {
		u, _ := a.session(r)
		return u
	}
	return ""
}

func writeProjectErr(w http.ResponseWriter, err error) {
	var ue *project.UserError
	switch {
	case errors.Is(err, project.ErrNotOpen):
		writeError(w, http.StatusConflict, "project_not_open", err.Error())
	case errors.Is(err, project.ErrExists):
		writeError(w, http.StatusConflict, "project_exists", err.Error())
	case errors.Is(err, project.ErrNoName), errors.As(err, &ue):
		writeError(w, http.StatusUnprocessableEntity, "project_invalid", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "project_failed", err.Error())
	}
}

func (s *Server) newProject(w http.ResponseWriter, r *http.Request) {
	if s.opts.Projects == nil {
		writeError(w, http.StatusConflict, "project_unavailable", "this installation is its own project")
		return
	}
	var req project.NewRequest
	if !readProjectBody(w, r, &req) {
		return
	}
	in, err := s.opts.Projects.New(r.Context(), req, s.user(r))
	if err != nil {
		writeProjectErr(w, err)
		return
	}
	s.log.Info("project created", "name", in.Name, "where", in.Where, "path", in.Path)
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) renameProject(w http.ResponseWriter, r *http.Request) {
	if s.opts.Projects == nil {
		writeError(w, http.StatusConflict, "project_unavailable", "this installation is its own project")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !readProjectBody(w, r, &req) {
		return
	}
	in, err := s.opts.Projects.Rename(r.Context(), req.Name)
	if err != nil {
		writeProjectErr(w, err)
		return
	}
	s.log.Info("project renamed", "name", in.Name)
	writeJSON(w, http.StatusOK, in)
}

// local is the project kept in folders on this machine, or nil in a cluster.
func (s *Server) local(w http.ResponseWriter) project.LocalProjects {
	l, ok := s.opts.Projects.(project.LocalProjects)
	if !ok {
		writeError(w, http.StatusConflict, "project_not_local", "projects are opened by folder only when Kamerton runs on this machine")
		return nil
	}
	return l
}

func (s *Server) openProject(w http.ResponseWriter, r *http.Request) {
	l := s.local(w)
	if l == nil {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !readProjectBody(w, r, &req) {
		return
	}
	in, err := l.Open(r.Context(), req.Path)
	if err != nil {
		writeProjectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) closeProject(w http.ResponseWriter, r *http.Request) {
	l := s.local(w)
	if l == nil {
		return
	}
	l.Close(r.Context())
	writeJSON(w, http.StatusOK, l.Info(r.Context()))
}

// checkProjectRepo reads a repository before a project is taken from it; nothing is stored.
func (s *Server) checkProjectRepo(w http.ResponseWriter, r *http.Request) {
	var req project.CloneRequest
	if !readProjectBody(w, r, &req) {
		return
	}
	runner := s.opts.GitRunner
	if runner == nil {
		runner = &gitsync.Runner{Logger: s.log}
	}
	writeJSON(w, http.StatusOK, runner.Try(r.Context(), gitsync.Settings{URL: req.URL, Branch: req.Branch, Path: req.Folder}, req.Token))
}

func (s *Server) cloneProject(w http.ResponseWriter, r *http.Request) {
	l := s.local(w)
	if l == nil {
		return
	}
	var req project.CloneRequest
	if !readProjectBody(w, r, &req) {
		return
	}
	in, err := l.Clone(r.Context(), req, s.user(r))
	if err != nil {
		writeProjectErr(w, err)
		return
	}
	s.log.Info("project cloned", "name", in.Name, "path", in.Path)
	writeJSON(w, http.StatusCreated, in)
}
