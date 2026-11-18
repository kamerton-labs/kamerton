// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useT, type MsgKey } from '@/i18n'
import type { GitCheck } from '@/lib/api'

const known: Record<string, MsgKey> = {
  address: 'gits.problem.address',
  no_host: 'gits.problem.no_host',
  unreachable: 'gits.problem.unreachable',
  timeout: 'gits.problem.timeout',
  tls: 'gits.problem.tls',
  auth: 'gits.problem.auth',
  not_found: 'gits.problem.not_found',
  no_branch: 'gits.problem.no_branch',
}

/** What is wrong with a repository, in words; the message of git under it for whoever needs it. */
export default function RepoProblem({ check }: { check: GitCheck }) {
  const { t } = useT()
  const key = check.problem ? known[check.problem] : undefined
  return (
    <div data-testid="repo-problem">
      <p className="text-destructive">{key ? t(key) : check.error}</p>
      {key && check.error && <p className="mt-0.5 text-xs text-muted-foreground [overflow-wrap:anywhere]">{check.error}</p>}
    </div>
  )
}
