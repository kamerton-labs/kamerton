// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// One set of programs on the status page: six program cards (state,
// address, where it comes from, version), the systems using the set and
// what can be done with it.

import { useState } from 'react'
import { Link } from 'react-router'
import { Activity, AlertTriangle, BellRing, ChartLine, FileText, Layers, MoreHorizontal, Pencil, Radio, Trash2, Waypoints, type LucideIcon } from 'lucide-react'
import { useConfirm } from '@/components/ConfirmDialog'
import { Chip, IconTile, StatusDot } from '@/components/notifications/parts'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { useDeleteStack, type ProgramKind, type StackSet } from '@/lib/api'
import { useT } from '@/i18n'
import { KINDS, originLabel, programInfo, programRole, programState, stackName } from '@/lib/stacks'
import AddressDialog from './AddressDialog'
import StackFormDialog from './StackFormDialog'

const kindIcon: Record<ProgramKind, LucideIcon> = {
  prometheus: Activity,
  alertmanager: BellRing,
  grafana: ChartLine,
  loki: FileText,
  tempo: Waypoints,
  'otel-collector': Radio,
}

function ProgramCard({ set, kind, onChange }: { set: StackSet; kind: ProgramKind; onChange: () => void }) {
  const program = set.programs.find((p) => p.kind === kind)
  const health = set.health.find((h) => h.name === kind)
  const info = programInfo[kind]
  const { t } = useT()
  const st = programState(health)
  const origin = program?.origin ?? 'none'
  return (
    <div data-testid={`program-${set.id}-${kind}`} className="flex min-w-0 flex-col gap-2 rounded-lg border bg-background p-3 dark:bg-input/20">
      <div className="flex items-start gap-2.5">
        <IconTile icon={kindIcon[kind]} size="sm" tint={st.tone === 'ok' ? 'ok' : st.tone === 'muted' ? 'neutral' : 'warn'} />
        <div className="min-w-0 flex-1">
          <div className="text-sm leading-tight font-medium">{info.title}</div>
          <div className="truncate text-xs text-muted-foreground">{programRole(kind)}</div>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <StatusDot tone={st.tone} className="font-medium">
          {st.text}
        </StatusDot>
        {origin !== 'none' && <Chip tone={origin === 'bundled' ? 'neutral' : 'muted'}>{originLabel(origin)}</Chip>}
        {health?.version && <span className="text-xs text-muted-foreground">{t('shell.version', { v: health.version })}</span>}
      </div>
      {program?.url ? (
        <div className="truncate font-mono text-xs text-muted-foreground" title={program.url}>
          {program.url}
        </div>
      ) : (
        <div className="text-xs text-muted-foreground">{t('stackBlock.noAddress')}</div>
      )}
      {health?.configured && !health.healthy && health.message && <p className="text-xs text-muted-foreground">{health.message}</p>}
      {set.builtin && program?.overridden && <p className="text-xs text-muted-foreground">{t('stackBlock.atInstall', { url: program.flagURL || t('stackBlock.notSet') })}</p>}
      <Button variant="ghost" size="xs" className="mt-auto -ml-2 self-start" data-testid={`change-address-${set.id}-${kind}`} onClick={onChange}>
        <Pencil /> {t('stackBlock.changeAddress')}
      </Button>
    </div>
  )
}

function SystemsLine({ set }: { set: StackSet }) {
  const { t, tn } = useT()
  if (set.usedBy.length === 0) return <span className="text-xs text-muted-foreground">{t('stackBlock.unused')}</span>
  return (
    <span className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
      {tn('stackBlock.systems', set.usedBy.length)}
      {set.usedBy.map((id) => (
        <Link
          key={id}
          to={`/systems/${encodeURIComponent(id)}`}
          className="rounded-full border bg-background px-2 py-0.5 font-medium text-foreground outline-none hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/50"
        >
          {id}
        </Link>
      ))}
    </span>
  )
}

export default function StackBlock({ set, total }: { set: StackSet; total: number }) {
  const [editing, setEditing] = useState<ProgramKind | null>(null)
  const [renaming, setRenaming] = useState(false)
  const del = useDeleteStack()
  const [confirmDialog, confirm] = useConfirm()
  const { t, tn } = useT()
  const name = stackName(set.id, set.name)
  const deleteBlocked = set.builtin ? t('stackBlock.builtinDelete') : set.usedBy.length > 0 ? t('stackBlock.inUseDelete') : ''
  const labels = Object.keys(set.ruleLabels).length + Object.keys(set.alertmanagerConfigLabels).length
  const title = total > 1 ? t('stackBlock.title', { name }) : t('stackBlock.titleSingle')
  return (
    <section aria-label={title} data-testid={`stack-block-${set.id}`} className="overflow-hidden rounded-xl bg-card text-card-foreground ring-1 ring-foreground/10">
      <header className="flex items-center gap-3 px-5 py-4">
        <IconTile icon={Layers} />
        <div className="min-w-0 flex-1">
          <h2 className="flex flex-wrap items-center gap-2 text-base leading-tight font-semibold">
            {title}
            {set.builtin && total > 1 && <Chip>{t('stackBlock.main')}</Chip>}
            {labels > 0 && <Chip>{tn('stackBlock.labels', labels)}</Chip>}
          </h2>
          <div className="mt-1">
            <SystemsLine set={set} />
          </div>
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" aria-label={t('stackBlock.actions', { name })} data-testid={`stack-actions-${set.id}`}>
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-64">
            <DropdownMenuItem data-testid="stack-rename" onSelect={() => setTimeout(() => setRenaming(true), 0)}>
              <Pencil /> {t('stackBlock.rename')}
            </DropdownMenuItem>
            <DropdownMenuItem
              variant="destructive"
              data-testid="stack-delete"
              disabled={!!deleteBlocked}
              title={deleteBlocked}
              onSelect={async () => {
                const ok = await confirm({ title: t('stackBlock.delete.title', { name }), description: t('stackBlock.delete.text') })
                if (ok) del.mutate(set.id)
              }}
            >
              <Trash2 /> {t('stackBlock.delete')}
            </DropdownMenuItem>
            {deleteBlocked && <p className="px-2 pb-1.5 text-xs text-muted-foreground">{deleteBlocked}.</p>}
          </DropdownMenuContent>
        </DropdownMenu>
      </header>
      <div className="space-y-3 border-t px-5 py-5">
        {set.warnings.map((w) => (
          <p key={w} className="flex gap-2 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2 text-xs">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warn" />
            {w}
          </p>
        ))}
        {del.isError && <p className="text-sm text-destructive">{t('system.deleteError', { error: del.error.message })}</p>}
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {KINDS.map((k) => (
            <ProgramCard key={k} set={set} kind={k} onChange={() => setEditing(k)} />
          ))}
        </div>
      </div>
      {editing && <AddressDialog set={set} kind={editing} onClose={() => setEditing(null)} />}
      {renaming && <StackFormDialog set={set} onClose={() => setRenaming(false)} />}
      {confirmDialog}
    </section>
  )
}
