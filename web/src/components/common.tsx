// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import type { ReactNode } from 'react'
import { cn } from 'cn'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useT } from '@/i18n'
import type { RuleState } from '@/lib/model/rules'
import { severityLabel, type Severity } from '@/lib/model/types'

export function Page({ title, description, actions, children, className }: { title: ReactNode; description?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cn('mx-auto w-full max-w-6xl space-y-5 p-4 md:p-6', className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h1 className="text-xl font-semibold tracking-tight md:text-2xl">{title}</h1>
          {description && <p className="text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  )
}

const dotClass: Record<RuleState, string> = {
  ok: 'bg-ok',
  pending: 'bg-warn/60',
  warning: 'bg-warn',
  firing: 'bg-firing animate-pulse',
  unknown: 'bg-muted-foreground/40',
}

export function StateDot({ state, className }: { state: RuleState; className?: string }) {
  return <span aria-hidden className={cn('inline-block size-2 shrink-0 rounded-full', dotClass[state], className)} />
}

export type SystemState = 'ok' | 'warning' | 'firing' | 'invalid' | 'unknown'

const pill: Record<SystemState, { cls: string; dot: RuleState }> = {
  ok: { cls: 'text-ok bg-ok/10', dot: 'ok' },
  warning: { cls: 'text-warn bg-warn/10', dot: 'pending' },
  firing: { cls: 'text-firing bg-firing/10', dot: 'firing' },
  invalid: { cls: 'text-firing bg-firing/10', dot: 'firing' },
  unknown: { cls: 'text-muted-foreground bg-muted', dot: 'unknown' },
}

export function StatePill({ state, children }: { state: SystemState; children: ReactNode }) {
  return (
    <span className={cn('inline-flex h-6 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-xs font-medium whitespace-nowrap', pill[state].cls)}>
      <StateDot state={pill[state].dot} />
      {children}
    </span>
  )
}

export function SeverityBadge({ severity, className }: { severity: Severity; className?: string }) {
  useT()
  return (
    <span
      className={cn(
        'inline-flex h-5 shrink-0 items-center rounded px-1.5 text-[11px] font-medium whitespace-nowrap',
        severity === 'critical' ? 'bg-firing/10 text-firing' : 'bg-warn/15 text-warn',
        className,
      )}
    >
      {severityLabel(severity)}
    </span>
  )
}

const UNITS = [
  { v: 's', label: 'unit.sec' },
  { v: 'm', label: 'unit.min' },
  { v: 'h', label: 'unit.hour' },
] as const

/** Splits "5m" into 5 and "m"; compound durations fall back to minutes. */
function splitDuration(d: string): { n: number; u: 's' | 'm' | 'h' } {
  const m = /^(\d+)(s|m|h)$/.exec(d)
  if (m) return { n: Number(m[1]), u: m[2] as 's' | 'm' | 'h' }
  let secs = 0
  for (const p of d.matchAll(/(\d+)(ms|s|m|h|d)/g)) secs += Number(p[1]) * ({ ms: 0.001, s: 1, m: 60, h: 3600, d: 86400 } as Record<string, number>)[p[2]]
  if (secs % 3600 === 0 && secs >= 3600) return { n: secs / 3600, u: 'h' }
  if (secs % 60 === 0) return { n: secs / 60, u: 'm' }
  return { n: Math.round(secs), u: 's' }
}

/** Duration as a number and a unit (sec / min / h), written as 30s / 5m / 1h. */
export function DurationInput({ value, onChange, id, disabled }: { value: string; onChange: (v: string) => void; id?: string; disabled?: boolean }) {
  const { t } = useT()
  const { n, u } = splitDuration(value || '0m')
  return (
    <div className="flex items-center gap-1">
      <Input
        id={id}
        aria-label={id ? undefined : t('common.duration')}
        type="number"
        min={0}
        step={1}
        value={Number.isFinite(n) ? n : ''}
        disabled={disabled}
        onChange={(e) => {
          const v = Math.max(0, Math.round(Number(e.target.value)))
          if (Number.isFinite(v)) onChange(`${v}${u}`)
        }}
        className="h-8 w-16 tabular-nums"
      />
      <Select value={u} onValueChange={(nu) => onChange(`${n}${nu}`)} disabled={disabled}>
        <SelectTrigger size="sm" className="w-[72px]" aria-label={t('common.timeUnit')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {UNITS.map((x) => (
            <SelectItem key={x.v} value={x.v}>
              {t(x.label)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

/** Severity with "as the service" default. value null = inherited. */
export function SeverityPicker({
  value,
  inherited,
  onChange,
  disabled,
}: {
  value: Severity | null
  inherited: Severity
  onChange: (v: Severity | null) => void
  disabled?: boolean
}) {
  const { t } = useT()
  return (
    <Select value={value ?? 'inherit'} onValueChange={(v) => onChange(v === 'inherit' ? null : (v as Severity))} disabled={disabled}>
      <SelectTrigger size="sm" className="w-full min-w-44" aria-label={t('common.severity')}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="inherit">{t('common.severityInherit', { s: severityLabel(inherited) })}</SelectItem>
        <SelectItem value="critical">{t('common.critical')}</SelectItem>
        <SelectItem value="warning">{t('common.warning')}</SelectItem>
      </SelectContent>
    </Select>
  )
}

export function Field({ label, hint, error, htmlFor, children, className }: { label: ReactNode; hint?: ReactNode; error?: ReactNode; htmlFor?: string; children: ReactNode; className?: string }) {
  return (
    <div className={cn('space-y-1.5', className)}>
      <label htmlFor={htmlFor} className="text-sm font-medium">
        {label}
      </label>
      {children}
      {error ? <p className="text-xs text-destructive">{error}</p> : hint ? <p className="text-xs text-muted-foreground">{hint}</p> : null}
    </div>
  )
}
