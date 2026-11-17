// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useMemo, useState } from 'react'
import { DurationInput, Field, SeverityPicker } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useT } from '@/i18n'
import { useEditor } from '@/lib/editor'
import { addRule, BUILTIN, deriveRules, hasRequestRules, isNoDataKind, kindHint, kindLabel, nextCustomName, ruleId, SUGGESTED, thresholdSpec, type RuleKind } from '@/lib/model/rules'
import type { Description, Service, Severity } from '@/lib/model/types'
import CustomRuleForm from './CustomRuleForm'
import RuleBacktest from './RuleBacktest'

const KINDS: RuleKind[] = [
  'errors',
  'latency',
  'traffic',
  'memory',
  'cpu',
  'restarts',
  'queueLag',
  'slo',
  'errorsPerOp',
  'opErrors',
  'opLatency',
  'latencyPerOp',
  'latencyAboveNormal',
  'memoryLimit',
  'cpuLimit',
  'cpuAboveNormal',
  'oomKilled',
  'queueStopped',
  'noData',
  'noReadyPods',
  'custom',
]

function applicable(kind: RuleKind, s: Service): boolean {
  if (kind === 'queueLag' || kind === 'queueStopped') return s.type === 'queue' && !!s.queue
  // Queues and databases have no incoming spans of their own (set trafficDrop in YAML if yours do).
  if (kind === 'traffic') return s.type === 'http' || s.type === 'grpc'
  if (['errorsPerOp', 'opErrors', 'opLatency', 'slo', 'latencyPerOp', 'latencyAboveNormal'].includes(kind)) return hasRequestRules(s)
  // Rules against the limit and the normal apply only while no explicit threshold is set.
  if (kind === 'memoryLimit') return !s.saturation?.memory
  if (kind === 'cpuLimit' || kind === 'cpuAboveNormal') return !s.saturation?.cpuCores
  if (kind === 'systemNoData') return false
  return true
}

function defaults(desc: Description, service: string, kind: RuleKind, operation: string) {
  const existing = deriveRules(desc).find((r) => r.id === ruleId(service, kind, kind === 'opErrors' || kind === 'opLatency' ? operation : undefined))
  if (existing) return { threshold: existing.threshold, for: existing.for, exists: true }
  const errorsRule = deriveRules(desc).find((r) => r.id === `${service}/errors`)
  const latencyRule = deriveRules(desc).find((r) => r.id === `${service}/latency`)
  const t: Record<RuleKind, [number, string]> = {
    errors: [BUILTIN.errorThreshold * 100, BUILTIN.errorFor],
    errorsPerOp: [errorsRule?.threshold ?? 5, errorsRule?.for ?? BUILTIN.errorFor],
    opErrors: [errorsRule?.threshold ?? 5, errorsRule?.for ?? BUILTIN.errorFor],
    latency: [BUILTIN.latencyMs, BUILTIN.latencyFor],
    opLatency: [latencyRule?.threshold ?? BUILTIN.latencyMs, latencyRule?.for ?? BUILTIN.latencyFor],
    memory: [512, BUILTIN.saturationFor],
    cpu: [SUGGESTED.cpuCores, BUILTIN.saturationFor],
    restarts: [BUILTIN.maxRestarts, ''],
    queueLag: [SUGGESTED.queueMaxLag, BUILTIN.queueFor],
    slo: [SUGGESTED.sloObjective, ''],
    traffic: [BUILTIN.trafficThreshold * 100, BUILTIN.trafficFor],
    custom: [NaN, BUILTIN.customFor],
    latencyPerOp: [latencyRule?.threshold ?? BUILTIN.latencyMs, ''],
    latencyAboveNormal: [BUILTIN.latencyFactor, ''],
    memoryLimit: [BUILTIN.memoryLimitShare * 100, BUILTIN.saturationFor],
    cpuLimit: [BUILTIN.cpuLimitShare * 100, BUILTIN.saturationFor],
    cpuAboveNormal: [BUILTIN.cpuFactor, BUILTIN.saturationFor],
    oomKilled: [NaN, ''],
    queueStopped: [NaN, ''],
    noData: [NaN, BUILTIN.noDataAfter],
    noReadyPods: [NaN, BUILTIN.noReadyPodsFor],
    systemNoData: [NaN, BUILTIN.noDataAfter],
  }
  return { threshold: t[kind][0], for: t[kind][1], exists: false }
}

interface Props {
  open: boolean
  onOpenChange: (v: boolean) => void
  /** Fixed service (service panel); otherwise the person picks. */
  service?: string
  initialKind?: RuleKind
}

export default function AddRuleDialog({ open, onOpenChange, service, initialKind }: Props) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl [&>*]:min-w-0">
        {open && <Body service={service} initialKind={initialKind} onDone={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function Body({ service, initialKind, onDone }: { service?: string; initialKind?: RuleKind; onDone: () => void }) {
  const { desc, edit } = useEditor()
  const { t, tn } = useT()
  const [services, setServices] = useState<string[]>(service ? [service] : [])
  const [kind, setKind] = useState<RuleKind>(initialKind ?? 'errors')
  const [operation, setOperation] = useState('')
  const first = services[0]
  const firstSvc = desc.spec.services.find((s) => s.name === first)
  const init = defaults(desc, first ?? '', kind, operation)
  const [values, setValues] = useState<{ key: string; threshold: string; for: string; severity: Severity | null }>({ key: '', threshold: '', for: '', severity: null })
  // Reset proposed values when the service or kind changes.
  const key = `${first}/${kind}`
  const v = values.key === key ? values : { key, threshold: Number.isFinite(init.threshold) ? String(init.threshold) : '', for: init.for, severity: null }
  const setV = (p: Partial<typeof v>) => setValues({ ...v, ...p })
  const spec = thresholdSpec(kind)
  const needsOp = kind === 'opErrors' || kind === 'opLatency'
  const threshold = Number(v.threshold.replace(',', '.'))
  const thresholdOk = !spec || (Number.isFinite(threshold) && threshold >= spec.min && (spec.max === undefined || threshold <= spec.max))
  const allowed = services.filter((n) => {
    const s = desc.spec.services.find((x) => x.name === n)
    return s && applicable(kind, s)
  })

  const apply = (d: Description, svc: string) =>
    addRule(d, svc, {
      kind,
      operation: needsOp ? operation.trim() : undefined,
      threshold: spec ? threshold : undefined,
      for: v.for || undefined,
      severity: v.severity ?? undefined,
    })

  const candidate = useMemo(() => (first && thresholdOk && (!needsOp || operation.trim()) && kind !== 'custom' ? apply(desc, first) : null), [desc, first, kind, v, operation, thresholdOk]) // eslint-disable-line react-hooks/exhaustive-deps -- apply only closes over the listed values
  const preview = candidate ? deriveRules(candidate).find((r) => r.id === ruleId(first, kind, needsOp ? operation.trim() : undefined)) : undefined

  return (
    <>
      <DialogHeader>
        <DialogTitle>{service ? `${t('addRule.title')} · ${service}` : t('addRule.title')}</DialogTitle>
        <DialogDescription>{t('addRule.description')}</DialogDescription>
      </DialogHeader>
      <div className="space-y-4">
        {!service && (
          <Field label={kind === 'custom' ? t('rules.col.service') : t('addRule.services')}>
            <div className="max-h-40 space-y-1 overflow-y-auto rounded-md border p-2">
              {desc.spec.services.map((s) => (
                <label key={s.name} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={services.includes(s.name)}
                    onCheckedChange={(on) =>
                      setServices((cur) => (kind === 'custom' ? (on ? [s.name] : []) : on ? [...cur, s.name] : cur.filter((x) => x !== s.name)))
                    }
                  />
                  <span className={applicable(kind, s) ? '' : 'text-muted-foreground'}>{s.name}</span>
                </label>
              ))}
            </div>
          </Field>
        )}
        <Field label={t('addRule.kind')} hint={kindHint(kind)}>
          <Select
            value={kind}
            onValueChange={(k) => {
              setKind(k as RuleKind)
              if (k === 'custom') setServices((cur) => cur.slice(0, 1))
            }}
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {KINDS.filter((k) => !firstSvc || applicable(k, firstSvc)).map((k) => (
                <SelectItem key={k} value={k}>
                  {kindLabel(k)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        {kind === 'custom' ? (
          first ? (
            <CustomRuleForm
              service={first}
              name={nextCustomName(firstSvc!)}
              onCancel={onDone}
              onSave={(r) => {
                edit((d) => addRule(d, first, { kind: 'custom', custom: r }))
                onDone()
              }}
            />
          ) : (
            <p className="text-sm text-muted-foreground">{t('addRule.pickService')}</p>
          )
        ) : (
          <>
            {needsOp && (
              <Field label={t('addRule.operation')} hint={t('addRule.operationHint')} htmlFor="op-name">
                <Input id="op-name" value={operation} onChange={(e) => setOperation(e.target.value)} className="font-mono" />
              </Field>
            )}
            <div className="flex flex-wrap items-end gap-x-4 gap-y-3">
              {spec && (
                <Field label={spec.label} htmlFor="new-thr" error={!thresholdOk ? t('addRule.checkNumber') : undefined}>
                  <div className="flex items-center gap-1.5">
                    <Input id="new-thr" type="number" step={spec.step} value={v.threshold} onChange={(e) => setV({ threshold: e.target.value })} className="h-8 w-24 tabular-nums" />
                    <span className="text-sm text-muted-foreground">{spec.unit}</span>
                  </div>
                </Field>
              )}
              {v.for !== '' && (
                <Field label={isNoDataKind(kind) ? t('ruleCard.after') : t('ruleCard.for')}>
                  <DurationInput value={v.for} onChange={(x) => setV({ for: x })} />
                </Field>
              )}
              {kind !== 'slo' && (
                <Field label={t('common.severity')} className="min-w-44">
                  <SeverityPicker value={v.severity} inherited={firstSvc?.critical ? 'critical' : 'warning'} onChange={(x) => setV({ severity: x })} />
                </Field>
              )}
            </div>
            {init.exists && <p className="text-xs text-muted-foreground">{t('addRule.exists', { service: first })}</p>}
            {services.length > allowed.length && <p className="text-xs text-muted-foreground">{t('addRule.notApplicable', { list: services.filter((s) => !allowed.includes(s)).join(', ') })}</p>}
            {preview && (
              <div className="space-y-2">
                <p className="text-sm">
                  <span className="text-muted-foreground">{services.length > 1 ? t('addRule.previewFor', { service: first }) : t('addRule.preview')} </span>
                  {preview.sentence}
                </p>
                <RuleBacktest rule={preview} desc={candidate!} />
              </div>
            )}
            <div className="flex justify-end gap-2 pt-1">
              <Button variant="ghost" onClick={onDone}>
                {t('common.cancel')}
              </Button>
              <Button
                disabled={allowed.length === 0 || !thresholdOk || (needsOp && !operation.trim())}
                onClick={() => {
                  edit((d) => allowed.reduce((acc, s) => apply(acc, s), d))
                  onDone()
                }}
              >
                {allowed.length > 1 ? tn('addRule.addFor', allowed.length) : t('common.add')}
              </Button>
            </div>
          </>
        )}
      </div>
    </>
  )
}
