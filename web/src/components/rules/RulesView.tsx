// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Fragment, useMemo, useState } from 'react'
import { ChevronDown, Plus } from 'lucide-react'
import { cn } from 'cn'
import { SeverityBadge, StateDot } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { destinationName, useEditor } from '@/lib/editor'
import { useT } from '@/i18n'
import { deriveRules, generatedCount, kindLabel, ruleState, setRule, stateLabel, type RuleKind, type RuleState } from '@/lib/model/rules'
import { receiversFor } from '@/lib/model/routing'
import AddRuleDialog from './AddRuleDialog'
import { RuleEditor } from './RuleCard'

type StateFilter = 'all' | RuleState | 'off'

export default function RulesView() {
  const { desc, edit, readOnly, states, destinations, telegram, email } = useEditor()
  const [service, setService] = useState('all')
  const [kind, setKind] = useState<'all' | RuleKind>('all')
  const [state, setState] = useState<StateFilter>('all')
  const [open, setOpen] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const { t, tn, lang } = useT()
  // eslint-disable-next-line react-hooks/exhaustive-deps -- sentences use the interface language, so recompute on a switch
  const all = useMemo(() => deriveRules(desc), [desc, lang])
  const kinds = useMemo(() => [...new Set(all.map((r) => r.kind))], [all])
  const rows = all.filter((r) => {
    if (service !== 'all' && (r.system || r.service !== service)) return false
    if (kind !== 'all' && r.kind !== kind) return false
    if (state === 'off') return !r.enabled
    if (state === 'firing') return r.enabled && ['firing', 'warning'].includes(ruleState(r, states))
    if (state !== 'all') return r.enabled && ruleState(r, states) === state
    return true
  })
  const firing = all.filter((r) => ['firing', 'warning'].includes(ruleState(r, states))).length

  return (
    <div className="mx-auto w-full max-w-6xl space-y-4 p-4 md:p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">{t('system.view.rules')}</h2>
          <p className="text-sm text-muted-foreground">
            {tn('rules.enabledOf', generatedCount(all), { total: all.reduce((n, r) => n + r.alerts.length, 0) })}
            {states ? (firing ? ` · ${t('rules.firing', { n: firing })}` : ` · ${t('rules.nothingFiring')}`) : ` · ${t('rules.stateUnknown')}`}
          </p>
        </div>
        <Button onClick={() => setCreating(true)} disabled={readOnly}>
          <Plus /> {t('rules.create')}
        </Button>
      </div>

      <div className="flex flex-wrap gap-2">
        <Select value={service} onValueChange={setService}>
          <SelectTrigger size="sm" className="min-w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('routing.allServices')}</SelectItem>
            {desc.spec.services.map((s) => (
              <SelectItem key={s.name} value={s.name}>
                {s.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={kind} onValueChange={(v) => setKind(v as typeof kind)}>
          <SelectTrigger size="sm" className="min-w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('rules.allKinds')}</SelectItem>
            {kinds.map((k) => (
              <SelectItem key={k} value={k}>
                {kindLabel(k)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={state} onValueChange={(v) => setState(v as StateFilter)}>
          <SelectTrigger size="sm" className="min-w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('rules.anyState')}</SelectItem>
            <SelectItem value="firing">{t('rules.filter.firing')}</SelectItem>
            <SelectItem value="pending">{t('rules.filter.pending')}</SelectItem>
            <SelectItem value="ok">{t('rules.filter.ok')}</SelectItem>
            <SelectItem value="off">{t('rules.filter.off')}</SelectItem>
          </SelectContent>
        </Select>
        {(service !== 'all' || kind !== 'all' || state !== 'all') && (
          <Button variant="ghost" size="sm" onClick={() => (setService('all'), setKind('all'), setState('all'))}>
            {t('rules.reset')}
          </Button>
        )}
      </div>

      <div className="overflow-hidden rounded-lg border">
        <div className="hidden grid-cols-[40px_140px_minmax(0,1fr)_120px_110px_minmax(0,200px)_28px] items-center gap-3 border-b bg-muted/40 px-3 py-2 text-xs font-medium text-muted-foreground lg:grid">
          <span />
          <span>{t('rules.col.service')}</span>
          <span>{t('rules.col.check')}</span>
          <span>{t('common.severity')}</span>
          <span>{t('rules.col.state')}</span>
          <span>{t('rules.col.to')}</span>
          <span />
        </div>
        {rows.length === 0 && <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t('rules.noneMatch')}</p>}
        {rows.map((r) => {
          const st = ruleState(r, states)
          const to = receiversFor(destinations, r.service, r.severity).map((d) => destinationName(d, telegram, email))
          const isOpen = open === r.id
          return (
            <Fragment key={r.id}>
              <div
                className={cn(
                  'grid cursor-pointer grid-cols-[40px_minmax(0,1fr)_28px] items-start gap-x-3 gap-y-1 border-b px-3 py-2.5 last:border-b-0 hover:bg-muted/30 lg:grid-cols-[40px_140px_minmax(0,1fr)_120px_110px_minmax(0,200px)_28px] lg:items-center',
                  isOpen && 'bg-muted/30',
                  !r.enabled && 'text-muted-foreground',
                )}
                onClick={() => setOpen(isOpen ? null : r.id)}
              >
                <span onClick={(e) => e.stopPropagation()}>
                  <Switch checked={r.enabled} disabled={readOnly} aria-label={t('rules.enabled')} onCheckedChange={(on) => edit((d) => setRule(d, r.id, { enabled: on }))} />
                </span>
                <span className="min-w-0 lg:contents">
                  {r.system ? (
                    <span className="block truncate text-sm font-medium lg:text-foreground" data-testid="rule-system">
                      {desc.metadata.name} <span className="font-normal text-muted-foreground">· {t('rules.wholeSystem')}</span>
                    </span>
                  ) : (
                    <span className="block truncate text-sm font-medium lg:text-foreground">{r.service}</span>
                  )}
                  <span className="block min-w-0">
                    <span className="block text-xs text-muted-foreground">{kindLabel(r.kind)}</span>
                    <span className={cn('block text-sm', !r.enabled && 'line-through decoration-muted-foreground/40')}>{r.sentence}</span>
                  </span>
                  <span className="mt-1 flex flex-wrap items-center gap-2 lg:mt-0 lg:contents">
                    <span>{r.enabled ? <SeverityBadge severity={r.severity} /> : <span className="text-xs">{t('rules.off')}</span>}</span>
                    <span className="flex items-center gap-1.5 text-xs">
                      {r.enabled && (
                        <>
                          <StateDot state={st} />
                          <span className={cn(st === 'firing' && 'font-medium text-firing', (st === 'pending' || st === 'warning') && 'text-warn', st === 'warning' && 'font-medium')}>{stateLabel(st)}</span>
                        </>
                      )}
                    </span>
                    <span className="truncate text-xs text-muted-foreground" title={to.join(', ')}>
                      {r.enabled ? to.join(', ') || <span className="text-warn">{t('service.nowhere')}</span> : ''}
                    </span>
                  </span>
                </span>
                {/* The whole row toggles with the mouse; this button makes it reachable from the keyboard. */}
                <button
                  type="button"
                  aria-expanded={isOpen}
                  aria-label={isOpen ? t('orient.collapse') : t('rules.configure')}
                  className="mt-1 rounded text-muted-foreground outline-none focus-visible:ring-3 focus-visible:ring-ring/50 lg:mt-0"
                  onClick={(e) => {
                    e.stopPropagation()
                    setOpen(isOpen ? null : r.id)
                  }}
                >
                  <ChevronDown className={cn('size-4 transition-transform', isOpen && 'rotate-180')} />
                </button>
              </div>
              {isOpen && (
                <div className="border-b bg-muted/10 px-3 py-3 lg:pl-[64px]">
                  <RuleEditor rule={r} />
                </div>
              )}
            </Fragment>
          )
        })}
      </div>
      <AddRuleDialog open={creating} onOpenChange={setCreating} />
    </div>
  )
}
