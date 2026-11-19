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
	// Why is set when Available is false.
	Why string `json:"why,omitempty"`
}

// GitChange is one file not committed yet: its path from the root of the repository and the
// two status letters of git status (M modified, A added, D deleted, ?? new).
type GitChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

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
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", userErr("git is not installed on this machine")
		}
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", userErr(explainGit(msg))
	}
	return out.String(), nil
}

// explainGit puts the usual reasons first, in words; git's own message follows.
func explainGit(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "could not read username"), strings.Contains(low, "authentication failed"),
		strings.Contains(low, "permission denied"), strings.Contains(low, "terminal prompts disabled"):
		return "no access to the repository: set it up in your own git (a credential helper or an SSH key) and try again. git: " + msg
	case strings.Contains(low, "please tell me who you are"), strings.Contains(low, "author identity unknown"):
		return "git does not know your name: run git config --global user.name and user.email. git: " + msg
	case strings.Contains(low, "not possible to fast-forward"), strings.Contains(low, "diverged"):
		return "the branch and the repository went apart: merge them in your git, Kamerton only pulls what follows on. git: " + msg
	case strings.Contains(low, "rejected"), strings.Contains(low, "fetch first"):
		return "the repository has commits this folder does not: pull first. git: " + msg
	}
	return "git: " + msg
}

var branchLine = regexp.MustCompile(`^## (?:No commits yet on )?(.+?)(?:\.\.\.(\S+))?(?: \[(.+)\])?$`)

// GitState reads the working copy without touching the network.
func (l *Local) GitState(ctx context.Context) GitState {
	out, err := l.git(ctx, "status", "--porcelain=v1", "--branch", "--untracked-files=all", "--", ".")
	if err != nil {
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
		return st, userErr(st.Why)
	}
	if len(st.Changes) > 0 {
		message = strings.TrimSpace(message)
		if message == "" {
			return st, userErr("write what the change is: the commit message is empty")
		}
		// Without a name of its own git makes one up from the account and the host name, and
		// that address would go into the repository's history for good.
		if os.Getenv("GIT_AUTHOR_EMAIL") == "" {
			if email, _ := l.git(ctx, "config", "user.email"); strings.TrimSpace(email) == "" {
				return st, userErr("git does not know your name and email: run git config --global user.name and user.email (a noreply address keeps your own private), then try again")
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
