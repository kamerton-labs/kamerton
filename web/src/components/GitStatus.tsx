// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { GitBranch } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { useT } from '@/i18n'
import type { GitStatus as Status } from '@/lib/api'
import { timeAgo } from '@/lib/format'

/** Where system descriptions are read from, and which files Kamerton could not apply. */
export default function GitStatus({ git }: { git: Status }) {
  const { t } = useT()
  const problems = git.files.filter((f) => f.state !== 'applied' && f.state !== 'unchanged')
  const bad = !!git.error || problems.length > 0
  const where = git.repo + (git.path ? ` · ${git.path}` : '')
  return (
    <Alert variant={bad ? 'destructive' : 'default'} data-testid="git-status">
      <GitBranch />
      <AlertTitle>{t('git.title', { branch: git.branch })}</AlertTitle>
      <AlertDescription>
        <p className="[overflow-wrap:anywhere]">{where}</p>
        {git.commit ? (
          <p>{t('git.commit', { commit: git.commit.slice(0, 8), ago: git.checkedAt ? timeAgo(git.checkedAt) : '' })}</p>
        ) : (
          !git.error && <p>{t('git.waiting')}</p>
        )}
        {git.error && <p data-testid="git-error">{t('git.error', { error: git.error })}</p>}
        {problems.length > 0 && (
          <ul className="space-y-0.5" data-testid="git-problems">
            {problems.map((f) => (
              <li key={f.path} className="[overflow-wrap:anywhere]">
                {t(f.state === 'conflict' ? 'git.fileConflict' : 'git.fileInvalid', { path: f.path, error: f.error ?? '' })}
              </li>
            ))}
          </ul>
        )}
        {!bad && <p>{t(git.write === 'pullRequest' ? 'git.hintPropose' : git.write === 'commit' ? 'git.hintCommit' : 'git.hint')}</p>}
      </AlertDescription>
    </Alert>
  )
}
