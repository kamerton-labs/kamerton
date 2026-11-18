// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Link } from 'react-router'
import { useState } from 'react'
import { ArrowUp, ExternalLink, GitPullRequest, Loader2, Pencil, RefreshCw } from 'lucide-react'
import { Page, StateDot } from '@/components/common'
import GitCommitDialog, { ChangeSummary } from '@/components/GitCommitDialog'
import GitSettingsForm from '@/components/GitSettingsForm'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useT } from '@/i18n'
import { useGitChanges, useGitCommits, useGitDiscard, useGitPush, useGitSettings, useGitSync, useSystems, type GitProposal, type GitSettingsView, type GitStatus } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { gitProblems, projectName } from '@/lib/git'

const ext = 'inline-flex items-center gap-1 underline underline-offset-2'

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[9rem_1fr] gap-3 py-1.5 text-sm">
      <div className="text-muted-foreground">{label}</div>
      <div className="min-w-0 [overflow-wrap:anywhere]">{children}</div>
    </div>
  )
}

function Section({ title, children, testid }: { title: string; children: React.ReactNode; testid?: string }) {
  return (
    <section className="rounded-xl border bg-card p-4" data-testid={testid}>
      <h2 className="mb-2 text-sm font-medium">{title}</h2>
      {children}
    </section>
  )
}

/** The project's repository: where descriptions come from, what is applied, what waits. */
export default function GitPage() {
  const systems = useSystems()
  const settings = useGitSettings()
  const { t } = useT()
  const git = systems.data?.git
  if (systems.isLoading)
    return (
      <Page title={t('gitp.title')}>
        <Skeleton className="h-40" />
      </Page>
    )
  if (!git)
    return (
      <Page title="Git" description={t('gitp.off.lead')}>
        <section className="max-w-2xl rounded-xl border bg-card p-4" data-testid="git-off">
          {!settings.data ? (
            <Skeleton className="h-40" />
          ) : settings.data.available && !settings.data.locked ? (
            <GitSettingsForm view={settings.data} />
          ) : (
            <p className="text-sm text-muted-foreground">{t('gitp.off.unavailable')}</p>
          )}
        </section>
      </Page>
    )
  return <Connected git={git} settings={settings.data} ids={new Map((systems.data?.systems ?? []).map((s) => [s.name, s.id]))} />
}

function Connected({ git, settings, ids }: { git: GitStatus; settings?: GitSettingsView; ids: Map<string, string> }) {
  const { t, tn } = useT()
  const sync = useGitSync()
  const problems = gitProblems(git)
  const changes = useGitChanges(true)
  const commits = useGitCommits(!git.empty)
  const push = useGitPush()
  const discard = useGitDiscard()
  const [committing, setCommitting] = useState(false)
  const [editing, setEditing] = useState(false)
  const [pushed, setPushed] = useState<GitProposal | null>(null)
  const saved = changes.data?.changes ?? []
  const target = t(git.write === 'pullRequest' && !git.empty ? 'gitc.where.propose' : 'gitc.where.commit', { branch: git.branch })
  return (
    <Page
      title={projectName(git)}
      actions={
        <>
          {settings && !settings.locked && (
            <Button variant="ghost" size="sm" onClick={() => setEditing(true)} data-testid="git-edit">
              <Pencil /> {t('gitp.edit')}
            </Button>
          )}
        <Button variant="outline" size="sm" onClick={() => sync.mutate()} disabled={sync.isPending} data-testid="git-check-now">
          {sync.isPending ? <Loader2 className="animate-spin" /> : <RefreshCw />} {t(sync.isPending ? 'git.checking' : 'git.checkNow')}
        </Button>
        </>
      }
    >
      {git.error && (
        <Alert variant="destructive" data-testid="git-error">
          <AlertTitle>{t('gitp.errorTitle')}</AlertTitle>
          <AlertDescription>{t('git.error', { error: git.error })}</AlertDescription>
        </Alert>
      )}

      {pushed && (
        <p className="text-sm" data-testid="git-pushed">
          {pushed.direct ? (
            t('system.gitPushed', { branch: pushed.branch })
          ) : pushed.url ? (
            <>
              {t('system.gitProposed')}{' '}
              <a className={ext} href={pushed.url} target="_blank" rel="noreferrer">
                {pushed.url}
              </a>
            </>
          ) : (
            t('system.gitPushed', { branch: pushed.branch })
          )}
        </p>
      )}

      {saved.length > 0 && (
        <section className="rounded-xl border bg-card p-4" data-testid="git-changes">
          <div className="mb-2 flex items-center justify-between gap-3">
            <h2 className="text-sm font-medium">{tn('gitp.changes', saved.length)}</h2>
            <Button size="sm" onClick={() => setCommitting(true)} data-testid="git-push">
              <ArrowUp /> Push
            </Button>
          </div>
          <ul className="divide-y">
            {saved.map((c) => (
              <li key={c.system} className="flex items-start justify-between gap-3 py-2 text-sm">
                <ChangeSummary c={c} />
                <button type="button" className="shrink-0 text-xs text-muted-foreground underline underline-offset-2" onClick={() => discard.mutate(c.system)} disabled={discard.isPending}>
                  {t('gitp.discard')}
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}

      <div>
        <Section title={t('gitp.repo')} testid="git-repo">
          <Row label={t('gitp.address')}>
            {git.repoURL ? (
              <a className={ext} href={git.repoURL} target="_blank" rel="noreferrer">
                {git.repo} <ExternalLink className="size-3" />
              </a>
            ) : (
              git.repo
            )}
          </Row>
          <Row label={t('gitp.branch')}>{git.branch}</Row>
          <Row label={t('gitp.applied')}>
            {git.commit ? (
              <>
                {git.commitURL ? (
                  <a className={`${ext} font-mono`} href={git.commitURL} target="_blank" rel="noreferrer">
                    {git.commit.slice(0, 8)}
                  </a>
                ) : (
                  <span className="font-mono">{git.commit.slice(0, 8)}</span>
                )}
                {git.checkedAt && <span className="text-muted-foreground"> · {t('gitp.checked', { ago: timeAgo(git.checkedAt) })}</span>}
              </>
            ) : (
              <span className="text-muted-foreground">{t(git.empty ? (git.write ? 'git.emptyWrite' : 'git.empty') : 'git.waiting')}</span>
            )}
          </Row>
          <Row label={t('gitp.systemsPath')}>
            <span className="font-mono">{git.systemsPath || '/'}</span>
          </Row>
          {git.generatedPath && (
            <Row label={t('gitp.generatedPath')}>
              <span className="font-mono">{git.generatedPath}</span>
            </Row>
          )}
          <Row label={t('gitp.saving')}>{t(git.write === 'pullRequest' ? 'gitp.saving.propose' : git.write === 'commit' ? 'gitp.saving.commit' : 'gitp.saving.off')}</Row>
        </Section>

      </div>

      {git.requests && git.requests.length > 0 && (
      <Section title={tn('gitp.requests', git.requests.length)} testid="git-requests">
        {(
          <ul className="divide-y">
            {git.requests.map((r) => (
              <li key={r.url} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-sm">
                <GitPullRequest className="size-4 shrink-0 text-muted-foreground" />
                <a className={ext} href={r.url} target="_blank" rel="noreferrer">
                  {r.title}
                </a>
                <span className="text-muted-foreground">{r.system}</span>
                <span className="ml-auto font-mono text-xs text-muted-foreground">{r.branch}</span>
              </li>
            ))}
          </ul>
        )}
      </Section>
      )}

      {commits.data && commits.data.commits.length > 0 && (
        <Section title="Commits" testid="git-commits">
          <ul className="divide-y">
            {commits.data.commits.map((c) => (
              <li key={c.hash} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 py-2 text-sm">
                {c.url ? (
                  <a className="font-mono text-xs underline underline-offset-2" href={c.url} target="_blank" rel="noreferrer">
                    {c.hash.slice(0, 8)}
                  </a>
                ) : (
                  <span className="font-mono text-xs">{c.hash.slice(0, 8)}</span>
                )}
                <span className="min-w-0 [overflow-wrap:anywhere]">{c.title}</span>
                {c.systems && c.systems.length > 0 && <span className="text-xs text-muted-foreground">{c.systems.join(', ')}</span>}
                {c.applied && <span className="rounded bg-ok/10 px-1.5 text-xs text-ok">{t('gitp.state.applied')}</span>}
                <span className="ml-auto text-xs text-muted-foreground">
                  {c.author} · {timeAgo(c.when)}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}

      <Section title={tn('gitp.files', git.files.length)} testid="git-files">
        {git.files.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('gitp.files.none')}</p>
        ) : (
          <ul className="divide-y">
            {git.files.map((f) => {
              const bad = f.state !== 'applied' && f.state !== 'unchanged'
              const id = f.system ? ids.get(f.system) : undefined
              return (
                <li key={f.path} className="py-2 text-sm" data-state={f.state}>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <StateDot state={bad ? 'firing' : 'ok'} />
                    <span className="font-mono text-xs">{f.path}</span>
                    {id && !bad ? (
                      <Link className="underline underline-offset-2" to={`/systems/${encodeURIComponent(id)}`}>
                        {f.system}
                      </Link>
                    ) : (
                      <span>{f.system}</span>
                    )}
                    <span className="ml-auto text-xs text-muted-foreground">{t(bad ? (f.state === 'conflict' ? 'gitp.state.conflict' : 'gitp.state.invalid') : 'gitp.state.applied')}</span>
                  </div>
                  {bad && f.error && <p className="mt-1 pl-5 text-xs text-destructive [overflow-wrap:anywhere]">{f.error}</p>}
                </li>
              )
            })}
          </ul>
        )}
        {problems > 0 && <p className="mt-2 text-xs text-muted-foreground">{t('gitp.files.kept')}</p>}
      </Section>
      {editing && settings && (
        <Dialog open onOpenChange={(v) => !v && setEditing(false)}>
          <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl">
            <DialogHeader>
              <DialogTitle>{t('gitp.repo')}</DialogTitle>
              <DialogDescription className="sr-only">{t('gitp.edit')}</DialogDescription>
            </DialogHeader>
            <GitSettingsForm view={settings} onDone={() => setEditing(false)} />
          </DialogContent>
        </Dialog>
      )}
      {committing && (
        <GitCommitDialog
          changes={saved}
          target={target}
          pending={push.isPending}
          error={push.error?.message}
          onClose={() => setCommitting(false)}
          onConfirm={(message) =>
            push.mutate(
              { message },
              {
                onSuccess: (res) => {
                  setPushed(res.proposal)
                  setCommitting(false)
                },
              },
            )
          }
        />
      )}
    </Page>
  )
}
