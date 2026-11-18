// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import type { GitCheck, GitStatus } from '@/lib/api'

/** Files Kamerton could not apply at the last read. */
export const gitProblems = (git: GitStatus): number => git.files.filter((f) => f.state !== 'applied' && f.state !== 'unchanged').length

/** The project's name: from the project file, else the repository's last path segment. */
export function projectName(git: GitStatus): string {
  if (git.project) return git.project
  const tail = git.repo.replace(/\.git$/, '').split('/').filter(Boolean).pop()
  return tail || git.repo
}

/**
 * What a checked repository leads to. A project, or a folder that already holds descriptions,
 * is opened as it is; anything else (an empty repository, a folder with nothing of Kamerton's)
 * gets a new project.
 */
export const checkAction = (c: GitCheck): 'open' | 'create' => (c.hasProject || c.files > 0 ? 'open' : 'create')

/** A folder name for a project name, as the core makes it: letters and digits of any script kept. */
export function slug(name: string): string {
  const out = name
    .toLowerCase()
    .replace(/[^\p{L}\p{N}_.]+/gu, '-')
    .replace(/^[-.]+|[-.]+$/g, '')
  return out || 'project'
}

/** The last part of a repository's address without .git. */
export function repoName(address: string): string {
  const tail = address.trim().replace(/\/+$/, '').split(/[/:]/).filter(Boolean).pop() ?? ''
  return tail.replace(/\.git$/, '')
}

/** A folder inside another, with the separator the other one uses. */
export function joinPath(dir: string, name: string): string {
  if (!dir) return name
  const sep = dir.includes('\\') && !dir.includes('/') ? '\\' : '/'
  return dir.replace(/[\\/]+$/, '') + sep + name
}
