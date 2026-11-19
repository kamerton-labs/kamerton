// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// The system as it is now: the map, a service's numbers over time, the requests that failed and
// one request with what it wrote. Mirrors internal/server/live.go and internal/live.

import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { getLang } from '@/i18n'
import { apiGet } from '@/lib/api'

export type ServiceState = 'source' | 'warning' | 'affected' | 'ok'

export interface NowAlert {
  name: string
  title: string
  severity: 'critical' | 'warning' | string
  value?: string
  startsAt: string
}

export interface NowService {
  name: string
  type: string
  critical?: boolean
  dependsOn: string[]
  state: ServiceState
  because?: string[]
  since?: string
  alerts: NowAlert[]
  requests: number | null
  errorRatio: number | null
  p95: number | null
}

export interface NowIncident {
  id: string
  roots: string[]
  affected: string[]
  startsAt: string
}

export interface SLO {
  service: string
  objective: number
  window: string
  availability: number | null
  budgetLeft: number | null
}

export interface NowEdge {
  from: string
  to: string
  requests: number | null
  errorRatio: number | null
}

export interface SystemNow {
  alerting: boolean
  metrics: boolean
  traces: boolean
  logs: boolean
  message?: string
  services: NowService[]
  incidents: NowIncident[]
  /** Calls Tempo saw between the services; empty without its metrics generator. */
  edges: NowEdge[]
  slo: SLO[]
  at: string
}

export interface CallSeries {
  timestamps: number[]
  step: number
  start: string
  end: string
  requests?: Chart
  errors?: Chart
  latency?: Chart
}

export interface CallsResponse {
  calls: CallSeries
  failed: RequestHit[]
  failedError?: string
}

export interface Line {
  label: string
  values: (number | null)[]
}

export interface Chart {
  lines: Line[]
  now: number | null
  usual: number | null
  limit?: number | null
}

export interface Operation {
  name: string
  requests: number | null
  errorRatio: number | null
  p95: number | null
}

export interface ServiceSeries {
  start: string
  end: string
  step: number
  timestamps: number[]
  requests?: Chart
  errors?: Chart
  latency?: Chart
  memory?: Chart
  cpu?: Chart
  queue?: Chart
  restarts: number | null
  operations: Operation[]
}

export interface RequestHit {
  traceId: string
  start: string
  durationMs: number
  rootService?: string
  rootName?: string
  span?: string
  spanId?: string
  spanMs?: number
}

export interface Attr {
  key: string
  value: string
}

export interface SpanEvent {
  name: string
  atMs: number
  attrs: Attr[]
}

export interface Span {
  id: string
  parent?: string
  service: string
  name: string
  kind: string
  startMs: number
  durationMs: number
  error: boolean
  status?: string
  attrs: Attr[]
  events: SpanEvent[]
}

export interface LogLine {
  time: string
  service: string
  level?: string
  spanId?: string
  line: string
}

export interface TraceView {
  id: string
  start: string
  durationMs: number
  spans: Span[]
  logs: LogLine[]
  logsError?: string
  /** Telemetry name → name of the service in the system. */
  services: Record<string, string>
}

export const RANGES = ['15m', '1h', '6h', '24h', '7d'] as const
export type Range = (typeof RANGES)[number]

const sys = (id: string) => `/systems/${encodeURIComponent(id)}`

export const useSystemNow = (id: string) =>
  useQuery({ queryKey: ['system', id, 'now'], queryFn: () => apiGet<SystemNow>(`${sys(id)}/now`), refetchInterval: 20_000 })

export const useServiceSeries = (id: string, service: string, range: Range) =>
  useQuery({
    queryKey: ['system', id, 'series', service, range],
    queryFn: () => apiGet<ServiceSeries>(`${sys(id)}/services/${encodeURIComponent(service)}/series?range=${range}`),
    refetchInterval: range === '15m' || range === '1h' ? 30_000 : 120_000,
    placeholderData: keepPreviousData,
  })

export interface RequestQuery {
  kind: 'errors' | 'slow'
  start: number
  end: number
  over?: number
  operation?: string
}

export const useServiceRequests = (id: string, service: string, q: RequestQuery | null) =>
  useQuery({
    queryKey: ['system', id, 'requests', service, q],
    enabled: !!q,
    queryFn: () => {
      const p = new URLSearchParams({ kind: q!.kind, start: String(Math.floor(q!.start)), end: String(Math.ceil(q!.end)) })
      if (q!.over) p.set('over', String(Math.round(q!.over)))
      if (q!.operation) p.set('operation', q!.operation)
      return apiGet<{ requests: RequestHit[] }>(`${sys(id)}/services/${encodeURIComponent(service)}/requests?${p}`)
    },
    placeholderData: keepPreviousData,
  })

export const useCalls = (id: string, from: string, to: string, enabled = true) =>
  useQuery({
    queryKey: ['system', id, 'calls', from, to],
    enabled,
    queryFn: () => apiGet<CallsResponse>(`${sys(id)}/calls?${new URLSearchParams({ from, to, range: '1h' })}`),
    refetchInterval: 30_000,
  })

export interface Change {
  kind: 'rollout' | 'scaling' | 'restart' | 'warning'
  at?: string
  subject: string
  from?: string
  to?: string
  count?: number
  reason?: string
  text?: string
}

export interface ChangesResponse {
  available: boolean
  error?: string
  changes: Change[]
  pods?: number
  notReady?: number
  noPods?: boolean
}

export const useChanges = (id: string, service: string) =>
  useQuery({
    queryKey: ['system', id, 'changes', service],
    queryFn: () => apiGet<ChangesResponse>(`${sys(id)}/services/${encodeURIComponent(service)}/changes`),
    refetchInterval: 60_000,
  })

export const useTrace = (id: string, trace: string) =>
  useQuery({ queryKey: ['system', id, 'trace', trace], queryFn: () => apiGet<TraceView>(`${sys(id)}/traces/${encodeURIComponent(trace)}`), retry: false, staleTime: Infinity })

// ---- Words and numbers ----

const nf = (digits: number) => new Intl.NumberFormat(getLang() === 'ru' ? 'ru-RU' : 'en-US', { maximumFractionDigits: digits, minimumFractionDigits: 0 })

/** 0.1234 → "12%", 0.0012 → "0,12%": enough digits to tell a small share from none. */
export function percent(v: number | null | undefined): string {
  if (v == null) return '—'
  const p = v * 100
  if (p === 0) return '0%'
  if (p < 0.1) return nf(2).format(p) + '%'
  if (p < 10) return nf(1).format(p) + '%'
  return nf(0).format(p) + '%'
}

/** Availability against an objective needs the hundredths: 99,48% is not 99,5%. */
export function availability(v: number | null | undefined): string {
  if (v == null) return '—'
  return nf(2).format(Math.floor(v * 10000) / 100) + '%'
}

/** "30d" → "30 дн" / "30 d"; anything else as written. */
export function windowText(w: string, day: (n: number) => string, hour: (n: number) => string): string {
  const m = /^(\d+)([dh])$/.exec(w)
  if (!m) return w
  return m[2] === 'd' ? day(Number(m[1])) : hour(Number(m[1]))
}

/** Requests per second: "6,3/s", small ones per minute. */
export function rate(v: number | null | undefined, perSec: string, perMin: string): string {
  if (v == null) return '—'
  if (v > 0 && v < 0.1) return nf(1).format(v * 60) + perMin
  return nf(v < 10 ? 1 : 0).format(v) + perSec
}

/** A rate formatter with one unit for a whole axis, picked by its largest value. */
export function rateAxis(max: number, perSec: string, perMin: string): (v: number) => string {
  if (max > 0 && max < 0.1) return (v) => nf(v * 60 < 10 ? 1 : 0).format(v * 60) + perMin
  return (v) => nf(v < 10 ? 1 : 0).format(v) + perSec
}

/** The largest value of a chart's lines. */
export function maxOf(lines: { values: (number | null)[] }[]): number {
  let m = 0
  for (const l of lines) for (const v of l.values) if (v != null && v > m) m = v
  return m
}

/** Milliseconds: "42 ms", "1,2 s". */
export function duration(ms: number | null | undefined, msUnit: string, sUnit: string, minUnit = getLang() === 'ru' ? 'мин' : 'min'): string {
  if (ms == null) return '—'
  if (ms < 1) return nf(2).format(ms) + ' ' + msUnit
  if (ms < 1000) return nf(0).format(ms) + ' ' + msUnit
  if (ms < 60_000) return nf(ms < 10_000 ? 1 : 0).format(ms / 1000) + ' ' + sUnit
  return nf(0).format(ms / 60_000) + ' ' + minUnit
}

/** Bytes: "48 MiB". */
export function bytes(v: number | null | undefined): string {
  if (v == null) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return nf(v < 10 ? 1 : 0).format(v) + ' ' + units[i]
}

/** CPU in cores: below one core in millicores, "120 m". */
export function cores(v: number | null | undefined): string {
  if (v == null) return '—'
  if (v < 1) return nf(0).format(v * 1000) + ' m'
  return nf(2).format(v)
}

/** The last value of a line that has one. */
export function lastOf(values: (number | null)[] | undefined): number | null {
  if (!values) return null
  for (let i = values.length - 1; i >= 0; i--) if (values[i] != null) return values[i]
  return null
}

export function number(v: number | null | undefined, digits = 1): string {
  return v == null ? '—' : nf(digits).format(v)
}

/** "14:03" in the viewer's time. */
export function clock(iso: string | number): string {
  const d = typeof iso === 'number' ? new Date(iso * 1000) : new Date(iso)
  return d.toLocaleTimeString(getLang() === 'ru' ? 'ru-RU' : 'en-US', { hour: '2-digit', minute: '2-digit' })
}

/** An alert title without the service name it starts with: next to the name it only repeats it. */
export function bareTitle(title: string, service: string): string {
  for (const sep of [': ', ' — ', ' - ']) if (title.startsWith(service + sep)) return title.slice(service.length + sep.length)
  return title
}

// ---- The map ----

/** The services whose failure is the news, worst first: sources, then warnings. */
export function problems(now: SystemNow): NowService[] {
  const rank: Record<ServiceState, number> = { source: 0, warning: 1, affected: 2, ok: 3 }
  return now.services.filter((s) => s.state === 'source' || s.state === 'warning').sort((a, b) => rank[a.state] - rank[b.state] || a.name.localeCompare(b.name))
}

/** Edges on a failure's path: from an affected service toward a root it suffers from. */
export function failurePath(now: SystemNow): Set<string> {
  const byName = new Map(now.services.map((s) => [s.name, s]))
  const out = new Set<string>()
  for (const s of now.services) {
    if (s.state !== 'affected' || !s.because?.length) continue
    // Walk the dependencies breadth-first to each root and mark the way.
    const roots = new Set(s.because)
    const prev = new Map<string, string>()
    const queue = [s.name]
    const seen = new Set(queue)
    while (queue.length) {
      const n = queue.shift()!
      if (roots.has(n)) {
        for (let cur = n; prev.has(cur); cur = prev.get(cur)!) out.add(`${prev.get(cur)}->${cur}`)
        continue
      }
      for (const d of byName.get(n)?.dependsOn ?? []) {
        if (seen.has(d)) continue
        seen.add(d)
        prev.set(d, n)
        queue.push(d)
      }
    }
  }
  return out
}

// ---- One request ----

export interface Row {
  span: Span
  depth: number
  /** On the way from the entry to a failed step. */
  onErrorPath: boolean
}

/**
 * The spans of a trace as a tree in time order. With `quiet`, the noise is left out: steps
 * shorter than 1% of the request that neither failed nor lead to a failure.
 */
export function rows(spans: Span[], quiet: boolean): Row[] {
  const ids = new Set(spans.map((s) => s.id))
  const children = new Map<string, Span[]>()
  const roots: Span[] = []
  for (const s of spans) {
    if (s.parent && ids.has(s.parent)) {
      const list = children.get(s.parent) ?? []
      list.push(s)
      children.set(s.parent, list)
    } else roots.push(s)
  }
  const byStart = (a: Span, b: Span) => a.startMs - b.startMs
  roots.sort(byStart)
  for (const list of children.values()) list.sort(byStart)
  const errPath = new Set<string>()
  const parentOf = new Map(spans.map((s) => [s.id, s.parent]))
  for (const s of spans) {
    if (!s.error) continue
    for (let cur: string | undefined = s.id; cur && !errPath.has(cur); cur = parentOf.get(cur)) errPath.add(cur)
  }
  const total = Math.max(...spans.map((s) => s.startMs + s.durationMs), 1)
  const out: Row[] = []
  const walk = (s: Span, depth: number) => {
    const keep = !quiet || depth === 0 || errPath.has(s.id) || s.durationMs >= total * 0.01
    if (!keep) return
    out.push({ span: s, depth, onErrorPath: errPath.has(s.id) })
    for (const c of children.get(s.id) ?? []) walk(c, depth + 1)
  }
  for (const r of roots) walk(r, 0)
  return out
}

/** A step longer than this is a session (a load generator's, a stream), not one request. */
export const REQUEST_MAX_MS = 60_000

/**
 * The request a step belongs to, when the trace is a long session of many requests: the step's
 * highest ancestor still the size of a request. Null when the whole trace is one request.
 */
export function requestOf(spans: Span[], focus: string | undefined): Span[] | null {
  const total = Math.max(...spans.map((s) => s.startMs + s.durationMs), 0)
  if (total <= REQUEST_MAX_MS || !focus) return null
  const byId = new Map(spans.map((s) => [s.id, s]))
  let top = byId.get(focus)
  if (!top) return null
  for (let p = top.parent ? byId.get(top.parent) : undefined; p && p.durationMs <= REQUEST_MAX_MS; p = p.parent ? byId.get(p.parent) : undefined) top = p
  const children = new Map<string, Span[]>()
  for (const s of spans) if (s.parent) children.set(s.parent, [...(children.get(s.parent) ?? []), s])
  const out: Span[] = []
  const walk = (s: Span) => {
    out.push({ ...s, parent: s.id === top!.id ? undefined : s.parent, startMs: s.startMs - top!.startMs })
    for (const c of children.get(s.id) ?? []) walk(c)
  }
  walk(top)
  return out
}

/** The deepest failed step: where the request actually broke. */
export function firstFailure(spans: Span[]): Span | undefined {
  const failed = spans.filter((s) => s.error)
  const parents = new Set(failed.map((s) => s.parent))
  return failed.find((s) => !parents.has(s.id)) ?? failed[0]
}

/** Attributes worth reading first, in this order; the rest follow. */
const KEY_ATTRS = ['exception.message', 'exception.type', 'error.type', 'http.response.status_code', 'http.status_code', 'rpc.grpc.status_code', 'http.request.method', 'url.path', 'http.route', 'rpc.method', 'db.statement', 'db.query.text', 'messaging.destination.name']

export function orderAttrs(attrs: Attr[]): { key: Attr[]; rest: Attr[] } {
  const key: Attr[] = []
  for (const k of KEY_ATTRS) {
    const a = attrs.find((x) => x.key === k)
    if (a) key.push(a)
  }
  const appKeys = attrs.filter((a) => a.key.startsWith('app.') && !key.includes(a))
  key.push(...appKeys)
  return { key, rest: attrs.filter((a) => !key.includes(a)) }
}
