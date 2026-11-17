// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package web embeds the built web UI (web/dist, produced by `make ui`).
//
// web/dist/.gitkeep is committed so the Go module compiles before the UI is
// built; in that case the server answers with a short "UI not built" page.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the UI file system and whether a built UI (index.html) is present.
func Dist() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return sub, false
	}
	return sub, true
}
