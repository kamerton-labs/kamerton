// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Link } from 'react-router'
import { GitBranch } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { useT } from '@/i18n'
import type { GitStatus as Status } from '@/lib/api'
import { gitProblems } from '@/lib/git'

/**
 * Shown on the Systems screen only when the repository needs attention: it cannot be read, or a
 * file could not be applied. The repository itself lives in the sidebar and on its own page.
 */
export default function GitStatus({ git }: { git: Status }) {
  const { t } = useT()
  const problems = git.files.filter((f) => f.state !== 'applied' && f.state !== 'unchanged')
  if (!git.error && gitProblems(git) === 0) return null
  return (
    <Alert variant="destructive" data-testid="git-status">
      <GitBranch />
      <AlertTitle>{t('git.attention')}</AlertTitle>
      <AlertDescription>
        <div className="space-y-1">
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
          <p>
            <Link className="underline underline-offset-2" to="/project">
              {t('git.open')}
            </Link>
          </p>
        </div>
      </AlertDescription>
    </Alert>
  )
}
