// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only
// Vite empties web/dist on every build. Restore the committed placeholder so
// the Go package `web` (go:embed all:dist) always has something to embed.
import { writeFileSync } from 'node:fs'

writeFileSync(new URL('../dist/.gitkeep', import.meta.url), '')
