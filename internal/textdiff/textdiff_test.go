// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package textdiff

import (
	"strings"
	"testing"
)

func render(l []Line) string {
	var b strings.Builder
	for _, x := range l {
		b.WriteString(x.Op + x.Text + "\n")
	}
	return b.String()
}

func TestLines(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\n"
	after := "a\nb\nc\nd\nE\nf\ng\nh\nnew\n"
	got := render(Lines(before, after))
	want := " c\n d\n-e\n+E\n f\n g\n h\n+new\n"
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if Lines("same\n", "same") != nil {
		t.Error("a trailing newline is not a difference")
	}
	if got := render(Lines("", "x\ny\n")); got != "+x\n+y\n" {
		t.Errorf("a new file: %q", got)
	}
	far := render(Lines("1\n2\n3\n4\n5\n6\n7\n8\n9\n", "one\n2\n3\n4\n5\n6\n7\n8\nnine\n"))
	if !strings.Contains(far, "@\n") {
		t.Errorf("distant changes must be separated:\n%s", far)
	}
}
