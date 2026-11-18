// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package textdiff compares two small texts line by line, for showing what a change of a
// description file is.
package textdiff

import "strings"

// Line is one line of a diff: Op is " " (same), "+" (added), "-" (removed) or "@" (lines left out).
type Line struct {
	Op   string `json:"op"`
	Text string `json:"text,omitempty"`
}

// context is how many unchanged lines are kept around a change.
const context = 2

// maxLines bounds the work: description files are a few hundred lines.
const maxLines = 4000

// Lines returns the difference between two texts with a little context. Nil: no difference.
func Lines(before, after string) []Line {
	a, b := split(before), split(after)
	if len(a) > maxLines || len(b) > maxLines {
		return []Line{{Op: "@", Text: "the file is too long to compare here"}}
	}
	// Longest common subsequence by lines.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var all []Line
	changed := false
	for i, j := 0, 0; i < len(a) || j < len(b); {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			all = append(all, Line{Op: " ", Text: a[i]})
			i++
			j++
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			// The removed line first, then what replaced it.
			all = append(all, Line{Op: "-", Text: a[i]})
			i++
			changed = true
		default:
			all = append(all, Line{Op: "+", Text: b[j]})
			j++
			changed = true
		}
	}
	if !changed {
		return nil
	}
	// Keep only the lines near a change.
	keep := make([]bool, len(all))
	for i, l := range all {
		if l.Op == " " {
			continue
		}
		for k := max(0, i-context); k <= min(len(all)-1, i+context); k++ {
			keep[k] = true
		}
	}
	var out []Line
	gap := false
	for i, l := range all {
		if !keep[i] {
			gap = true
			continue
		}
		if gap && len(out) > 0 {
			out = append(out, Line{Op: "@"})
		}
		gap = false
		out = append(out, l)
	}
	return out
}

func split(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
