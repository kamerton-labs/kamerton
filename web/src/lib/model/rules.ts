// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Rules of a system as editable cards. Merging of defaults, service and
// operation policies mirrors internal/generator; the built-in values below
// must match internal/generator/generator.go.

import { getLang, t as tr, tl, tn, type Lang } from '@/i18n'
import { clone, type CustomRule, type Description, type ErrorRatePolicy, type LatencyPolicy, type NoDataPolicy, type NoReadyPodsPolicy, type Service, type Severity, type TrafficDropPolicy } from './types'

export const BUILTIN = {
  errorThreshold: 0.05,
  minRPS: 0.05,
  errorFor: '1m',
  latencyQuantile: 0.95,
  latencyMs: 1000,
  latencyFor: '2m',
  saturationFor: '2m',
  maxRestarts: 3,
  queueFor: '2m',
  customFor: '2m',
  sloWindow: '30d',
  trafficThreshold: 0.1,
  trafficMinRPS: 0.02,
  trafficFor: '2m',
  latencyFactor: 5,
  /** Fixed in the library: internal/rules/normal.go. */
  latencyAboveNormalMinMs: 100,
  cpuAboveNormalMinCores: 0.5,
  memoryLimitShare: 0.95,
  cpuLimitShare: 0.9,
  cpuFactor: 3,
  /** Own pending periods; a shorter latency "for" wins (internal/generator). */
  opLatencyFor: '30s',
  aboveNormalFor: '1m',
  /** Queue stopped (internal/rules/normal.go): no members for 1m or offset still for 3m. */
  queueStoppedFor: '1m',
  queueNoMembers: '1m',
  queueStalled: '3m',
  noDataAfter: '3m',
  /** Fixed pending period of both no-data rules (internal/rules/nodata.go NoDataPending). */
  noDataFor: '1m',
  noReadyPodsFor: '30s',
  /** With no pod left the alert lasts this long after the last ready one (internal/rules/pods.go). */
  podsGoneRange: '1h',
} as const

/** Service part of the id of rules about the whole system: generator.SystemRunbook, never a service name. */
export const SYSTEM_RULES = '_system'

/** A metric set to "-" turns its rules off (internal/description DisabledMetric). */
const DISABLED = '-'

/** Values proposed when a rule is added. */
export const SUGGESTED = { memory: '512Mi', cpuCores: 1, sloObjective: 99.5, queueMaxLag: 1000 } as const

export type RuleKind =
  | 'errors'
  | 'errorsPerOp'
  | 'opErrors'
  | 'latency'
  | 'latencyPerOp'
  | 'latencyAboveNormal'
  | 'opLatency'
  | 'memory'
  | 'memoryLimit'
  | 'cpu'
  | 'cpuLimit'
  | 'cpuAboveNormal'
  | 'restarts'
  | 'oomKilled'
  | 'queueLag'
  | 'queueStopped'
  | 'slo'
  | 'traffic'
  | 'noData'
  | 'noReadyPods'
  | 'systemNoData'
  | 'custom'

export const kindLabel = (kind: RuleKind): string => tr(`rule.kind.${kind}` as const)

export const kindHint = (kind: RuleKind): string => tr(`rule.hint.${kind}` as const)

/** The duration of the no-data rules is how long data may be missing, not how long a condition holds. */
export const isNoDataKind = (kind: RuleKind) => kind === 'noData' || kind === 'systemNoData'

export interface RuleItem {
  /** service/kind[/operation|name]; rules about the whole system use SYSTEM_RULES as the service part. */
  id: string
  /** '' for a rule about the whole system: its alert has no service label. */
  service: string
  system?: boolean
  kind: RuleKind
  operation?: string
  customName?: string
  enabled: boolean
  /** Threshold in display units (see thresholdSpec). NaN when not applicable. */
  threshold: number
  /** Duration the condition must hold; '' when fixed by the library. */
  for: string
  severity: Severity
  /** True when the severity follows the service (not set on the rule). */
  severityInherited: boolean
  /** Severity cannot be changed (SLO burn-rate pair). */
  severityFixed?: boolean
  /** What "as the service" means when it is not the service's own severity. */
  severityDefault?: Severity
  sentence: string
  /** An extra line for the editor. */
  note?: string
  /** Alert name used for state and backtest, with the operation label. */
  alert: string
  alertOperation: string
  alerts: string[]
  /** The rule can be deleted (not only turned off). */
  removable: boolean
  custom?: CustomRule
}


export interface ThresholdSpec {
  unit: string
  step: number
  min: number
  max?: number
  label: string
}

const thresholdBase: Record<RuleKind, Omit<ThresholdSpec, 'unit' | 'label'> | null> = {
  errors: { step: 0.5, min: 0, max: 100 },
  errorsPerOp: { step: 0.5, min: 0, max: 100 },
  opErrors: { step: 0.5, min: 0, max: 100 },
  latency: { step: 50, min: 0 },
  latencyPerOp: { step: 50, min: 0 },
  latencyAboveNormal: { step: 1, min: 1 },
  opLatency: { step: 50, min: 0 },
  memory: { step: 64, min: 0 },
  memoryLimit: { step: 1, min: 1, max: 100 },
  cpu: { step: 0.1, min: 0 },
  cpuLimit: { step: 1, min: 1, max: 100 },
  cpuAboveNormal: { step: 0.5, min: 1 },
  restarts: { step: 1, min: 0 },
  oomKilled: null,
  queueLag: { step: 10, min: 0 },
  queueStopped: null,
  slo: { step: 0.1, min: 50, max: 99.999 },
  traffic: { step: 1, min: 0, max: 99 },
  noData: null,
  noReadyPods: null,
  systemNoData: null,
  custom: null,
}

/** Unit of a threshold in a language: the interface's, or the notification's in previews. */
export const thresholdUnit = (kind: RuleKind, lang: Lang = getLang()): string => (thresholdBase[kind] ? tl(lang, `rule.unit.${kind as ThresholdKind}` as const) : '')

type ThresholdKind = Exclude<RuleKind, 'oomKilled' | 'queueStopped' | 'noData' | 'noReadyPods' | 'systemNoData' | 'custom'>

/** Threshold input of a rule kind, or null when the rule has no threshold. */
export function thresholdSpec(kind: RuleKind): ThresholdSpec | null {
  const b = thresholdBase[kind]
  if (!b) return null
  return { ...b, unit: thresholdUnit(kind), label: tr(`rule.threshold.${kind as ThresholdKind}` as const) }
}

type DurUnit = 'ms' | 's' | 'm' | 'h' | 'd' | 'w' | 'y'

/** "5m" -> "5 min", "1h30m" -> "1 h 30 min", "0s" -> "immediately". */
export function formatDuration(d: string | undefined): string {
  if (!d) return ''
  const parts = [...d.matchAll(/(\d+)(ms|s|m|h|d|w|y)/g)]
  if (parts.length === 0) return d
  if (parts.every((p) => p[1] === '0')) return tr('dur.immediately')
  return parts
    .filter((p) => p[1] !== '0')
    .map((p) => tr(`dur.${p[2] as DurUnit}` as const, { n: p[1] }))
    .join(' ')
}

const trim = (n: number) => String(Math.round(n * 1000) / 1000)

export function formatMs(ms: number): string {
  return ms < 1000 ? tr('dur.ms', { n: trim(ms) }) : tr('dur.s', { n: trim(ms / 1000) })
}

/** Kubernetes quantity -> MiB. */
export function quantityToMiB(q: string): number {
  const m = /^([0-9]+(?:\.[0-9]+)?)(Ki|Mi|Gi|Ti|k|M|G|T)?$/.exec(q.trim())
  if (!m) return NaN
  const n = Number(m[1])
  const mult: Record<string, number> = { '': 1, Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4, k: 1e3, M: 1e6, G: 1e9, T: 1e12 }
  return (n * mult[m[2] ?? '']) / 1024 ** 2
}

export function formatMiB(mib: number): string {
  return mib >= 1024 ? tr('size.gib', { n: trim(mib / 1024) }) : tr('size.mib', { n: trim(mib) })
}

const cores = (n: number) => tr(n === 1 ? 'rule.cores.one' : 'rule.cores.other', { n: trim(n) })

const isOn = (...flags: (boolean | undefined)[]) => flags.reduce<boolean>((acc, f) => (f === undefined ? acc : f), true)

function pick<T, K extends keyof T>(key: K, ...layers: (T | undefined)[]): T[K] | undefined {
  let v: T[K] | undefined
  for (const l of layers) if (l && l[key] !== undefined) v = l[key]
  return v
}

/** The traffic rule can exist: http/grpc, or queue/database with trafficDrop set (internal/generator trafficApplies). */
export function trafficApplies(s: Service): boolean {
  return s.type === 'http' || s.type === 'grpc' || !!s.trafficDrop
}

/** The traffic rule is on without anything set on the service: critical services, or defaults turn it on. */
function trafficOnByDefault(d: Description['spec']['defaults'], s: Service): boolean {
  return d?.trafficDrop?.enabled ?? !!s.critical
}

export function hasRequestRules(s: Service): boolean {
  if (s.type === 'http' || s.type === 'grpc') return true
  return !!(s.errorRate || s.latency || s.operations?.length || s.slo)
}

const serviceSeverity = (s: Service): Severity => (s.critical ? 'critical' : 'warning')

const spansOn = (desc: Description) => desc.spec.telemetry?.spans?.callsMetric !== DISABLED

const telemetryName = (s: Service) => s.telemetryName || s.name

/** The per-service no-data rule needs another service that still sends (internal/generator otherTelemetryNames). */
const hasOtherTelemetry = (desc: Description, s: Service) => desc.spec.services.some((x) => telemetryName(x) !== telemetryName(s))

/** The per-service no-data rule can exist: http/grpc, or queue/database with noData set (internal/generator mergeNoData). */
export function noDataApplies(desc: Description, s: Service): boolean {
  return (s.type === 'http' || s.type === 'grpc' || !!s.noData) && spansOn(desc) && hasOtherTelemetry(desc, s)
}

export function customAlertName(name: string): string {
  return 'KamertonCustom' + name.split('-').map((p) => (p ? p[0].toUpperCase() + p.slice(1) : '')).join('')
}

const OPERATORS = ['gt', 'gte', 'lt', 'lte', 'eq', 'ne'] as const
export const operatorWords = (op: string) => ((OPERATORS as readonly string[]).includes(op) ? tr(`rule.op.${op as (typeof OPERATORS)[number]}` as const) : op)

/** The shorter of two durations ('' counts as the second). */
function shorter(a: string, b: string): string {
  const sec = (d: string) => [...d.matchAll(/(\d+)(ms|s|m|h|d|w|y)/g)].reduce((n, p) => n + Number(p[1]) * ({ ms: 0.001, s: 1, m: 60, h: 3600, d: 86400, w: 604800, y: 31536000 } as Record<string, number>)[p[2]], 0)
  return a && sec(a) < sec(b) ? a : b
}

function during(forD: string): string {
  const f = formatDuration(forD)
  return f === tr('dur.immediately') || !f ? '' : tr('rule.during', { d: f })
}

/** All rules of a system, in the order of services. */
export function deriveRules(desc: Description): RuleItem[] {
  const out: RuleItem[] = []
  const d = desc.spec.defaults ?? {}
  if (spansOn(desc)) {
    const after = desc.spec.noData?.after ?? BUILTIN.noDataAfter
    out.push({
      id: `${SYSTEM_RULES}/systemNoData`,
      service: '',
      system: true,
      kind: 'systemNoData',
      enabled: desc.spec.noData?.enabled !== false,
      threshold: NaN,
      for: after,
      severity: 'critical',
      severityInherited: false,
      severityFixed: true,
      sentence: tr('rule.s.systemNoData', { after: formatDuration(after) }),
      alert: 'KamertonTelemetryMissing',
      alertOperation: '',
      alerts: ['KamertonTelemetryMissing'],
      removable: false,
    })
  }
  for (const s of desc.spec.services) {
    const sev = serviceSeverity(s)
    const base = { service: s.name }
    if (hasRequestRules(s)) {
      const er: ErrorRatePolicy[] = [d.errorRate ?? {}, s.errorRate ?? {}]
      const thr = pick('threshold', ...er) ?? BUILTIN.errorThreshold
      const forD = pick('for', ...er) ?? BUILTIN.errorFor
      const esev = pick('severity', ...er)
      const enabled = isOn(...er.map((x) => x.enabled))
      out.push({
        ...base,
        id: `${s.name}/errors`,
        kind: 'errors',
        enabled,
        threshold: thr * 100,
        for: forD,
        severity: esev ?? sev,
        severityInherited: !esev,
        sentence: tr('rule.s.errors', { thr: trim(thr * 100) }) + during(forD),
        alert: 'KamertonErrorRateHigh',
        alertOperation: '',
        alerts: ['KamertonErrorRateHigh'],
        removable: false,
      })
      {
        out.push({
          ...base,
          id: `${s.name}/errorsPerOp`,
          kind: 'errorsPerOp',
          enabled: enabled && (pick('perOperation', ...er) ?? true),
          threshold: thr * 100,
          for: forD,
          severity: esev ?? sev,
          severityInherited: !esev,
          sentence: tr('rule.s.errorsPerOp', { thr: trim(thr * 100) }) + during(forD),
          alert: 'KamertonOperationErrorRateHigh',
          alertOperation: '',
          alerts: ['KamertonOperationErrorRateHigh'],
          removable: false,
        })
      }
      const lt: LatencyPolicy[] = [d.latency ?? {}, s.latency ?? {}]
      const q = pick('quantile', ...lt) ?? BUILTIN.latencyQuantile
      const ms = pick('thresholdMs', ...lt) ?? BUILTIN.latencyMs
      const lfor = pick('for', ...lt) ?? BUILTIN.latencyFor
      const lsev = pick('severity', ...lt)
      const latOn = isOn(...lt.map((x) => x.enabled))
      out.push({
        ...base,
        id: `${s.name}/latency`,
        kind: 'latency',
        enabled: latOn,
        threshold: ms,
        for: lfor,
        severity: lsev ?? sev,
        severityInherited: !lsev,
        sentence: tr('rule.s.latency', { ms: formatMs(ms), pct: trim(100 - q * 100) }) + during(lfor),
        alert: 'KamertonLatencyHigh',
        alertOperation: '',
        alerts: ['KamertonLatencyHigh'],
        removable: false,
      })
      const opFor = shorter(lfor, BUILTIN.opLatencyFor)
      out.push({
        ...base,
        id: `${s.name}/latencyPerOp`,
        kind: 'latencyPerOp',
        enabled: latOn && (pick('perOperation', ...lt) ?? true),
        threshold: ms,
        for: '',
        severity: lsev ?? sev,
        severityInherited: !lsev,
        sentence: tr('rule.s.latencyPerOp', { ms: formatMs(ms), pct: trim(100 - q * 100) }) + during(opFor),
        alert: 'KamertonOperationLatencyHigh',
        alertOperation: '',
        alerts: ['KamertonOperationLatencyHigh'],
        removable: false,
      })
      const factor = pick('aboveNormalFactor', ...lt) ?? BUILTIN.latencyFactor
      const anFor = shorter(lfor, BUILTIN.aboveNormalFor)
      out.push({
        ...base,
        id: `${s.name}/latencyAboveNormal`,
        kind: 'latencyAboveNormal',
        enabled: latOn && factor > 0,
        threshold: factor > 0 ? factor : BUILTIN.latencyFactor,
        for: '',
        severity: lsev ?? sev,
        severityInherited: !lsev,
        sentence:
          tr('rule.s.latencyAboveNormal', { f: trim(factor > 0 ? factor : BUILTIN.latencyFactor), ms: formatMs(BUILTIN.latencyAboveNormalMinMs) }) + during(anFor),
        alert: 'KamertonLatencyAboveNormal',
        alertOperation: '',
        alerts: ['KamertonLatencyAboveNormal'],
        removable: false,
      })
      for (const op of s.operations ?? []) {
        if (op.errorRate) {
          const layers = [...er, op.errorRate]
          const t = pick('threshold', ...layers) ?? BUILTIN.errorThreshold
          const f = pick('for', ...layers) ?? BUILTIN.errorFor
          const sv = pick('severity', ...layers)
          out.push({
            ...base,
            id: `${s.name}/opErrors/${op.name}`,
            kind: 'opErrors',
            operation: op.name,
            enabled: isOn(...layers.map((x) => x.enabled)),
            threshold: t * 100,
            for: f,
            severity: sv ?? sev,
            severityInherited: !sv,
            sentence: tr('rule.s.opErrors', { op: op.name, thr: trim(t * 100) }) + during(f),
            alert: 'KamertonOperationErrorRateHigh',
            alertOperation: op.name,
            alerts: ['KamertonOperationErrorRateHigh'],
            removable: true,
          })
        }
        if (op.latency) {
          const layers = [...lt, op.latency]
          const oq = pick('quantile', ...layers) ?? BUILTIN.latencyQuantile
          const oms = pick('thresholdMs', ...layers) ?? BUILTIN.latencyMs
          const f = pick('for', ...layers) ?? BUILTIN.latencyFor
          const sv = pick('severity', ...layers)
          out.push({
            ...base,
            id: `${s.name}/opLatency/${op.name}`,
            kind: 'opLatency',
            operation: op.name,
            enabled: isOn(...layers.map((x) => x.enabled)),
            threshold: oms,
            for: f,
            severity: sv ?? sev,
            severityInherited: !sv,
            sentence: tr('rule.s.opLatency', { op: op.name, ms: formatMs(oms), pct: trim(100 - oq * 100) }) + during(f),
            alert: 'KamertonOperationLatencyHigh',
            alertOperation: op.name,
            alerts: ['KamertonOperationLatencyHigh'],
            removable: true,
          })
        }
      }
      if (s.slo) {
        out.push({
          ...base,
          id: `${s.name}/slo`,
          kind: 'slo',
          enabled: true,
          threshold: s.slo.objective,
          for: '',
          severity: sev,
          severityInherited: true,
          severityFixed: true,
          sentence: tr('rule.s.slo', { obj: trim(s.slo.objective), window: formatDuration(s.slo.window ?? BUILTIN.sloWindow) }),
          alert: 'KamertonSLOBurnRateFast',
          alertOperation: '',
          alerts: ['KamertonSLOBurnRateFast', 'KamertonSLOBurnRateSlow'],
          removable: true,
        })
      }
    }
    const nd: NoDataPolicy[] = [d.noData ?? {}, s.noData ?? {}]
    const noDataShown = noDataApplies(desc, s)
    const noDataOn = noDataShown && isOn(...nd.map((x) => x.enabled))
    const noDataAfter = pick('after', ...nd) ?? BUILTIN.noDataAfter
    let trafficSeverity: Severity | undefined
    if (trafficApplies(s)) {
      const tp: TrafficDropPolicy[] = [d.trafficDrop ?? {}, s.trafficDrop ?? {}]
      const onByDefault = trafficOnByDefault(d, s)
      const enabled = s.trafficDrop?.enabled ?? onByDefault
      // Shown when on, or when the service has its own setting; otherwise it is offered in "Add".
      if (enabled || s.trafficDrop) {
        const thr = pick('threshold', ...tp) ?? BUILTIN.trafficThreshold
        const tfor = pick('for', ...tp) ?? BUILTIN.trafficFor
        const tsev = pick('severity', ...tp)
        if (enabled && spansOn(desc)) trafficSeverity = tsev ?? sev
        out.push({
          ...base,
          id: `${s.name}/traffic`,
          kind: 'traffic',
          enabled,
          threshold: Math.round(thr * 1000) / 10,
          for: tfor,
          severity: tsev ?? sev,
          severityInherited: !tsev,
          sentence: (thr === 0 ? tr('rule.s.trafficNone') : tr('rule.s.traffic', { thr: trim(thr * 100) })) + during(tfor),
          note: noDataOn ? tr('rule.note.trafficNoData') : undefined,
          alert: 'KamertonRequestsStopped',
          alertOperation: '',
          alerts: ['KamertonRequestsStopped'],
          removable: !onByDefault,
        })
      }
    }
    if (noDataShown) {
      const nsev = pick('severity', ...nd)
      const def = trafficSeverity ?? 'warning'
      out.push({
        ...base,
        id: `${s.name}/noData`,
        kind: 'noData',
        enabled: noDataOn,
        threshold: NaN,
        for: noDataAfter,
        severity: nsev ?? def,
        severityInherited: !nsev,
        severityDefault: def,
        sentence: tr('rule.s.noData', { after: formatDuration(noDataAfter) }),
        alert: 'KamertonServiceTelemetryMissing',
        alertOperation: '',
        alerts: ['KamertonServiceTelemetryMissing'],
        removable: !(s.type === 'http' || s.type === 'grpc'),
      })
    }
    const sat = [d.saturation ?? {}, s.saturation ?? {}]
    const satOn = isOn(...sat.map((x) => x.enabled))
    const sfor = pick('for', ...sat) ?? BUILTIN.saturationFor
    const ssev = pick('severity', ...sat)
    const mem = pick('memory', ...sat)
    if (!mem) {
      const share = pick('memoryLimitShare', ...sat) ?? BUILTIN.memoryLimitShare
      const pct = Math.round((share > 0 ? share : BUILTIN.memoryLimitShare) * 1000) / 10
      out.push({
        ...base,
        id: `${s.name}/memoryLimit`,
        kind: 'memoryLimit',
        enabled: satOn && share > 0,
        threshold: pct,
        for: sfor,
        severity: ssev ?? sev,
        severityInherited: !ssev,
        sentence: tr('rule.s.memoryLimit', { pct: trim(pct) }) + during(sfor),
        alert: 'KamertonMemoryNearLimit',
        alertOperation: '',
        alerts: ['KamertonMemoryNearLimit'],
        removable: false,
      })
    }
    if (mem) {
      const mib = quantityToMiB(mem)
      out.push({
        ...base,
        id: `${s.name}/memory`,
        kind: 'memory',
        enabled: satOn,
        threshold: mib,
        for: sfor,
        severity: ssev ?? sev,
        severityInherited: !ssev,
        sentence: tr('rule.s.memory', { size: formatMiB(mib) }) + during(sfor),
        alert: 'KamertonMemoryHigh',
        alertOperation: '',
        alerts: ['KamertonMemoryHigh'],
        removable: true,
      })
    }
    const cpu = pick('cpuCores', ...sat)
    if (!(cpu !== undefined && cpu > 0)) {
      const share = pick('cpuLimitShare', ...sat) ?? BUILTIN.cpuLimitShare
      const pct = Math.round((share > 0 ? share : BUILTIN.cpuLimitShare) * 1000) / 10
      out.push({
        ...base,
        id: `${s.name}/cpuLimit`,
        kind: 'cpuLimit',
        enabled: satOn && share > 0,
        threshold: pct,
        for: sfor,
        severity: ssev ?? sev,
        severityInherited: !ssev,
        sentence: tr('rule.s.cpuLimit', { pct: trim(pct) }) + during(sfor),
        alert: 'KamertonCPUNearLimit',
        alertOperation: '',
        alerts: ['KamertonCPUNearLimit'],
        removable: false,
      })
      const factor = pick('cpuAboveNormalFactor', ...sat) ?? BUILTIN.cpuFactor
      const f = factor > 0 ? factor : BUILTIN.cpuFactor
      out.push({
        ...base,
        id: `${s.name}/cpuAboveNormal`,
        kind: 'cpuAboveNormal',
        enabled: satOn && factor > 0,
        threshold: f,
        for: sfor,
        severity: ssev ?? sev,
        severityInherited: !ssev,
        sentence: tr('rule.s.cpuAboveNormal', { f: trim(f), cores: cores(BUILTIN.cpuAboveNormalMinCores) }) + during(sfor),
        alert: 'KamertonCPUAboveNormal',
        alertOperation: '',
        alerts: ['KamertonCPUAboveNormal'],
        removable: false,
      })
    }
    if (cpu !== undefined && cpu > 0) {
      out.push({
        ...base,
        id: `${s.name}/cpu`,
        kind: 'cpu',
        enabled: satOn,
        threshold: cpu,
        for: sfor,
        severity: ssev ?? sev,
        severityInherited: !ssev,
        sentence: tr('rule.s.cpu', { cores: cores(cpu) }) + during(sfor),
        alert: 'KamertonCPUHigh',
        alertOperation: '',
        alerts: ['KamertonCPUHigh'],
        removable: true,
      })
    }
    const restarts = pick('maxRestartsPerHour', ...sat) ?? BUILTIN.maxRestarts
    out.push({
      ...base,
      id: `${s.name}/restarts`,
      kind: 'restarts',
      enabled: satOn && isOn(...sat.map((x) => x.restartsEnabled)),
      threshold: restarts,
      for: '',
      severity: ssev ?? sev,
      severityInherited: !ssev,
      sentence: tn('rule.s.restarts', restarts),
      alert: 'KamertonPodRestarting',
      alertOperation: '',
      alerts: ['KamertonPodRestarting'],
      removable: false,
    })
    out.push({
      ...base,
      id: `${s.name}/oomKilled`,
      kind: 'oomKilled',
      enabled: satOn && isOn(...sat.map((x) => x.oomKilledEnabled)),
      threshold: NaN,
      for: '',
      severity: ssev ?? sev,
      severityInherited: !ssev,
      sentence: tr('rule.s.oomKilled'),
      alert: 'KamertonOOMKilled',
      alertOperation: '',
      alerts: ['KamertonOOMKilled'],
      removable: false,
    })
    if (desc.spec.telemetry?.containers?.readyMetric !== DISABLED) {
      const np: NoReadyPodsPolicy[] = [d.noReadyPods ?? {}, s.noReadyPods ?? {}]
      const pfor = pick('for', ...np) ?? BUILTIN.noReadyPodsFor
      const psev = pick('severity', ...np)
      out.push({
        ...base,
        id: `${s.name}/noReadyPods`,
        kind: 'noReadyPods',
        enabled: isOn(...np.map((x) => x.enabled)),
        threshold: NaN,
        for: pfor,
        severity: psev ?? sev,
        severityInherited: !psev,
        sentence: tr('rule.s.noReadyPods', { gone: formatDuration(BUILTIN.podsGoneRange) }) + during(pfor),
        alert: 'KamertonNoReadyPods',
        alertOperation: '',
        alerts: ['KamertonNoReadyPods'],
        removable: false,
      })
    }
    if (s.queue) {
      const qfor = s.queue.for ?? BUILTIN.queueFor
      out.push({
        ...base,
        id: `${s.name}/queueLag`,
        kind: 'queueLag',
        enabled: true,
        threshold: s.queue.maxLag,
        for: qfor,
        severity: s.queue.severity ?? sev,
        severityInherited: !s.queue.severity,
        sentence: tr('rule.s.queueLag', { topic: s.queue.topic, n: trim(s.queue.maxLag) }) + during(qfor),
        alert: 'KamertonQueueLagHigh',
        alertOperation: '',
        alerts: ['KamertonQueueLagHigh'],
        removable: false,
      })
      const stoppedFor = shorter(qfor, BUILTIN.queueStoppedFor)
      out.push({
        ...base,
        id: `${s.name}/queueStopped`,
        kind: 'queueStopped',
        enabled: s.queue.consumerStoppedEnabled !== false,
        threshold: NaN,
        for: '',
        severity: s.queue.severity ?? sev,
        severityInherited: !s.queue.severity,
        sentence:
          tr('rule.s.queueStopped', { topic: s.queue.topic, members: formatDuration(BUILTIN.queueNoMembers), stalled: formatDuration(BUILTIN.queueStalled) }) + during(stoppedFor),
        alert: 'KamertonQueueConsumerStopped',
        alertOperation: '',
        alerts: ['KamertonQueueConsumerStopped'],
        removable: false,
      })
    }
    for (const cr of s.customRules ?? []) {
      const f = cr.for ?? BUILTIN.customFor
      out.push({
        ...base,
        id: `${s.name}/custom/${cr.name}`,
        kind: 'custom',
        customName: cr.name,
        enabled: cr.enabled !== false,
        threshold: cr.condition?.threshold ?? NaN,
        for: f,
        severity: cr.severity ?? sev,
        severityInherited: !cr.severity,
        sentence: customSentence(cr),
        alert: customAlertName(cr.name),
        alertOperation: '',
        alerts: [customAlertName(cr.name)],
        removable: true,
        custom: cr,
      })
    }
  }
  return out
}

/** Rules the core generates for these cards: the SLO card stands for two (internal/generator, rules.Count). */
export function generatedCount(rules: RuleItem[]): number {
  return rules.reduce((n, r) => n + (r.enabled ? r.alerts.length : 0), 0)
}

export function customSentence(cr: CustomRule): string {
  const f = during(cr.for ?? BUILTIN.customFor)
  if (cr.condition) {
    const unit = cr.unit ? ` ${cr.unit}` : ''
    return `${cr.title}: ${cr.condition.metric} ${operatorWords(cr.condition.operator)} ${trim(cr.condition.threshold)}${unit}${f}`
  }
  return `${cr.title}${f}`
}

export function addableKinds(desc: Description, service: string): RuleKind[] {
  const s = desc.spec.services.find((x) => x.name === service)
  if (!s) return []
  const present = new Set(deriveRules(desc).filter((r) => r.service === service).map((r) => r.kind))
  const out: RuleKind[] = []
  if (!hasRequestRules(s)) out.push('errors', 'latency')
  else {
    out.push('opErrors', 'opLatency')
    if (!present.has('slo')) out.push('slo')
  }
  if (!present.has('traffic') && (s.type === 'http' || s.type === 'grpc')) out.push('traffic')
  if (!present.has('noData') && !(s.type === 'http' || s.type === 'grpc') && spansOn(desc) && hasOtherTelemetry(desc, s)) out.push('noData')
  if (!present.has('memory')) out.push('memory')
  if (!present.has('cpu')) out.push('cpu')
  out.push('custom')
  return out
}

export interface RulePatch {
  enabled?: boolean
  threshold?: number
  for?: string
  /** null resets to the service default. */
  severity?: Severity | null
}

function prune<T extends object>(o: T | undefined): T | undefined {
  if (!o) return undefined
  for (const k of Object.keys(o) as (keyof T)[]) if (o[k] === undefined) delete o[k]
  return Object.keys(o).length > 0 ? o : undefined
}

const round = (n: number, digits: number) => Math.round(n * 10 ** digits) / 10 ** digits

/** Sets enabled on a policy layer so the merged value becomes `on`. */
function setEnabled<T extends { enabled?: boolean }>(layer: T, inherited: (boolean | undefined)[], on: boolean) {
  delete layer.enabled
  if (isOn(...inherited) !== on) layer.enabled = on
}

function applySeverity<T extends { severity?: Severity }>(layer: T, sev: Severity | null | undefined) {
  if (sev === undefined) return
  if (sev === null) delete layer.severity
  else layer.severity = sev
}

export function setRule(desc: Description, id: string, patch: RulePatch): Description {
  const next = clone(desc)
  const [service, kind, ...rest] = id.split('/')
  const key = rest.join('/')
  if (service === SYSTEM_RULES) {
    if (kind !== 'systemNoData') return desc
    const p = next.spec.noData ?? {}
    if (patch.enabled !== undefined) {
      delete p.enabled
      if (!patch.enabled) p.enabled = false
    }
    if (patch.for !== undefined) p.after = patch.for
    next.spec.noData = prune(p)
    if (!next.spec.noData) delete next.spec.noData
    return next
  }
  const s = next.spec.services.find((x) => x.name === service)
  if (!s) return desc
  const d = next.spec.defaults ?? {}
  switch (kind as RuleKind) {
    case 'errors':
    case 'errorsPerOp': {
      const p: ErrorRatePolicy = s.errorRate ?? {}
      if (patch.enabled !== undefined) {
        if (kind === 'errorsPerOp') {
          if (patch.enabled) setEnabled(p, [d.errorRate?.enabled], true)
          delete p.perOperation
          if ((d.errorRate?.perOperation ?? true) !== patch.enabled) p.perOperation = patch.enabled
        } else setEnabled(p, [d.errorRate?.enabled], patch.enabled)
      }
      if (patch.threshold !== undefined) p.threshold = round(patch.threshold / 100, 6)
      if (patch.for !== undefined) p.for = patch.for
      applySeverity(p, patch.severity)
      s.errorRate = prune(p)
      break
    }
    case 'latency':
    case 'latencyPerOp':
    case 'latencyAboveNormal': {
      const p: LatencyPolicy = s.latency ?? {}
      if (patch.enabled !== undefined) {
        if (kind === 'latency') setEnabled(p, [d.latency?.enabled], patch.enabled)
        else {
          if (patch.enabled) setEnabled(p, [d.latency?.enabled], true)
          if (kind === 'latencyPerOp') {
            delete p.perOperation
            if ((d.latency?.perOperation ?? true) !== patch.enabled) p.perOperation = patch.enabled
          } else {
            delete p.aboveNormalFactor
            const inherited = d.latency?.aboveNormalFactor ?? BUILTIN.latencyFactor
            if (patch.enabled && inherited <= 0) p.aboveNormalFactor = BUILTIN.latencyFactor
            if (!patch.enabled && inherited > 0) p.aboveNormalFactor = 0
          }
        }
      }
      if (patch.threshold !== undefined && kind === 'latencyAboveNormal') p.aboveNormalFactor = round(patch.threshold, 3)
      else if (patch.threshold !== undefined) p.thresholdMs = round(patch.threshold, 3)
      if (patch.for !== undefined) p.for = patch.for
      applySeverity(p, patch.severity)
      s.latency = prune(p)
      break
    }
    case 'opErrors':
    case 'opLatency': {
      const op = s.operations?.find((o) => o.name === key)
      if (!op) return desc
      if (kind === 'opErrors') {
        const p: ErrorRatePolicy = op.errorRate ?? {}
        if (patch.enabled !== undefined) setEnabled(p, [d.errorRate?.enabled, s.errorRate?.enabled], patch.enabled)
        if (patch.threshold !== undefined) p.threshold = round(patch.threshold / 100, 6)
        if (patch.for !== undefined) p.for = patch.for
        applySeverity(p, patch.severity)
        op.errorRate = p
      } else {
        const p: LatencyPolicy = op.latency ?? {}
        if (patch.enabled !== undefined) setEnabled(p, [d.latency?.enabled, s.latency?.enabled], patch.enabled)
        if (patch.threshold !== undefined) p.thresholdMs = round(patch.threshold, 3)
        if (patch.for !== undefined) p.for = patch.for
        applySeverity(p, patch.severity)
        op.latency = p
      }
      break
    }
    case 'memory':
    case 'cpu':
    case 'restarts':
    case 'memoryLimit':
    case 'cpuLimit':
    case 'cpuAboveNormal':
    case 'oomKilled': {
      const p = s.saturation ?? {}
      if (patch.enabled !== undefined) {
        if (kind === 'restarts') {
          if (patch.enabled) setEnabled(p, [d.saturation?.enabled], true)
          delete p.restartsEnabled
          if (isOn(d.saturation?.restartsEnabled) !== patch.enabled) p.restartsEnabled = patch.enabled
        } else if (kind === 'oomKilled') {
          if (patch.enabled) setEnabled(p, [d.saturation?.enabled], true)
          delete p.oomKilledEnabled
          if (isOn(d.saturation?.oomKilledEnabled) !== patch.enabled) p.oomKilledEnabled = patch.enabled
        } else if (kind === 'memoryLimit' || kind === 'cpuLimit' || kind === 'cpuAboveNormal') {
          // Each of these is turned off by its own value 0; the saturation switch stays as it is.
          const key = kind === 'memoryLimit' ? 'memoryLimitShare' : kind === 'cpuLimit' ? 'cpuLimitShare' : 'cpuAboveNormalFactor'
          const builtin = kind === 'memoryLimit' ? BUILTIN.memoryLimitShare : kind === 'cpuLimit' ? BUILTIN.cpuLimitShare : BUILTIN.cpuFactor
          if (patch.enabled) setEnabled(p, [d.saturation?.enabled], true)
          delete p[key]
          const inherited = d.saturation?.[key] ?? builtin
          if (patch.enabled && inherited <= 0) p[key] = builtin
          if (!patch.enabled && inherited > 0) p[key] = 0
        } else {
          setEnabled(p, [d.saturation?.enabled], patch.enabled)
        }
      }
      if (patch.threshold !== undefined) {
        if (kind === 'memory') p.memory = `${Math.max(1, Math.round(patch.threshold))}Mi`
        if (kind === 'cpu') p.cpuCores = round(patch.threshold, 3)
        if (kind === 'restarts') p.maxRestartsPerHour = Math.max(0, Math.round(patch.threshold))
        if (kind === 'memoryLimit') p.memoryLimitShare = round(patch.threshold / 100, 4)
        if (kind === 'cpuLimit') p.cpuLimitShare = round(patch.threshold / 100, 4)
        if (kind === 'cpuAboveNormal') p.cpuAboveNormalFactor = round(patch.threshold, 3)
      }
      if (patch.for !== undefined) p.for = patch.for
      applySeverity(p, patch.severity)
      s.saturation = prune(p)
      break
    }
    case 'queueLag': {
      if (!s.queue) return desc
      if (patch.threshold !== undefined) s.queue.maxLag = round(patch.threshold, 3)
      if (patch.for !== undefined) s.queue.for = patch.for
      applySeverity(s.queue, patch.severity)
      break
    }
    case 'queueStopped': {
      // On by default; only false is written. Severity is shared with the lag rule; the pending period is fixed.
      if (!s.queue) return desc
      if (patch.enabled !== undefined) {
        delete s.queue.consumerStoppedEnabled
        if (!patch.enabled) s.queue.consumerStoppedEnabled = false
      }
      applySeverity(s.queue, patch.severity)
      break
    }
    case 'slo': {
      if (!s.slo) return desc
      if (patch.threshold !== undefined) s.slo.objective = round(patch.threshold, 4)
      if (patch.enabled === false) delete s.slo
      break
    }
    case 'traffic': {
      const p: TrafficDropPolicy = s.trafficDrop ?? {}
      if (patch.enabled !== undefined) {
        delete p.enabled
        if (trafficOnByDefault(d, s) !== patch.enabled) p.enabled = patch.enabled
      }
      if (patch.threshold !== undefined) p.threshold = round(patch.threshold / 100, 4)
      if (patch.for !== undefined) p.for = patch.for
      applySeverity(p, patch.severity)
      const pruned = prune(p)
      if (pruned) s.trafficDrop = pruned
      else if (trafficOnByDefault(d, s)) delete s.trafficDrop
      // Off by default and nothing set: keep the card, turned off.
      else s.trafficDrop = { enabled: false }
      break
    }
    case 'noData': {
      const p: NoDataPolicy = s.noData ?? {}
      if (patch.enabled !== undefined) setEnabled(p, [d.noData?.enabled], patch.enabled)
      if (patch.for !== undefined) p.after = patch.for
      applySeverity(p, patch.severity)
      // For queue and database the policy itself turns the rule on, so an empty one stays.
      s.noData = prune(p) ?? (s.type === 'http' || s.type === 'grpc' ? undefined : {})
      if (!s.noData) delete s.noData
      break
    }
    case 'noReadyPods': {
      const p: NoReadyPodsPolicy = s.noReadyPods ?? {}
      if (patch.enabled !== undefined) setEnabled(p, [d.noReadyPods?.enabled], patch.enabled)
      if (patch.for !== undefined) p.for = patch.for
      applySeverity(p, patch.severity)
      s.noReadyPods = prune(p)
      if (!s.noReadyPods) delete s.noReadyPods
      break
    }
    case 'custom': {
      const cr = s.customRules?.find((c) => c.name === key)
      if (!cr) return desc
      if (patch.enabled !== undefined) {
        if (patch.enabled) delete cr.enabled
        else cr.enabled = false
      }
      if (patch.threshold !== undefined && cr.condition) cr.condition.threshold = patch.threshold
      if (patch.for !== undefined) cr.for = patch.for
      applySeverity(cr, patch.severity)
      break
    }
  }
  return next
}

export interface NewRule {
  kind: RuleKind
  operation?: string
  threshold?: number
  for?: string
  severity?: Severity
  custom?: CustomRule
}

export function addRule(desc: Description, service: string, r: NewRule): Description {
  let next = clone(desc)
  const s = next.spec.services.find((x) => x.name === service)
  if (!s) return desc
  const patch: RulePatch = { threshold: r.threshold, for: r.for, severity: r.severity }
  switch (r.kind) {
    case 'errors':
      s.errorRate = { ...(s.errorRate ?? {}), enabled: true }
      break
    case 'latency':
      s.latency = { ...(s.latency ?? {}), enabled: true }
      break
    case 'errorsPerOp':
    case 'latencyPerOp':
    case 'latencyAboveNormal':
    case 'memoryLimit':
    case 'cpuLimit':
    case 'cpuAboveNormal':
    case 'oomKilled':
    case 'queueStopped':
    case 'noReadyPods':
    case 'systemNoData':
      // Built-in rules: adding means turning on (setRule below).
      break
    case 'noData':
      s.noData = s.noData ?? {}
      break
    case 'opErrors':
    case 'opLatency': {
      if (!r.operation) return desc
      s.operations = s.operations ?? []
      let op = s.operations.find((o) => o.name === r.operation)
      if (!op) {
        op = { name: r.operation }
        s.operations.push(op)
      }
      if (r.kind === 'opErrors') op.errorRate = op.errorRate ?? {}
      else op.latency = op.latency ?? {}
      break
    }
    case 'memory':
      s.saturation = { ...(s.saturation ?? {}), memory: s.saturation?.memory ?? SUGGESTED.memory }
      break
    case 'cpu':
      s.saturation = { ...(s.saturation ?? {}), cpuCores: s.saturation?.cpuCores ?? SUGGESTED.cpuCores }
      break
    case 'restarts':
      break
    case 'traffic':
      s.trafficDrop = { ...(s.trafficDrop ?? {}) }
      break
    case 'slo':
      s.slo = s.slo ?? { objective: r.threshold ?? SUGGESTED.sloObjective }
      break
    case 'queueLag':
      s.queue = s.queue ?? { topic: s.name, maxLag: r.threshold ?? SUGGESTED.queueMaxLag }
      break
    case 'custom': {
      if (!r.custom) return desc
      s.customRules = [...(s.customRules ?? []), clone(r.custom)]
      return next
    }
  }
  const id = ruleId(service, r.kind, r.operation)
  next = setRule(next, id, { ...patch, enabled: true })
  // Request rules of queues and databases exist only while a policy is set.
  const ns = next.spec.services.find((x) => x.name === service)!
  if (r.kind === 'errors' && !hasRequestRules(ns)) ns.errorRate = { ...(ns.errorRate ?? {}), enabled: true }
  if (r.kind === 'latency' && !hasRequestRules(ns)) ns.latency = { ...(ns.latency ?? {}), enabled: true }
  return next
}

export function ruleId(service: string, kind: RuleKind, sub?: string): string {
  return sub ? `${service}/${kind}/${sub}` : `${service}/${kind}`
}

/** Deletes a rule; rules that always exist are turned off instead. */
export function removeRule(desc: Description, id: string): Description {
  const next = clone(desc)
  const [service, kind, ...rest] = id.split('/')
  const key = rest.join('/')
  if (service === SYSTEM_RULES) return setRule(desc, id, { enabled: false })
  const s = next.spec.services.find((x) => x.name === service)
  if (!s) return desc
  switch (kind as RuleKind) {
    case 'opErrors':
    case 'opLatency': {
      const op = s.operations?.find((o) => o.name === key)
      if (!op) return desc
      if (kind === 'opErrors') delete op.errorRate
      else delete op.latency
      if (!op.errorRate && !op.latency) s.operations = s.operations!.filter((o) => o !== op)
      if (s.operations?.length === 0) delete s.operations
      return next
    }
    case 'memory':
      if (s.saturation) {
        delete s.saturation.memory
        s.saturation = prune(s.saturation)
      }
      return next
    case 'cpu':
      if (s.saturation) {
        delete s.saturation.cpuCores
        s.saturation = prune(s.saturation)
      }
      return next
    case 'traffic':
      if (trafficOnByDefault(next.spec.defaults, s)) return setRule(desc, id, { enabled: false })
      delete s.trafficDrop
      return next
    case 'slo':
      delete s.slo
      return next
    case 'noData':
      if (s.type === 'http' || s.type === 'grpc') return setRule(desc, id, { enabled: false })
      delete s.noData
      return next
    case 'custom':
      s.customRules = (s.customRules ?? []).filter((c) => c.name !== key)
      if (s.customRules.length === 0) delete s.customRules
      return next
    default:
      return setRule(desc, id, { enabled: false })
  }
}

/** A custom rule name not used in the service: rule-1, rule-2, … */
export function nextCustomName(s: Service): string {
  const taken = new Set((s.customRules ?? []).map((c) => c.name))
  for (let i = 1; ; i++) if (!taken.has(`rule-${i}`)) return `rule-${i}`
}

export type RuleState = 'ok' | 'pending' | 'warning' | 'firing' | 'unknown'

export interface AlertStateSample {
  alert: string
  service: string
  operation?: string
  state: string
  severity?: string
}

/** Whether a series of the core (alert state, noise counters) belongs to the card: alert name, service and operation label. */
export const ruleMatches = (rule: RuleItem, x: { alert?: string; service?: string; operation?: string }): boolean =>
  rule.alerts.includes(x.alert ?? '') && (x.service ?? '') === rule.service && (x.operation ?? '') === rule.alertOperation

export function ruleState(rule: RuleItem, samples: AlertStateSample[] | undefined): RuleState {
  if (!samples) return 'unknown'
  if (!rule.enabled) return 'ok'
  const mine = samples.filter((x) => ruleMatches(rule, x))
  if (mine.some((x) => x.state === 'firing' && x.severity === 'warning')) return mine.some((x) => x.state === 'firing' && x.severity === 'critical') ? 'firing' : 'warning'
  if (mine.some((x) => x.state === 'firing')) return 'firing'
  if (mine.some((x) => x.state === 'pending')) return 'pending'
  return 'ok'
}

/** State of a service: firing (critical alert), warning (warning alert), pending, ok. */
export function serviceState(service: string, samples: AlertStateSample[] | undefined): RuleState {
  if (!samples) return 'unknown'
  const mine = samples.filter((x) => x.service === service)
  if (mine.some((x) => x.state === 'firing' && x.severity === 'critical')) return 'firing'
  if (mine.some((x) => x.state === 'firing')) return 'warning'
  if (mine.some((x) => x.state === 'pending')) return 'pending'
  return 'ok'
}

export const stateLabel = (st: RuleState): string => tr(`rule.state.${st}` as const)
