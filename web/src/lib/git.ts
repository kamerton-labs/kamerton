// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import type { GitStatus } from '@/lib/api'

/** Files Kamerton could not apply at the last read. */
export const gitProblems = (git: GitStatus): number => git.files.filter((f) => f.state !== 'applied' && f.state !== 'unchanged').length

/** The project's name: from the project file, else the repository's last path segment. */
export function projectName(git: GitStatus): string {
  if (git.project) return git.project
  const tail = git.repo.replace(/\.git$/, '').split('/').filter(Boolean).pop()
  return tail || git.repo
}
