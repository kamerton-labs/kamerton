// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// One system: canvas, rules and YAML all edit the same draft. YAML is the
// stored truth; the visual editors write their changes into it.

import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Activity, ArrowLeft, Check, FileCode2, GitBranch, ListChecks, Loader2, MoreHorizontal, Trash2, Workflow } from 'lucide-react'
import { Link, useBlocker, useNavigate, useParams, useSearchParams } from 'react-router'
import { cn } from 'cn'
import SystemCanvas from '@/components/canvas/SystemCanvas'
import { StatePill } from '@/components/common'
import SystemNow from '@/components/live/SystemNow'
import { useConfirm } from '@/components/ConfirmDialog'
import RulesView from '@/components/rules/RulesView'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError, useAlerts, useDeleteSystem, useMoveToGit, useRuleStates, useSaveSystem, useStacks, useSystem, useSystems, useTelegram, useEmail, validateSystem, type Issue, type SystemResponse } from '@/lib/api'
import { t as tCurrent, useT } from '@/i18n'
import { EditorContext, useDebounced, type EditorState } from '@/lib/editor'
import { systemStateText } from '@/lib/format'
import { systemIncidents } from '@/lib/incidents'
import { edit as editDraft, initDraft, isDirty, markSaved, setText, type Draft } from '@/lib/model/draft'
import { deriveDestinations } from '@/lib/model/routing'
import { positionsOf } from '@/lib/model/canvas'
import { getStack } from '@/lib/model/stack'
import type { Description } from '@/lib/model/types'

const YamlView = lazy(() => import('@/components/editor/YamlView'))

type View = 'now' | 'schema' | 'rules' | 'yaml'
const VIEWS = [
  { v: 'now', label: 'system.view.now', icon: Activity },
  { v: 'schema', label: 'system.view.schema', icon: Workflow },
  { v: 'rules', label: 'system.view.rules', icon: ListChecks },
  { v: 'yaml', label: 'system.view.yaml', icon: FileCode2 },
] as const satisfies readonly { v: View; label: string; icon: typeof Workflow }[]

/** Plain-words version of the core's validation paths, e.g. /spec/services/2/dependsOn/0. */
function describeIssue(i: Issue, desc: Description | null): string {
  const m = /^\/spec\/services\/(\d+)(.*)$/.exec(i.path)
  if (m && desc) {
    const s = desc.spec.services[Number(m[1])]
    if (s) return tCurrent('system.issue.service', { name: s.name, message: i.message })
  }
  return i.path ? `${i.path}: ${i.message}` : i.message
}

export default function SystemPage() {
  const { id = '' } = useParams()
  const sys = useSystem(id)
  const { t } = useT()
  if (sys.isLoading) return <Skeleton className="m-6 h-[60svh]" />
  if (sys.isError || !sys.data)
    return (
      <div className="p-6">
        <Alert variant="destructive">
          <AlertTitle>{t('system.openError')}</AlertTitle>
          <AlertDescription>
            {sys.error instanceof ApiError && sys.error.status === 404 ? t('system.notFound', { id }) : sys.error?.message}{' '}
            <Link to="/" className="underline">
              {t('system.backToList')}
            </Link>
          </AlertDescription>
        </Alert>
      </div>
    )
  return <Editor key={id} data={sys.data} />
}

function Editor({ data }: { data: SystemResponse }) {
  const id = data.id
  const { t, rich } = useT()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const view = (params.get('view') as View) || 'now'
  const selected = params.get('service')
  const setView = (v: View) => setParams((p) => (p.set('view', v), p), { replace: true })
  const select = useCallback((s: string | null) => setParams((p) => (s ? p.set('service', s) : p.delete('service'), p), { replace: true }), [setParams])

  const [draft, setDraft] = useState<Draft>(() => initDraft(data.yaml))
  const save = useSaveSystem(id)
  const del = useDeleteSystem()
  const states = useRuleStates(id)
  const tg = useTelegram(id, 15_000)
  const em = useEmail()
  const systems = useSystems()
  const move = useMoveToGit()
  // Writing to git is on for the installation; a system made by hand can be moved there.
  const gitWriteOn = !!systems.data?.git?.write
  const summary = systems.data?.systems.find((s) => s.id === id)
  // The shell's alert counter keeps this query alive, so reading it here sends no extra request.
  const incidents = systemIncidents(useAlerts().data, id)
  const stacks = useStacks()

  // Validate the draft with the core (debounced): the same check as on save.
  const debouncedText = useDebounced(draft.text, 600)
  const validation = useQuery({
    queryKey: ['validate', id, debouncedText],
    queryFn: () => validateSystem(id, debouncedText),
    enabled: !draft.error,
    retry: false,
    placeholderData: (prev) => prev,
  })
  const issues = useMemo(() => (draft.error ? [] : (validation.data?.errors ?? [])), [draft.error, validation.data])
  const dirty = isDirty(draft)
  // While typing in YAML mode the text is briefly invalid; say so only once it stays that way.
  const shownError = useDebounced(draft.error, 1200)
  const syntaxError = draft.error && shownError ? draft.error : null

  const [confirmDialog, confirm] = useConfirm()
  const blocker = useBlocker(({ currentLocation, nextLocation }) => dirty && currentLocation.pathname !== nextLocation.pathname)
  // One question per blocked navigation, even if the blocker re-renders while the dialog is open.
  const askingToLeave = useRef(false)
  useEffect(() => {
    if (blocker.state !== 'blocked' || askingToLeave.current) return
    askingToLeave.current = true
    void confirm({
      title: t('system.leave.title'),
      description: t('system.leave.text'),
      confirmLabel: t('system.leave.confirm'),
    }).then((ok) => {
      askingToLeave.current = false
      if (ok) blocker.proceed()
      else blocker.reset()
    })
  }, [blocker, confirm, t])
  useEffect(() => {
    const h = (e: BeforeUnloadEvent) => {
      if (dirty) e.preventDefault()
    }
    window.addEventListener('beforeunload', h)
    return () => window.removeEventListener('beforeunload', h)
  }, [dirty])

  const editFn = useCallback((fn: (d: Description) => Description) => setDraft((d) => editDraft(d, fn)), [])
  const desc = draft.desc
  const readOnly = !!draft.error || !data.writable
  const ctx = useMemo<EditorState | null>(
    () =>
      desc && {
        systemId: id,
        draft,
        desc,
        readOnly,
        edit: editFn,
        states: states.data?.reachable ? states.data.states : undefined,
        destinations: deriveDestinations(desc),
        telegram: tg.data?.available ? tg.data.status : undefined,
        email: em.data?.available ? em.data.status : undefined,
        issues,
      },
    [id, draft, desc, readOnly, editFn, states.data, tg.data, em.data, issues],
  )

  // Saving a system kept in git keeps the change for a later push; nothing else moves.
  const [staged, setStaged] = useState<boolean>(false)
  const unpushed = staged || !!data.gitSaved || !!systems.data?.gitChanges?.some((c) => c.system === data.name)
  const doSave = () =>
    save.mutate(draft.text, {
      onSuccess: (res) => {
        setStaged(!!res.staged)
        setDraft((d) => markSaved(d))
      },
    })
  const openRequest = systems.data?.git?.requests?.find((r) => r.system === data.name)
  const saveError = save.error instanceof ApiError ? save.error : null
  // A set the description asks for that does not exist: the built-in set is used instead.
  const askedStack = desc ? getStack(desc) : ''
  const missingStack = askedStack && stacks.data && !stacks.data.sets.some((s) => s.id === askedStack) ? askedStack : ''
  const canSave = dirty && !draft.error && issues.length === 0 && data.writable && !save.isPending

  return (
    <div className="flex h-[calc(100svh-3.5rem)] min-h-0 flex-col md:h-svh">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-3 py-2 md:px-4">
        <Button variant="ghost" size="icon-sm" asChild aria-label={t('system.backToList')}>
          <Link to="/">
            <ArrowLeft />
          </Link>
        </Button>
        <div className="flex min-w-0 items-center gap-2">
          <h1 className="truncate text-base font-semibold md:text-lg">{data.name}</h1>
          {summary && (
            <StatePill state={summary.state}>{systemStateText(summary, incidents)}</StatePill>
          )}
        </div>
        <div className="order-last flex w-full gap-0.5 rounded-lg bg-muted p-0.5 sm:order-none sm:ml-2 sm:w-auto">
          {VIEWS.map(({ v, label, icon: Icon }) => (
            <button
              key={v}
              type="button"
              aria-pressed={view === v}
              onClick={() => setView(v)}
              className={cn('flex flex-1 items-center justify-center gap-1.5 rounded-md px-3 py-1 text-sm', view === v ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground hover:text-foreground')}
            >
              <Icon className="size-4" />
              {t(label)}
            </button>
          ))}
        </div>
        <div className="ml-auto flex items-center gap-2">
          <span className="hidden text-xs text-muted-foreground lg:inline">
            {save.isPending ? t('system.saving') : dirty ? t('system.unsaved') : save.isSuccess ? t('system.saved') : ''}
          </span>
          {dirty && (
            <Button variant="ghost" size="sm" onClick={() => setDraft(initDraft(draft.saved))}>
              {t('system.discard')}
            </Button>
          )}
          <Button size="sm" onClick={doSave} disabled={!canSave} data-testid="save-button" className={cn(view === 'now' && !dirty && 'hidden')}>
            {save.isPending ? <Loader2 className="animate-spin" /> : !dirty ? <Check /> : null} {dirty ? t('system.save') : t('system.saved')}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-sm" aria-label={t('common.more')}>
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-72">
              <DropdownMenuLabel className="font-normal">
                <div className="text-xs text-muted-foreground">{t('system.source')}</div>
                <div className="font-mono text-xs break-all">{data.source}</div>
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              {gitWriteOn && !data.git && (
                <DropdownMenuItem
                  data-testid="move-to-git"
                  disabled={dirty || move.isPending}
                  onSelect={() => move.mutate(id, { onSuccess: () => setStaged(true) })}
                >
                  <GitBranch /> {t('system.moveToGit')}
                </DropdownMenuItem>
              )}
              <DropdownMenuItem
                variant="destructive"
                disabled={!data.writable}
                onSelect={async () => {
                  const extra = data.mode === 'cluster' ? t('system.delete.cluster') : t('system.delete.file')
                  const ok = await confirm({ title: t('system.delete.title', { name: data.name }), description: `${t('system.delete.text')} ${extra}` })
                  if (ok)
                    del.mutate(id, {
                      // A system from git is not deleted here: its removal waits for a push.
                      onSuccess: (res) => navigate(res.staged ? '/project' : '/', { replace: true }),
                    })
                }}
              >
                <Trash2 /> {t('system.delete')}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {(syntaxError || issues.length > 0 || saveError || !data.writable || data.git || unpushed || openRequest || del.isError || move.isError || missingStack) && (
        <div className="space-y-2 border-b bg-muted/20 px-3 py-2 md:px-4">
          {syntaxError && (
            <p className="text-sm text-destructive">
              {t('system.yamlError', {
                where: syntaxError.line ? t('system.yamlLine', { n: syntaxError.line }) : '',
                message: syntaxError.message,
              })}
            </p>
          )}
          {issues.length > 0 && (
            <div className="text-sm text-destructive">
              <span className="font-medium">{issues.length === 1 ? t('system.issues.one') : t('system.issues.many')}</span>
              <ul className="mt-0.5 list-disc pl-5">
                {issues.slice(0, 4).map((i) => (
                  <li key={i.path + i.message}>{describeIssue(i, desc)}</li>
                ))}
                {issues.length > 4 && <li>{t('system.issues.more', { n: issues.length - 4 })}</li>}
              </ul>
            </div>
          )}
          {saveError && <p className="text-sm text-destructive">{t('system.saveError', { error: saveError.code === 'conflict' ? t('system.conflict') : saveError.message })}</p>}
          {unpushed && (
            <p className="flex flex-wrap items-center gap-x-2 text-sm" data-testid="git-unpushed">
              {t(data.draft ? 'system.gitDraft' : 'system.gitUnpushed')}
              <Link className="font-medium underline underline-offset-2" to="/project">
                Push
              </Link>
            </p>
          )}
          {openRequest && (
            <p className="text-sm" data-testid="git-open-request">
              {t('system.gitOpenRequest')}{' '}
              <a className="font-medium underline underline-offset-2" href={openRequest.url} target="_blank" rel="noreferrer">
                {openRequest.title}
              </a>
            </p>
          )}
          {data.git ? (
            <p className="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground" data-testid="system-from-git">
              <GitBranch className="size-3.5" />
              {data.gitFileURL ? (
                <a className="font-mono underline underline-offset-2" href={data.gitFileURL} target="_blank" rel="noreferrer">
                  {data.git.path}
                </a>
              ) : (
                <span className="font-mono">{data.git.path}</span>
              )}
              <span className="font-mono">{data.git.commit.slice(0, 8)}</span>
              {!data.gitWrite && <span>{t('system.gitReadOnly')}</span>}
            </p>
          ) : (
            !data.writable && <p className="text-sm text-muted-foreground">{t('system.readOnly')}</p>
          )}
          {del.isError && <p className="text-sm text-destructive">{t('system.deleteError', { error: del.error.message })}</p>}
          {move.isError && <p className="text-sm text-destructive">{t('system.moveToGit.error', { error: move.error.message })}</p>}
          {missingStack && (
            <p className="text-sm text-warn" data-testid="stack-missing">
              {rich('system.stackMissing', {
                name: missingStack,
                link: (
                  <Link to="/status" className="underline">
                    {t('system.stackMissingLink')}
                  </Link>
                ),
              })}
            </p>
          )}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-hidden">
        {!ctx ? (
          view === 'yaml' ? null : (
            <div className="p-6">
              <Alert variant="destructive">
                <AlertTitle>{t('system.unreadable')}</AlertTitle>
                <AlertDescription>{t('system.unreadableHint')}</AlertDescription>
              </Alert>
            </div>
          )
        ) : (
          <EditorContext value={ctx}>
            {view === 'now' && <SystemNow id={id} positions={desc ? positionsOf(desc) : {}} />}
            {view === 'schema' && <SystemCanvas selected={selected && desc?.spec.services.some((s) => s.name === selected) ? selected : null} onSelect={select} />}
            {view === 'rules' && (
              <div className="h-full overflow-y-auto">
                <RulesView />
              </div>
            )}
          </EditorContext>
        )}
        {view === 'yaml' && (
          <Suspense fallback={<Skeleton className="m-4 h-96" />}>
            <YamlView text={draft.text} onChange={(text) => setDraft((d) => setText(d, text))} issues={issues} readOnly={!data.writable} />
          </Suspense>
        )}
      </div>
{confirmDialog}
    </div>
  )
}
