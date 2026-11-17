// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// One rule as a card. The same editor is used in the canvas service panel and
// in the rules list.

import { useState } from 'react'
import { ChevronDown, Pencil, Trash2 } from 'lucide-react'
import { cn } from 'cn'
import { DurationInput, Field, SeverityBadge, SeverityPicker, StateDot } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { getLang, t, useT } from '@/i18n'
import { useEditor } from '@/lib/editor'
import { BUILTIN, isNoDataKind, kindHint, kindLabel, removeRule, ruleState, setRule, stateLabel, thresholdSpec, type RuleItem } from '@/lib/model/rules'
import { severityLabel, type CustomRule } from '@/lib/model/types'
import CustomRuleForm from './CustomRuleForm'
import MessageEditor from './MessageEditor'
import RuleBacktest from './RuleBacktest'

/** 0.02 calls/s -> "1.2 requests per minute" (in Russian the genitive after "fewer than"). */
function perMinute(rps: number): string {
  const n = Math.round(rps * 60 * 10) / 10
  if (getLang() === 'ru') {
    const singular = !Number.isInteger(n) || (n % 10 === 1 && n % 100 !== 11)
    return t(singular ? 'ruleCard.perMinute.one' : 'ruleCard.perMinute.other', { n: String(n).replace('.', ',') })
  }
  return t(n === 1 ? 'ruleCard.perMinute.one' : 'ruleCard.perMinute.other', { n })
}

function ThresholdInput({ rule, disabled }: { rule: RuleItem; disabled: boolean }) {
  const { edit } = useEditor()
  const { t } = useT()
  const spec = thresholdSpec(rule.kind) ?? (rule.custom?.condition ? { unit: rule.custom.unit ?? '', step: 1, min: undefined, max: undefined, label: t('ruleCard.threshold') } : null)
  const [text, setText] = useState(String(rule.threshold))
  const [prev, setPrev] = useState(rule.threshold)
  // Rules without a threshold carry NaN, and NaN !== NaN: compare as React does.
  if (!Object.is(prev, rule.threshold)) {
    setPrev(rule.threshold)
    setText(String(rule.threshold))
  }
  if (!spec) return null
  const commit = (raw: string) => {
    const n = Number(raw.replace(',', '.'))
    if (!Number.isFinite(n) || (spec.min !== undefined && n < spec.min) || (spec.max !== undefined && n > spec.max)) {
      setText(String(rule.threshold))
      return
    }
    if (n !== rule.threshold) edit((d) => setRule(d, rule.id, { threshold: n }))
  }
  return (
    <Field label={spec.label} htmlFor={`thr-${rule.id}`}>
      <div className="flex items-center gap-1.5">
        <Input
          id={`thr-${rule.id}`}
          type="number"
          inputMode="decimal"
          step={spec.step}
          min={spec.min}
          max={spec.max}
          value={text}
          disabled={disabled}
          onChange={(e) => setText(e.target.value)}
          onBlur={(e) => commit(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && commit((e.target as HTMLInputElement).value)}
          className="h-8 w-24 tabular-nums"
        />
        <span className="text-sm text-muted-foreground">{spec.unit}</span>
      </div>
    </Field>
  )
}

export default function RuleCard({ rule }: { rule: RuleItem }) {
  const { edit, readOnly, states } = useEditor()
  const [open, setOpen] = useState(false)
  const { t } = useT()
  const st = ruleState(rule, states)

  return (
    <div className={cn('rounded-lg border bg-card transition-colors', open && 'ring-1 ring-ring/30', !rule.enabled && 'bg-muted/30')}>
      <div className="flex items-start gap-2.5 p-2.5">
        <Switch
          className="mt-0.5"
          checked={rule.enabled}
          disabled={readOnly}
          aria-label={rule.enabled ? t('ruleCard.turnOff') : t('ruleCard.turnOn')}
          onCheckedChange={(on) => edit((d) => setRule(d, rule.id, { enabled: on }))}
        />
        <button type="button" className="min-w-0 flex-1 text-left" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
            <span>{kindLabel(rule.kind)}</span>
            {rule.enabled && <SeverityBadge severity={rule.severity} />}
            {rule.enabled && st !== 'unknown' && (
              <span className="flex items-center gap-1" title={stateLabel(st)}>
                <StateDot state={st} />
                <span className={cn(st === 'firing' && 'font-medium text-firing', (st === 'pending' || st === 'warning') && 'text-warn', st === 'warning' && 'font-medium')}>{stateLabel(st)}</span>
              </span>
            )}
          </div>
          <div className={cn('mt-0.5 text-sm leading-snug', !rule.enabled && 'text-muted-foreground line-through decoration-muted-foreground/40')}>{rule.sentence}</div>
        </button>
        <div className="flex shrink-0 items-center gap-2 pt-0.5">
          <button type="button" onClick={() => setOpen((o) => !o)} aria-label={open ? t('orient.collapse') : t('rules.configure')} className="text-muted-foreground hover:text-foreground">
            <ChevronDown className={cn('size-4 transition-transform', open && 'rotate-180')} />
          </button>
        </div>
      </div>
      {open && <RuleEditor rule={rule} className="border-t px-2.5 pt-2.5 pb-3" />}
    </div>
  )
}

export function RuleEditor({ rule, className }: { rule: RuleItem; className?: string }) {
  const { edit, readOnly, desc } = useEditor()
  const [editing, setEditing] = useState(false)
  const { t } = useT()
  const svc = desc.spec.services.find((s) => s.name === rule.service)
  const inheritedSev = rule.severityDefault ?? (svc?.critical ? 'critical' : 'warning')
  return (
    <>
      <div className={cn('space-y-3', className)}>
        <p className="text-xs text-muted-foreground">{kindHint(rule.kind)}</p>
        {rule.note && <p className="text-xs text-muted-foreground">{rule.note}</p>}
        <div className="flex flex-wrap items-end gap-x-4 gap-y-3">
          <ThresholdInput rule={rule} disabled={readOnly} />
          {rule.for !== '' && (
            <Field label={isNoDataKind(rule.kind) ? t('ruleCard.after') : t('ruleCard.for')}>
              <DurationInput value={rule.for} onChange={(v) => edit((d) => setRule(d, rule.id, { for: v }))} disabled={readOnly} />
            </Field>
          )}
          {!rule.severityFixed && (
            <Field label={t('common.severity')} className="min-w-44">
              <SeverityPicker
                value={rule.severityInherited ? null : rule.severity}
                inherited={inheritedSev}
                disabled={readOnly}
                onChange={(v) => edit((d) => setRule(d, rule.id, { severity: v }))}
              />
            </Field>
          )}
        </div>
        {rule.kind === 'slo' && <p className="text-xs text-muted-foreground">{t('ruleCard.slo', { fast: severityLabel(inheritedSev), slow: severityLabel('warning') })}</p>}
        {rule.kind === 'traffic' && (
          <p className="text-xs text-muted-foreground">
            {t('ruleCard.traffic', {
              rate: perMinute(svc?.trafficDrop?.minRequestsPerSecond ?? desc.spec.defaults?.trafficDrop?.minRequestsPerSecond ?? BUILTIN.trafficMinRPS),
            })}
          </p>
        )}
        {rule.custom && (
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1" title={rule.custom.expr ?? ''}>
              {rule.custom.expr ?? `${rule.custom.condition?.metric}`}
            </code>
            <Button variant="outline" size="sm" disabled={readOnly} onClick={() => setEditing(true)}>
              <Pencil /> {t('ruleCard.editCondition')}
            </Button>
          </div>
        )}
        <RuleBacktest rule={rule} />
        <MessageEditor rule={rule} />
        {rule.removable && (
          <div className="flex justify-end">
            <Button variant="ghost" size="sm" className="text-muted-foreground hover:text-destructive" disabled={readOnly} onClick={() => edit((d) => removeRule(d, rule.id))}>
              <Trash2 /> {t('ruleCard.delete')}
            </Button>
          </div>
        )}
      </div>
      {rule.custom && (
        <Dialog open={editing} onOpenChange={setEditing}>
          <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl [&>*]:min-w-0">
            <DialogHeader>
              <DialogTitle>
                {t('rule.kind.custom')} · {rule.service}
              </DialogTitle>
              <DialogDescription>{t('rule.hint.custom')}</DialogDescription>
            </DialogHeader>
            <CustomRuleForm
              service={rule.service}
              name={rule.custom.name}
              initial={rule.custom}
              onCancel={() => setEditing(false)}
              onSave={(r: CustomRule) => {
                edit((d) => {
                  const next = structuredClone(d)
                  const s = next.spec.services.find((x) => x.name === rule.service)
                  if (s?.customRules) s.customRules = s.customRules.map((c) => (c.name === r.name ? r : c))
                  return next
                })
                setEditing(false)
              }}
            />
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}
