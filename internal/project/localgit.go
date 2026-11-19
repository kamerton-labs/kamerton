// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package project

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GitState is the working copy of a local project as git sees it.
type GitState struct {
	// Available: git is installed and the project's folder is in a working copy.
	Available bool   `json:"available"`
	Branch    string `json:"branch,omitempty"`
	// Upstream is the remote branch the local one follows; empty when it follows none yet.
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	// Changes are the files of the project's folder not committed yet.
	Changes []GitChange `json:"changes"`
	// Why is set when Available is false: git's message, and Problem one of the Git* names.
	Why     string `json:"why,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// GitChange is one file not committed yet: its path from the root of the repository and the
// two status letters of git status (M modified, A added, D deleted, ?? new).
type GitChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// What went wrong with the person's git, as GitError.Problem names it; the UI says each in
// words, in its own language.
const (
	GitNoGit     = "no_git"     // git is not installed
	GitNotRepo   = "not_repo"   // the project's folder is not in a working copy
	GitAuth      = "auth"       // no access to the repository from this git
	GitIdentity  = "identity"   // git has no name and email to sign a commit with
	GitDiverged  = "diverged"   // the branch and the repository went apart
	GitBehind    = "behind"     // the repository has commits this folder does not
	GitNoMessage = "no_message" // there are changes and no commit message
	GitNoRemote  = "no_remote"  // the working copy has no repository to push to
	GitNoHost    = "no_host"
	GitOffline   = "unreachable"
	GitTimeout   = "timeout"
	GitTLS       = "tls"
	GitNotFound  = "not_found"
)

// GitError is a step of git that failed: what it is, and git's own message for whoever needs it.
type GitError struct {
	// Problem is one of the Git* names, or "" when it is none of the known things.
	Problem string
	Detail  string
}

func (e *GitError) Error() string {
	if e.Detail == "" {
		return "git: " + e.Problem
	}
	return "git: " + e.Detail
}

func gitErr(problem, detail string) error { return &GitError{Problem: problem, Detail: detail} }

// The person's own git does the work: their name, their access to the repository (a credential
// helper or an SSH key), nothing kept by Kamerton.
func (l *Local) git(ctx context.Context, args ...string) (string, error) {
	l.mu.Lock()
	cur := l.cur
	l.mu.Unlock()
	if cur == nil {
		return "", ErrNotOpen
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cur.Path}, args...)...)
	// No prompt for a password: there is nobody at a terminal to answer it.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "LC_ALL=C")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", gitErr(GitNoGit, "")
		}
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", gitErr(GitTimeout, msg)
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", gitErr(gitProblem(msg), msg)
	}
	return out.String(), nil
}

// gitProblem names the usual reasons git fails by its message (in English: git runs with LC_ALL=C).
func gitProblem(msg string) string {
	low := strings.ToLower(msg)
	has := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(low, p) {
				return true
			}
		}
		return false
	}
	switch {
	case has("not a git repository"):
		return GitNotRepo
	case has("please tell me who you are", "author identity unknown"):
		return GitIdentity
	case has("could not resolve host", "could not resolve hostname", "no such host"):
		return GitNoHost
	case has("ssl certificate", "server certificate", "certificate verify"):
		return GitTLS
	case has("timed out"):
		return GitTimeout
	case has("connection refused", "network is unreachable", "couldn't connect", "failed to connect"):
		return GitOffline
	case has("repository not found", "does not appear to be a git repository") && !has("'origin'"):
		return GitNotFound
	case has("no configured push destination", "'origin' does not appear to be a git repository", "no such remote"):
		return GitNoRemote
	case has("could not read username", "could not read password", "authentication failed", "permission denied",
		"terminal prompts disabled", "host key verification failed"):
		return GitAuth
	case has("not possible to fast-forward", "diverged", "divergent branches"):
		return GitDiverged
	case has("rejected", "fetch first", "non-fast-forward"):
		return GitBehind
	}
	return ""
}

var branchLine = regexp.MustCompile(`^## (?:No commits yet on )?(.+?)(?:\.\.\.(\S+))?(?: \[(.+)\])?$`)

// GitState reads the working copy without touching the network.
func (l *Local) GitState(ctx context.Context) GitState {
	out, err := l.git(ctx, "-c", "core.quotePath=false", "status", "--porcelain=v1", "--branch", "--untracked-files=all", "--", ".")
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) {
			return GitState{Why: ge.Detail, Problem: ge.Problem}
		}
		return GitState{Why: err.Error()}
	}
	st := GitState{Available: true, Changes: []GitChange{}}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		if i == 0 && strings.HasPrefix(line, "## ") {
			if m := branchLine.FindStringSubmatch(line); m != nil {
				st.Branch, st.Upstream = m[1], m[2]
				for _, part := range strings.Split(m[3], ", ") {
					if n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(part, "ahead "), "behind ")); err == nil {
						if strings.HasPrefix(part, "ahead ") {
							st.Ahead = n
						} else if strings.HasPrefix(part, "behind ") {
							st.Behind = n
						}
					}
				}
			}
			continue
		}
		if len(line) > 3 {
			st.Changes = append(st.Changes, GitChange{Status: strings.TrimSpace(line[:2]), Path: strings.Trim(line[3:], `"`)})
		}
	}
	return st
}

// Fetch asks the repository what it has, so the state shows the commits that came there; the
// working copy is not changed.
func (l *Local) Fetch(ctx context.Context) (GitState, error) {
	if _, err := l.git(ctx, "fetch", "--quiet"); err != nil {
		return l.GitState(ctx), err
	}
	return l.GitState(ctx), nil
}

// Pull brings the branch up to the repository when it only follows on; a branch that went
// apart is left for the person to merge in their git.
func (l *Local) Pull(ctx context.Context) (GitState, error) {
	if _, err := l.git(ctx, "pull", "--ff-only"); err != nil {
		return l.GitState(ctx), err
	}
	return l.GitState(ctx), nil
}

// Push commits the changes of the project's folder with the message, if there are any, and
// pushes the branch. Files outside the project's folder are not touched.
func (l *Local) Push(ctx context.Context, message string) (GitState, error) {
	st := l.GitState(ctx)
	if !st.Available {
		return st, gitErr(st.Problem, st.Why)
	}
	if len(st.Changes) > 0 {
		message = strings.TrimSpace(message)
		if message == "" {
			return st, gitErr(GitNoMessage, "")
		}
		// Without a name of its own git makes one up from the account and the host name, and
		// that address would go into the repository's history for good.
		if os.Getenv("GIT_AUTHOR_EMAIL") == "" {
			if email, _ := l.git(ctx, "config", "user.email"); strings.TrimSpace(email) == "" {
				return st, gitErr(GitIdentity, "user.email is not set")
			}
		}
		if _, err := l.git(ctx, "add", "--all", "--", "."); err != nil {
			return l.GitState(ctx), err
		}
		if _, err := l.git(ctx, "commit", "--quiet", "-m", message, "--", "."); err != nil {
			return l.GitState(ctx), err
		}
	}
	args := []string{"push", "--quiet"}
	if st.Upstream == "" {
		// The first push of a branch sets where it goes, the way git suggests.
		args = append(args, "--set-upstream", "origin", "HEAD")
	}
	if _, err := l.git(ctx, args...); err != nil {
		return l.GitState(ctx), err
	}
	return l.GitState(ctx), nil
}
