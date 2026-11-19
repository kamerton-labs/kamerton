// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package version exposes build information set with -ldflags.
package version

// Set at build time: -ldflags "-X github.com/kamerton-labs/kamerton/internal/version.Version=v0.3.1".
var (
	Version = "0.3.1"
	Commit  = "unknown"
)
