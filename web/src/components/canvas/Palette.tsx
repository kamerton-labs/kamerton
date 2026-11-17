// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// New services by type, and services seen in telemetry but not yet in this system.

import type { ReactNode } from 'react'
import { Plus, Radar } from 'lucide-react'
import { cn } from 'cn'
import { useT } from '@/i18n'
import { useSystemDiscovery } from '@/lib/api'
import { SERVICE_TYPES, type ServiceType } from '@/lib/model/types'
import { DND_TYPE, typeHint, typeIcon, typeShort } from './meta'

export type PaletteItem = { kind: 'type'; type: ServiceType } | { kind: 'discovered'; name: string }

interface Props {
  systemId: string
  disabled?: boolean
  onAdd: (item: PaletteItem) => void
  /** Names already in the draft (not saved yet). */
  present?: Set<string>
  className?: string
}

function Item({ item, disabled, onAdd, children, title }: { item: PaletteItem; disabled?: boolean; onAdd: (i: PaletteItem) => void; children: ReactNode; title?: string }) {
  return (
    <button
      type="button"
      draggable={!disabled}
      disabled={disabled}
      title={title}
      onDragStart={(e) => {
        e.dataTransfer.setData(DND_TYPE, JSON.stringify(item))
        e.dataTransfer.effectAllowed = 'move'
      }}
      onClick={() => onAdd(item)}
      className="group flex w-full cursor-grab items-center gap-2 rounded-md border bg-card px-2 py-1.5 text-left text-sm hover:border-primary/40 hover:bg-muted active:cursor-grabbing disabled:cursor-not-allowed disabled:opacity-50"
    >
      {children}
      <Plus className="ml-auto size-3.5 shrink-0 text-muted-foreground opacity-0 group-hover:opacity-100" />
    </button>
  )
}

export default function Palette({ systemId, disabled, onAdd, present, className }: Props) {
  const disc = useSystemDiscovery(systemId)
  const { t } = useT()
  const fresh = (disc.data?.services ?? [])
    .filter((s) => !s.described && !present?.has(s.name))
    .sort((a, b) => (a.inSystems?.length ?? 0) - (b.inSystems?.length ?? 0) || a.name.localeCompare(b.name))
  return (
    <div className={cn('space-y-4', className)}>
      <section className="space-y-1.5">
        <h3 className="text-xs font-medium text-muted-foreground">{t('palette.addService')}</h3>
        {SERVICE_TYPES.map((ty) => {
          const Icon = typeIcon[ty]
          return (
            <Item key={ty} item={{ kind: 'type', type: ty }} disabled={disabled} onAdd={onAdd} title={typeHint(ty)}>
              <Icon className="size-4 shrink-0 text-muted-foreground" />
              <span>{typeShort(ty)}</span>
            </Item>
          )
        })}
      </section>
      <section className="space-y-1.5">
        <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Radar className="size-3.5" /> {t('palette.found')}
        </h3>
        {disc.isLoading ? (
          <p className="text-xs text-muted-foreground">{t('orient.searching')}</p>
        ) : disc.isError ? (
          <p className="text-xs text-muted-foreground">{t('palette.promDown')}</p>
        ) : !disc.data?.configured ? (
          <p className="text-xs text-muted-foreground">{t('create.noProm')}</p>
        ) : fresh.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t('palette.allPlaced')}</p>
        ) : (
          <div className="max-h-[40svh] space-y-1.5 overflow-y-auto pr-0.5">
            {fresh.map((s) => (
              <Item key={s.name} item={{ kind: 'discovered', name: s.name }} disabled={disabled} onAdd={onAdd}>
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{s.name}</span>
                  <span className="block text-[11px] text-muted-foreground">
                    {s.requestsPerSecond > 0 ? t('palette.rps', { n: s.requestsPerSecond < 0.1 ? '<0.1' : s.requestsPerSecond.toFixed(1) }) : t('palette.noRequests')}
                    {s.inSystems?.length ? ` · ${t('palette.inSystems', { list: s.inSystems.join(', ') })}` : ''}
                  </span>
                </span>
              </Item>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}
