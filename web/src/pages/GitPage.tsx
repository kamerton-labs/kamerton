// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Link, useNavigate } from 'react-router'
import { useState } from 'react'
import { ArrowUp, ExternalLink, GitPullRequest, Loader2, Pencil, RefreshCw } from 'lucide-react'
import { Page, StateDot } from '@/components/common'
import { ChangeSummary, defaultMessage } from '@/components/GitChanges'
import GitSettingsForm from '@/components/GitSettingsForm'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { useT } from '@/i18n'
import { useCloseProject, useRenameProject, useGitChanges, useGitCommit, useGitCommits, useGitDiscard, useGitPush, useGitSettings, useGitSync, useProject, useSystems, type GitProposal, type GitSettingsView, type GitStatus, type ProjectInfo } from '@/lib/api'
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

function Section({ title, children, testid, actions }: { title: string; children: React.ReactNode; testid?: string; actions?: React.ReactNode }) {
  return (
    <section className="rounded-xl border bg-card p-4" data-testid={testid}>
      <div className="mb-2 flex min-h-7 items-center justify-between gap-3">
        <h2 className="text-sm font-medium">{title}</h2>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  )
}

/** The project's repository: where descriptions come from, what is applied, what waits. */
export default function GitPage() {
  const systems = useSystems()
  const settings = useGitSettings()
  const project = useProject()
  const { t } = useT()
  const git = systems.data?.git
  if (systems.isLoading)
    return (
      <Page title={t('proj.title')}>
        <Skeleton className="h-40" />
      </Page>
    )
  if (!git)
    return (
      <Page title={project.data?.name || t('proj.title')} description={t('proj.off')}>
        {project.data?.where === 'local' ? (
          <LocalProject info={project.data} />
        ) : (
          <>
          {project.data?.name && (
            <section className="max-w-2xl rounded-xl border bg-card p-4" data-testid="cluster-project">
              <NameRow name={project.data.name} />
              <Row label={t('setup.where')}>{t('setup.whereCluster', { ns: project.data.namespace || 'kamerton' })}</Row>
            </section>
          )}
          <section className="max-w-2xl rounded-xl border bg-card p-4" data-testid="git-off">
            <h2 className="mb-1 text-sm font-medium">Git</h2>
            <p className="mb-4 text-sm text-muted-foreground">{t('gitp.off.lead')}</p>
            {!settings.data ? (
              <Skeleton className="h-40" />
            ) : settings.data.available && !settings.data.locked ? (
              <GitSettingsForm view={settings.data} />
            ) : (
              <p className="text-sm text-muted-foreground">{t('gitp.off.unavailable')}</p>
            )}
          </section>
          </>
        )}
      </Page>
    )
  return <Connected git={git} settings={settings.data} ids={new Map((systems.data?.systems ?? []).map((s) => [s.name, s.id]))} />
}

/** The project's name, renamed in place. */
function NameRow({ name }: { name: string }) {
  const { t } = useT()
  const rename = useRenameProject()
  const [draft, setDraft] = useState<string | null>(null)
  if (draft === null)
    return (
      <Row label={t('setup.name')}>
        <span className="mr-2">{name}</span>
        <button type="button" className="text-xs text-muted-foreground underline underline-offset-2" onClick={() => (setDraft(name), rename.reset())} data-testid="project-rename">
          {t('gitp.rename')}
        </button>
      </Row>
    )
  const save = () => {
    const next = draft.trim()
    if (!next || next === name) return setDraft(null)
    rename.mutate(next, { onSuccess: () => setDraft(null) })
  }
  return (
    <Row label={t('setup.name')}>
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          save()
        }}
      >
        <Input className="h-8 min-w-0 flex-1" value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && setDraft(null)} onFocus={(e) => e.currentTarget.select()} autoFocus maxLength={80} aria-label={t('setup.name')} />
        <Button type="submit" size="sm" disabled={rename.isPending || !draft.trim()}>
          {rename.isPending && <Loader2 className="animate-spin" />}
          {t('system.save')}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={() => setDraft(null)}>
          {t('common.cancel')}
        </Button>
      </form>
      {rename.isError && <p className="mt-1 text-xs text-destructive">{rename.error.message}</p>}
    </Row>
  )
}

/** A project kept in a folder on this machine: where it is, and the way back to the first screen. */
function LocalProject({ info }: { info: ProjectInfo }) {
  const { t } = useT()
  const close = useCloseProject()
  const navigate = useNavigate()
  return (
    <section className="max-w-2xl rounded-xl border bg-card p-4" data-testid="local-project">
      {info.name && <NameRow name={info.name} />}
      <Row label={t('gitp.folder')}>
        <span className="font-mono text-xs">{info.path}</span>
      </Row>
      <Row label="Git">{t(info.git ? 'gitp.localGit' : 'gitp.localNoGit')}</Row>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <Button variant="outline" size="sm" disabled={close.isPending} onClick={() => close.mutate(undefined, { onSuccess: () => void navigate('/', { replace: true }) })} data-testid="project-close">
          {t('gitp.close')}
        </Button>
        <span className="text-xs text-muted-foreground">{t('gitp.closeHint')}</span>
      </div>
    </section>
  )
}

function Connected({ git, settings, ids }: { git: GitStatus; settings?: GitSettingsView; ids: Map<string, string> }) {
  const { t, tn } = useT()
  const sync = useGitSync()
  const problems = gitProblems(git)
  const changes = useGitChanges(true)
  const [commitLimit, setCommitLimit] = useState(10)
  const [openCommit, setOpenCommit] = useState<string | null>(null)
  const commits = useGitCommits(!git.empty, commitLimit)
  const push = useGitPush()
  const discard = useGitDiscard()
  const [message, setMessage] = useState('')
  const [editing, setEditing] = useState(false)
  const [pushed, setPushed] = useState<GitProposal | null>(null)
  const saved = changes.data?.changes ?? []
  const target = t(git.write === 'pullRequest' && !git.empty ? 'gitc.where.propose' : 'gitc.where.commit', { branch: git.branch })
  const doPush = () =>
    push.mutate(
      { message: message.trim() || defaultMessage(saved) },
      {
        onSuccess: (res) => {
          setPushed(res.proposal)
          setMessage('')
        },
      },
    )
  return (
    <Page title={projectName(git)}
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
          <h2 className="mb-2 text-sm font-medium">{tn('gitp.changes', saved.length)}</h2>
          <div className="flex flex-wrap gap-2">
            <Input
              className="min-w-0 flex-1"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              placeholder={defaultMessage(saved)}
              maxLength={120}
              aria-label={t('gitc.message')}
              data-testid="git-commit-message"
              onKeyDown={(e) => e.key === 'Enter' && !push.isPending && doPush()}
            />
            <Button onClick={doPush} disabled={push.isPending} data-testid="git-push" title={target}>
              {push.isPending ? <Loader2 className="animate-spin" /> : <ArrowUp />} Commit &amp; Push
            </Button>
          </div>
          {push.isError && <p className="mt-2 text-sm text-destructive">{push.error.message}</p>}
          <ul className="mt-3 divide-y">
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
        <Section
          title="Git"
          testid="git-repo"
          actions={
            <>
              {settings && !settings.locked && (
                <Button variant="outline" size="sm" onClick={() => setEditing(true)} data-testid="git-edit">
                  <Pencil /> {t('gitp.edit')}
                </Button>
              )}
              <Button variant="outline" size="sm" onClick={() => sync.mutate()} disabled={sync.isPending} data-testid="git-check-now">
                {sync.isPending ? <Loader2 className="animate-spin" /> : <RefreshCw />} {t(sync.isPending ? 'git.checking' : 'git.checkNow')}
              </Button>
            </>
          }
        >
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
              <li key={c.hash} className="py-2 text-sm">
                <button
                  type="button"
                  className="flex w-full flex-wrap items-baseline gap-x-3 gap-y-0.5 text-left"
                  onClick={() => setOpenCommit((h) => (h === c.hash ? null : c.hash))}
                  aria-expanded={openCommit === c.hash}
                  data-testid="git-commit-row"
                >
                  <span className="font-mono text-xs">{c.hash.slice(0, 8)}</span>
                  <span className="min-w-0 [overflow-wrap:anywhere]">{c.title}</span>
                  {c.systems && c.systems.length > 0 && <span className="text-xs text-muted-foreground">{c.systems.join(', ')}</span>}
                  {c.applied && <span className="rounded bg-ok/10 px-1.5 text-xs text-ok">{t('gitp.state.applied')}</span>}
                  <span className="ml-auto text-xs text-muted-foreground">
                    {c.author} · {timeAgo(c.when)}
                  </span>
                </button>
                {openCommit === c.hash && <CommitBody hash={c.hash} url={c.url} />}
              </li>
            ))}
          </ul>
          {commits.data.more && (
            <button
              type="button"
              className="mt-2 text-xs text-muted-foreground underline underline-offset-2"
              onClick={() => setCommitLimit((n) => Math.min(n + 30, 300))}
              disabled={commits.isFetching}
              data-testid="git-commits-more"
            >
              {t(commits.isFetching ? 'git.checking' : 'gitp.commits.more')}
            </button>
          )}
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
    </Page>
  )
}

/** What a commit changed: per description file, the rules in words and the text behind "Diff". */
function CommitBody({ hash, url }: { hash: string; url?: string }) {
  const { t, tn } = useT()
  const detail = useGitCommit(hash)
  if (detail.isLoading) return <Skeleton className="mt-2 h-10" />
  if (!detail.data) return <p className="mt-2 text-xs text-muted-foreground">{t('gitp.commit.unknown')}</p>
  const d = detail.data
  return (
    <div className="mt-2 space-y-3 rounded-md bg-muted/20 p-3" data-testid="git-commit-body">
      {d.message && <p className="text-xs whitespace-pre-line text-muted-foreground">{d.message}</p>}
      {d.files.length === 0 && <p className="text-xs text-muted-foreground">{t('gitp.commit.noSystems')}</p>}
      {d.files.map((f) => (
        <ChangeSummary key={f.path} c={f} />
      ))}
      <p className="flex flex-wrap gap-x-3 text-xs text-muted-foreground">
        {d.others > 0 && <span>{tn('gitp.commit.others', d.others)}</span>}
        {url && (
          <a className={ext} href={url} target="_blank" rel="noreferrer">
            {t('gitp.commit.open')} <ExternalLink className="size-3" />
          </a>
        )}
      </p>
    </div>
  )
}
