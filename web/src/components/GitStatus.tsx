// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { GitBranch, Loader2, RefreshCw } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { useT } from '@/i18n'
import { useGitSync, type GitStatus as Status } from '@/lib/api'
import { timeAgo } from '@/lib/format'

/** Where system descriptions are read from, and which files Kamerton could not apply. */
export default function GitStatus({ git }: { git: Status }) {
  const { t, tn } = useT()
  const sync = useGitSync()
  const problems = git.files.filter((f) => f.state !== 'applied' && f.state !== 'unchanged')
  const bad = !!git.error || problems.length > 0
  const where = git.repo + (git.path ? ` · ${git.path}` : '')
  return (
    <Alert variant={bad ? 'destructive' : 'default'} data-testid="git-status">
      <GitBranch />
      <AlertTitle>{t('git.title', { branch: git.branch })}</AlertTitle>
      <AlertDescription>
        <div className="space-y-1.5">
        <p className="[overflow-wrap:anywhere]">
          {git.repoURL ? (
            <a className="underline underline-offset-2" href={git.repoURL} target="_blank" rel="noreferrer">
              {where}
            </a>
          ) : (
            where
          )}
        </p>
        {git.commit ? (
          <p className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span>
              {t('git.commitBefore')}{' '}
              {git.commitURL ? (
                <a className="font-mono underline underline-offset-2" href={git.commitURL} target="_blank" rel="noreferrer">
                  {git.commit.slice(0, 8)}
                </a>
              ) : (
                <span className="font-mono">{git.commit.slice(0, 8)}</span>
              )}
              {t('git.commitAfter', { ago: git.checkedAt ? timeAgo(git.checkedAt) : '' })}
            </span>
            <Button variant="outline" size="sm" className="h-6 px-2 text-xs" onClick={() => sync.mutate()} disabled={sync.isPending} data-testid="git-check-now">
              {sync.isPending ? <Loader2 className="animate-spin" /> : <RefreshCw />} {t(sync.isPending ? 'git.checking' : 'git.checkNow')}
            </Button>
          </p>
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
        {git.requests && git.requests.length > 0 && (
          <div data-testid="git-requests">
            <p>{tn('git.requests', git.requests.length)}</p>
            <ul className="space-y-0.5">
              {git.requests.map((r) => (
                <li key={r.url} className="[overflow-wrap:anywhere]">
                  <a className="underline underline-offset-2" href={r.url} target="_blank" rel="noreferrer">
                    {r.title}
                  </a>
                </li>
              ))}
            </ul>
          </div>
        )}
        {!bad && <p>{t(git.write === 'pullRequest' ? 'git.hintPropose' : git.write === 'commit' ? 'git.hintCommit' : 'git.hint')}</p>}
        </div>
      </AlertDescription>
    </Alert>
  )
}
