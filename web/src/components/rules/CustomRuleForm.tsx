// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// A rule the library does not cover: a simple builder or a raw PromQL
// expression, which the core validates with Prometheus' parser.

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'
import { DurationInput, Field, SeverityPicker } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { t, useT, type MsgKey } from '@/i18n'
import { validateSystem } from '@/lib/api'
import { useDebounced, useEditor } from '@/lib/editor'
import { BUILTIN, deriveRules } from '@/lib/model/rules'
import type { CustomAggregation, CustomFunction, CustomOperator, CustomRule, Description, Severity } from '@/lib/model/types'
import { writeDescriptionYaml } from '@/lib/model/yaml'
import RuleBacktest from './RuleBacktest'

const FUNCTIONS: { v: CustomFunction; label: MsgKey }[] = [
  { v: 'none', label: 'custom.fn.none' },
  { v: 'rate', label: 'custom.fn.rate' },
  { v: 'increase', label: 'custom.fn.increase' },
]
const AGGS: { v: CustomAggregation; label: MsgKey }[] = [
  { v: 'max', label: 'custom.agg.max' },
  { v: 'sum', label: 'custom.agg.sum' },
  { v: 'avg', label: 'custom.agg.avg' },
  { v: 'min', label: 'custom.agg.min' },
  { v: 'count', label: 'custom.agg.count' },
]
const OPS: { v: CustomOperator; label: MsgKey; sym: string }[] = [
  { v: 'gt', label: 'rule.op.gt', sym: '>' },
  { v: 'gte', label: 'rule.op.gte', sym: '>=' },
  { v: 'lt', label: 'rule.op.lt', sym: '<' },
  { v: 'lte', label: 'rule.op.lte', sym: '<=' },
  { v: 'eq', label: 'rule.op.eq', sym: '==' },
  { v: 'ne', label: 'rule.op.ne', sym: '!=' },
]

const METRIC_RE = /^[a-zA-Z_:][a-zA-Z0-9_:]*$/
const LABEL_RE = /^[a-zA-Z_][a-zA-Z0-9_]*$/

interface FormState {
  title: string
  mode: 'builder' | 'expr'
  metric: string
  labels: { k: string; v: string }[]
  fn: CustomFunction
  agg: CustomAggregation
  by: string
  op: CustomOperator
  threshold: string
  unit: string
  expr: string
  for: string
  severity: Severity | null
}

function fromRule(r?: CustomRule): FormState {
  const c = r?.condition
  return {
    title: r?.title ?? '',
    mode: r?.expr ? 'expr' : 'builder',
    metric: c?.metric ?? '',
    labels: Object.entries(c?.labels ?? {}).map(([k, v]) => ({ k, v })),
    fn: c?.function ?? 'none',
    agg: c?.aggregation ?? 'max',
    by: (c?.by ?? []).join(', '),
    op: c?.operator ?? 'gt',
    threshold: c ? String(c.threshold) : '',
    unit: r?.unit ?? '',
    expr: r?.expr ?? '',
    for: r?.for ?? BUILTIN.customFor,
    severity: r?.severity ?? null,
  }
}

function toRule(name: string, f: FormState): CustomRule {
  const r: CustomRule = { name, title: f.title.trim() }
  if (f.mode === 'expr') r.expr = f.expr.trim()
  else {
    const labels = Object.fromEntries(f.labels.filter((l) => l.k.trim()).map((l) => [l.k.trim(), l.v]))
    const by = f.by.split(',').map((s) => s.trim()).filter(Boolean)
    r.condition = { metric: f.metric.trim(), operator: f.op, threshold: Number(f.threshold) }
    if (Object.keys(labels).length) r.condition.labels = labels
    if (f.fn !== 'none') r.condition.function = f.fn
    if (f.agg !== 'max') r.condition.aggregation = f.agg
    if (by.length) r.condition.by = by
  }
  if (f.for !== BUILTIN.customFor) r.for = f.for
  if (f.severity) r.severity = f.severity
  if (f.unit.trim()) r.unit = f.unit.trim()
  return r
}

/** Mirrors CustomCondition.Expr in internal/rules/custom.go, for the preview line. */
function previewExpr(f: FormState, window: string): string {
  const labels = f.labels.filter((l) => l.k.trim()).sort((a, b) => a.k.localeCompare(b.k))
  let inner = labels.length ? `${f.metric}{${labels.map((l) => `${l.k.trim()}=${JSON.stringify(l.v)}`).join(', ')}}` : f.metric
  if (f.fn !== 'none') inner = `${f.fn}(${inner}[${window}])`
  const by = f.by.split(',').map((s) => s.trim()).filter(Boolean)
  return `${f.agg}${by.length ? ` by (${by.join(', ')})` : ''} (${inner}) ${OPS.find((o) => o.v === f.op)!.sym} ${f.threshold || '…'}`
}

function localErrors(f: FormState): Record<string, string> {
  const e: Record<string, string> = {}
  if (!f.title.trim()) e.title = t('custom.err.title')
  if (/\{\{|\}\}/.test(f.title)) e.title = t('custom.err.braces')
  if (f.mode === 'expr') {
    if (!f.expr.trim()) e.expr = t('custom.err.expr')
  } else {
    if (!METRIC_RE.test(f.metric.trim())) e.metric = t('custom.err.metric')
    if (f.threshold.trim() === '' || !Number.isFinite(Number(f.threshold))) e.threshold = t('custom.err.threshold')
    if (f.labels.some((l) => l.k.trim() && !LABEL_RE.test(l.k.trim()))) e.labels = t('custom.err.label')
  }
  return e
}

interface Props {
  service: string
  /** Rule being edited; a new rule otherwise. */
  initial?: CustomRule
  name: string
  onCancel: () => void
  onSave: (r: CustomRule) => void
}

export default function CustomRuleForm({ service, initial, name, onCancel, onSave }: Props) {
  const { desc, draft, systemId } = useEditor()
  const { t } = useT()
  const [f, setF] = useState<FormState>(() => fromRule(initial))
  const set = (p: Partial<FormState>) => setF((x) => ({ ...x, ...p }))
  const [tried, setTried] = useState(false)
  const allLocal = localErrors(f)
  // Required-field messages appear after the first attempt to save; wrong values right away.
  const local = Object.fromEntries(
    Object.entries(allLocal).filter(([k]) => tried || (k === 'title' ? f.title : k === 'metric' ? f.metric : k === 'threshold' ? f.threshold : k === 'expr' ? f.expr : 'x')),
  ) as Record<string, string>
  const rule = useMemo(() => toRule(name, f), [name, f])
  const window = desc.spec.defaults?.rateWindow ?? '5m'

  // The description with this rule in place, for server validation and the backtest.
  const { candidate, si, ci } = useMemo(() => {
    const d: Description = structuredClone(desc)
    const si = d.spec.services.findIndex((s) => s.name === service)
    const s = d.spec.services[si]
    const list = [...(s.customRules ?? [])]
    let ci = list.findIndex((c) => c.name === (initial?.name ?? name))
    if (ci < 0) {
      ci = list.length
      list.push(rule)
    } else list[ci] = rule
    s.customRules = list
    return { candidate: d, si, ci }
  }, [desc, service, initial, name, rule])

  const debounced = useDebounced(candidate, 500)
  const debouncedForm = useDebounced(f, 500)
  const debouncedOk = Object.keys(localErrors(debouncedForm)).length === 0 && debouncedForm === f
  const hasLocal = Object.keys(allLocal).length > 0
  const v = useQuery({
    queryKey: ['validate-custom', systemId, JSON.stringify(debounced.spec.services[si]?.customRules?.[ci])],
    queryFn: () => validateSystem(systemId, writeDescriptionYaml(draft.text, debounced)),
    enabled: debouncedOk && !draft.error,
    retry: false,
  })
  const vErrors = debouncedOk ? (v.data?.errors ?? []) : []
  const prefix = `/spec/services/${si}/customRules/${ci}`
  const serverErr = (field: string) =>
    vErrors
      .filter((e) => e.path === `${prefix}/${field}` || (field === 'condition' && e.path.startsWith(`${prefix}/condition`)) || (field === '' && e.path === prefix))
      .map((e) => e.message)
      .join('; ')
  const mine = vErrors.filter((e) => e.path.startsWith(prefix))
  const valid = !hasLocal && debouncedOk && v.isSuccess && mine.length === 0
  const derived = useMemo(() => deriveRules(candidate).find((r) => r.id === `${service}/custom/${name}`), [candidate, service, name])

  const svcObj = desc.spec.services.find((s) => s.name === service)
  return (
    <div className="space-y-4">
      <Field label={t('message.title')} hint={t('custom.titleHint')} error={local.title || serverErr('title')} htmlFor="cr-title">
        <Input id="cr-title" value={f.title} onChange={(e) => set({ title: e.target.value })} placeholder={t('custom.titlePlaceholder')} autoFocus />
      </Field>

      <div className="flex gap-0.5 rounded-lg bg-muted p-0.5 text-sm">
        {(['builder', 'expr'] as const).map((m) => (
          <button
            key={m}
            type="button"
            aria-pressed={f.mode === m}
            onClick={() => set(m === 'expr' && !f.expr && f.metric ? { mode: m, expr: previewExpr(f, window) } : { mode: m })}
            className={`flex-1 rounded-md px-2 py-1 ${f.mode === m ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground'}`}
          >
            {m === 'builder' ? t('custom.builder') : t('custom.expr')}
          </button>
        ))}
      </div>

      {f.mode === 'builder' ? (
        <div className="space-y-3">
          <Field label={t('custom.metric')} error={local.metric || serverErr('condition/metric')} htmlFor="cr-metric">
            <Input id="cr-metric" value={f.metric} onChange={(e) => set({ metric: e.target.value })} placeholder="http_requests_total" className="font-mono" />
          </Field>
          <Field label={t('custom.labels')} hint={t('custom.labelsHint')} error={local.labels || serverErr('condition/labels')}>
            <div className="space-y-1.5">
              {f.labels.map((l, i) => (
                <div key={i} className="flex items-center gap-1.5">
                  <Input value={l.k} placeholder={t('custom.label')} aria-label={t('custom.labelName')} className="font-mono" onChange={(e) => set({ labels: f.labels.map((x, j) => (j === i ? { ...x, k: e.target.value } : x)) })} />
                  <span className="text-muted-foreground">=</span>
                  <Input value={l.v} placeholder={t('custom.value')} aria-label={t('custom.labelValue')} className="font-mono" onChange={(e) => set({ labels: f.labels.map((x, j) => (j === i ? { ...x, v: e.target.value } : x)) })} />
                  <Button variant="ghost" size="icon-sm" aria-label={t('custom.removeLabel')} onClick={() => set({ labels: f.labels.filter((_, j) => j !== i) })}>
                    <X />
                  </Button>
                </div>
              ))}
              <Button
                variant="outline"
                size="sm"
                onClick={() => set({ labels: [...f.labels, { k: desc.spec.telemetry?.spans?.serviceLabel ?? 'service_name', v: svcObj?.telemetryName ?? service }] })}
              >
                <Plus /> {t('custom.addLabel')}
              </Button>
            </div>
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t('custom.fn')}>
              <Select value={f.fn} onValueChange={(v) => set({ fn: v as CustomFunction })}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {FUNCTIONS.map((x) => (
                    <SelectItem key={x.v} value={x.v}>
                      {t(x.label)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('custom.agg')}>
              <Select value={f.agg} onValueChange={(v) => set({ agg: v as CustomAggregation })}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {AGGS.map((x) => (
                    <SelectItem key={x.v} value={x.v}>
                      {t(x.label)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field label={t('custom.alertWhen')} error={local.threshold || serverErr('condition')}>
            <div className="flex flex-wrap items-center gap-1.5">
              <Select value={f.op} onValueChange={(v) => set({ op: v as CustomOperator })}>
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {OPS.map((x) => (
                    <SelectItem key={x.v} value={x.v}>
                      {t(x.label)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input value={f.threshold} onChange={(e) => set({ threshold: e.target.value })} inputMode="decimal" placeholder="100" aria-label={t('ruleCard.threshold')} className="w-28 tabular-nums" />
              <Input value={f.unit} onChange={(e) => set({ unit: e.target.value })} placeholder={t('custom.unitPlaceholder')} aria-label={t('custom.unit')} className="w-36" />
            </div>
          </Field>
          <details className="text-sm">
            <summary className="cursor-pointer text-muted-foreground">{t('custom.by')}</summary>
            <div className="mt-2">
              <Input value={f.by} onChange={(e) => set({ by: e.target.value })} placeholder="pod, namespace" aria-label={t('custom.byAria')} className="font-mono" />
              <p className="mt-1 text-xs text-muted-foreground">{t('custom.byHint')}</p>
            </div>
          </details>
          <p className="truncate rounded-md bg-muted px-2 py-1.5 font-mono text-xs text-muted-foreground" title={previewExpr(f, window)}>
            {previewExpr(f, window)}
          </p>
        </div>
      ) : (
        <Field label={t('custom.expr')} hint={t('custom.exprHint')} error={local.expr || serverErr('expr') || serverErr('')} htmlFor="cr-expr">
          <Textarea id="cr-expr" rows={4} value={f.expr} onChange={(e) => set({ expr: e.target.value })} className="field-sizing-fixed w-full min-w-0 font-mono text-xs break-all" placeholder="sum(rate(http_requests_total{code=~&quot;5..&quot;}[5m])) > 1" />
        </Field>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t('custom.for')}>
          <DurationInput value={f.for} onChange={(v) => set({ for: v })} />
        </Field>
        <Field label={t('common.severity')}>
          <SeverityPicker value={f.severity} inherited={svcObj?.critical ? 'critical' : 'warning'} onChange={(v) => set({ severity: v })} />
        </Field>
      </div>

      {valid && derived && <RuleBacktest rule={derived} desc={candidate} />}
      {v.isError && <p className="text-xs text-destructive">{t('custom.validateError', { error: v.error.message })}</p>}

      <div className="flex justify-end gap-2 pt-1">
        <Button variant="ghost" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button disabled={mine.length > 0} onClick={() => (hasLocal ? setTried(true) : onSave(rule))}>
          {initial ? t('custom.apply') : t('custom.add')}
        </Button>
      </div>
    </div>
  )
}
