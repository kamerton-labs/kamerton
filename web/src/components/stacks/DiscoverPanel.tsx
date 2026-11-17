// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// "Find installed": programs found through the Kubernetes API (cluster
// Services confirmed by an HTTP check). Selectors of Prometheus Operator
// resources fill in the labels Kamerton's objects need.

import { useState } from 'react'
import { AlertTriangle, Loader2, Plus, SearchX, X } from 'lucide-react'
import { Chip, StatusDot } from '@/components/notifications/parts'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { useSetStackAddress, useStackDiscovery, useUpdateStack, type FoundProgram, type StackSet } from '@/lib/api'
import { useT } from '@/i18n'
import { checkText, checkTone, draftFromFound, groupFound, programInfo, stackName, suggestedLabels, type StackDraft } from '@/lib/stacks'

const key = (f: FoundProgram) => `${f.namespace}/${f.service}`

function FoundRow({ f, sets, selected, onToggle }: { f: FoundProgram; sets: StackSet[]; selected: boolean; onToggle: () => void }) {
  const put = useSetStackAddress()
  const update = useUpdateStack()
  const { t } = useT()
  const usedNames = f.usedBy.map((id) => stackName(id, sets.find((s) => s.id === id)?.name))
  const labels = suggestedLabels(f)
  const op = f.operator
  const usable = !!f.url
  return (
    <li data-testid={`found-${f.namespace}-${f.service}`} className="flex flex-wrap items-start gap-x-3 gap-y-1.5 px-3 py-2.5">
      <Checkbox className="mt-0.5" checked={selected} disabled={!usable} onCheckedChange={onToggle} aria-label={t('discover.select', { service: f.service, ns: f.namespace })} />
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="text-sm font-medium">{f.service}</span>
          <span className="text-xs text-muted-foreground">{t('discover.namespace', { ns: f.namespace })}</span>
          <StatusDot tone={checkTone(f.state)}>{checkText(f.state)}</StatusDot>
          {f.version && <span className="text-xs text-muted-foreground">{t('shell.version', { v: f.version })}</span>}
          {usedNames.map((n) => (
            <Chip key={n}>{t('discover.inSet', { name: n })}</Chip>
          ))}
        </div>
        {f.url && <div className="truncate font-mono text-xs text-muted-foreground">{f.url}</div>}
        {f.state !== 'ok' && <p className="text-xs text-muted-foreground">{f.message}</p>}
        {op && (
          <p className="text-xs text-muted-foreground">
            {op.kind} {op.namespace}/{op.name}:{' '}
            {op.selectsNone
              ? t('discover.selectsNone')
              : op.selectsAll
                ? t('discover.selectsAll', { ns: op.namespaces })
                : t('discover.selects', {
                    labels: Object.entries(op.labels)
                      .map(([k, v]) => `${k}=${v}`)
                      .join(', '),
                    expr: op.hasExpressions ? t('discover.expressions') : '',
                    ns: op.namespaces,
                  })}
          </p>
        )}
        {(put.isError || update.isError) && <p className="text-xs text-destructive">{t('common.failed', { error: (put.error ?? update.error)?.message ?? '' })}</p>}
      </div>
      {usable && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="xs" data-testid={`found-put-${f.namespace}-${f.service}`} disabled={put.isPending}>
              {put.isPending ? <Loader2 className="animate-spin" /> : <Plus />} {t('discover.toSet')}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-64">
            <DropdownMenuLabel className="text-xs text-muted-foreground">{t('discover.putAs', { program: programInfo[f.kind].title })}</DropdownMenuLabel>
            {sets.map((s) => (
              <DropdownMenuItem
                key={s.id}
                onSelect={() =>
                  put.mutate(
                    { id: s.id, kind: f.kind, url: f.url },
                    {
                      onSuccess: () => {
                        // The selector of the found instance tells which labels its objects need.
                        if (!labels || Object.keys(labels).length === 0) return
                        if (f.kind === 'prometheus') update.mutate({ id: s.id, ruleLabels: { ...s.ruleLabels, ...labels } })
                        if (f.kind === 'alertmanager') update.mutate({ id: s.id, alertmanagerConfigLabels: { ...s.alertmanagerConfigLabels, ...labels } })
                      },
                    },
                  )
                }
              >
                {t('discover.setName', { name: stackName(s.id, s.name) })}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </li>
  )
}

export default function DiscoverPanel({ sets, onClose, onCreateSet }: { sets: StackSet[]; onClose: () => void; onCreateSet: (d: StackDraft) => void }) {
  const disc = useStackDiscovery(true)
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  const { t } = useT()
  const data = disc.data
  const found = data?.available ? data.found : []
  const chosen = found.filter((f) => selected[key(f)])
  return (
    <section aria-label={t('discover.aria')} data-testid="discover-panel" className="overflow-hidden rounded-xl bg-card text-card-foreground ring-1 ring-foreground/10">
      <header className="flex items-center gap-3 px-5 py-4">
        <div className="min-w-0 flex-1">
          <h2 className="text-base leading-tight font-semibold">{t('discover.title')}</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">{t('discover.description')}</p>
        </div>
        <Button variant="ghost" size="sm" onClick={() => void disc.refetch()} disabled={disc.isFetching}>
          {disc.isFetching ? <Loader2 className="animate-spin" /> : null} {t('discover.again')}
        </Button>
        <Button variant="ghost" size="icon-sm" aria-label={t('common.close')} onClick={onClose}>
          <X />
        </Button>
      </header>
      <div className="space-y-4 border-t px-5 py-5">
        {disc.isLoading ? (
          <Skeleton className="h-32" />
        ) : disc.isError ? (
          <p className="text-sm text-destructive">{t('discover.error', { error: disc.error.message })}</p>
        ) : data && !data.available ? (
          <div className="flex gap-3 text-sm" data-testid="discover-unavailable">
            <SearchX className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <p>{data.reason}</p>
          </div>
        ) : data?.available ? (
          <>
            {data.warnings.map((w) => (
              <p key={w} className="flex gap-2 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2 text-xs">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warn" />
                {w}
              </p>
            ))}
            {!data.operator.available && data.operator.reason && <p className="text-xs text-muted-foreground">{data.operator.reason}</p>}
            {found.length === 0 && <p className="text-sm text-muted-foreground">{t('discover.none')}</p>}
            {groupFound(found).map((g) => (
              <div key={g.kind} className="space-y-1.5">
                <h3 className="text-sm font-medium">{programInfo[g.kind].title}</h3>
                <ul className="divide-y rounded-lg border">
                  {g.items.map((f) => (
                    <FoundRow key={key(f)} f={f} sets={sets} selected={!!selected[key(f)]} onToggle={() => setSelected((s) => ({ ...s, [key(f)]: !s[key(f)] }))} />
                  ))}
                </ul>
              </div>
            ))}
            {found.length > 0 && (
              <div className="flex flex-wrap items-center gap-3 border-t pt-4">
                <Button data-testid="create-from-found" disabled={chosen.length === 0} onClick={() => onCreateSet(draftFromFound(chosen))}>
                  <Plus /> {t('discover.create')}
                </Button>
                <span className="text-xs text-muted-foreground">{chosen.length === 0 ? t('discover.pickHint') : t('discover.picked', { n: chosen.length })}</span>
              </div>
            )}
          </>
        ) : null}
      </div>
    </section>
  )
}
