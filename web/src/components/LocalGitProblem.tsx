// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useT, type MsgKey } from '@/i18n'

// The problems of internal/project.Git*: what went wrong with the person's own git.
const known: Record<string, MsgKey> = {
  no_git: 'gitp.problem.no_git',
  not_repo: 'gitp.problem.not_repo',
  auth: 'gitp.problem.auth',
  identity: 'gitp.problem.identity',
  diverged: 'gitp.problem.diverged',
  behind: 'gitp.problem.behind',
  no_message: 'gitp.problem.no_message',
  no_remote: 'gitp.problem.no_remote',
  no_host: 'gits.problem.no_host',
  unreachable: 'gits.problem.unreachable',
  timeout: 'gits.problem.timeout',
  tls: 'gits.problem.tls',
  not_found: 'gitp.problem.not_found',
}

/** A step of git that failed, in words of the interface; git's own message under it, small. */
export default function LocalGitProblem({ problem, detail, message, tone = 'error' }: { problem?: string; detail?: string; message?: string; tone?: 'error' | 'muted' }) {
  const { t } = useT()
  const key = problem ? known[problem] : undefined
  const raw = detail ?? message
  return (
    <div data-testid="local-git-problem">
      <p className={tone === 'error' ? 'text-destructive' : undefined}>{key ? t(key) : raw}</p>
      {key && detail && <p className="mt-0.5 font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]">{detail}</p>}
    </div>
  )
}
