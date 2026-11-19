// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// One service: what is wrong with it (said first), its numbers over time against the usual, and
// the way down to the requests behind a spike. A click on a chart lists the requests of those
// minutes; a request opens with its trace and logs.

import { useMemo, useState, type ReactNode } from 'react'
import { ChevronRight, Loader2 } from 'lucide-react'
import { Link, useParams, useSearchParams } from 'react-router'
import { cn } from 'cn'
import LiveChart, { type LiveLine } from '@/components/live/LiveChart'
import { Skeleton } from '@/components/ui/skeleton'
import { useT } from '@/i18n'
import { cssColor, timeAgo } from '@/lib/format'
import {
  RANGES,
  bareTitle,
  bytes,
  clock,
  cores,
  lastOf,
  maxOf,
  rateAxis,
  duration,
  number,
  percent,
  rate,
  useChanges,
  useServiceRequests,
  useServiceSeries,
  useSystemNow,
  type Chart,
  type NowService,
  type Range,
  type RequestQuery,
  type ServiceSeries,
} from '@/lib/live'
import { useTheme } from '@/lib/theme'

const PALETTE = ['#4f7fd9', '#c98a2b', '#5f9a5a', '#a06bb8', '#3f9fa3', '#c4596a']

function useColors() {
  const { theme } = useTheme()
  return useMemo(
    () => ({
      fg: cssColor('--foreground', '#222'),
      muted: cssColor('--muted-foreground', '#888'),
      firing: cssColor('--firing', '#d33'),
      warn: cssColor('--warn', '#d90'),
    }),
    // The colors follow the theme.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [theme],
  )
}

function Card({ title, value, usual, extra, children, testid }: { title: string; value?: ReactNode; usual?: ReactNode; extra?: ReactNode; children: ReactNode; testid?: string }) {
  return (
    <section className="rounded-lg border bg-card p-3" data-testid={testid}>
      <div className="mb-1 flex flex-wrap items-baseline gap-x-2">
        <h3 className="text-sm text-muted-foreground">{title}</h3>
        {value != null && <span className="text-base font-semibold tabular-nums">{value}</span>}
        {usual && <span className="text-xs text-muted-foreground">{usual}</span>}
        {extra && <span className="ml-auto text-xs text-muted-foreground">{extra}</span>}
      </div>
      {children}
    </section>
  )
}

/** Which line is which, when a chart has several: pods, consumer groups. */
function Legend({ chart }: { chart?: Chart }) {
  if (!chart || chart.lines.length < 2) return null
  return (
    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
      {chart.lines.slice(0, 8).map((l, i) => (
        <span key={l.label} className="inline-flex items-center gap-1">
          <span className="inline-block h-0.5 w-3 rounded" style={{ background: PALETTE[i % PALETTE.length] }} />
          {l.label}
        </span>
      ))}
    </div>
  )
}

/** "обычно 12 мс", or a warning when now is far off the usual. */
function Usual({ chart, fmt, worseUp = true }: { chart?: Chart; fmt: (v: number) => string; worseUp?: boolean }) {
  const { t } = useT()
  if (!chart || chart.usual == null) return null
  const off = chart.now != null && chart.usual > 0 && (worseUp ? chart.now > chart.usual * 2 : chart.now < chart.usual / 2)
  return <span className={off ? 'text-warn' : undefined}>{t('svc.usual', { v: fmt(chart.usual) })}</span>
}

function Charts({ s, onPick, marked }: { s: ServiceSeries; onPick: (ts: number) => void; marked: number | null }) {
  const { t, tn } = useT()
  const c = useColors()
  const ms = (v: number) => duration(v, t('unit.ms'), t('unit.s'))
  const rps = (v: number) => rate(v, t('unit.perSec'), t('unit.perMin'))
  const one = (chart: Chart | undefined, color: string): LiveLine[] => (chart?.lines ?? []).map((l) => ({ label: l.label, values: l.values, color, width: 1.75 }))
  const many = (chart: Chart | undefined): LiveLine[] => (chart?.lines ?? []).map((l, i) => ({ label: l.label, values: l.values, color: PALETTE[i % PALETTE.length] }))
  const latency: LiveLine[] = (s.latency?.lines ?? []).map((l) => ({
    label: l.label,
    values: l.values,
    color: l.label === 'p95' ? c.fg : c.muted,
    width: l.label === 'p95' ? 1.75 : 1,
    dash: l.label === 'p99' ? [3, 3] : undefined,
  }))
  const common = { timestamps: s.timestamps, onPick, marked, muted: c.muted, limitColor: c.firing }
  const errNow = s.errors?.now
  const items: ReactNode[] = []
  const rpsAxis = useMemo(() => rateAxis(maxOf(s.requests?.lines ?? []), t('unit.perSec'), t('unit.perMin')), [s.requests, t])
  // A flat zero says nothing a sentence cannot.
  const noErrors = !!s.errors && maxOf(s.errors.lines) === 0 && !s.errors.usual
  if (s.errors && noErrors)
    items.push(
      <Card key="err" testid="chart-errors" title={t('svc.errors')} value="0%" extra={t('svc.noErrors')}>
        {null}
      </Card>,
    )
  else if (s.errors)
    items.push(
      <Card key="err" testid="chart-errors" title={t('svc.errors')} value={<span className={errNow ? 'text-firing' : undefined}>{percent(errNow)}</span>} usual={<Usual chart={s.errors} fmt={percent} />}>
        <LiveChart {...common} lines={one(s.errors, c.firing)} usual={s.errors.usual} format={percent} />
      </Card>,
    )
  if (s.latency)
    items.push(
      <Card key="lat" testid="chart-latency" title={t('svc.latency')} value={ms(s.latency.now ?? NaN)} usual={<Usual chart={s.latency} fmt={ms} />} extra={t('svc.latencyLegend')}>
        <LiveChart {...common} lines={latency} usual={s.latency.usual} format={ms} />
      </Card>,
    )
  if (s.requests)
    items.push(
      <Card key="req" testid="chart-requests" title={t('svc.requests')} value={rps(s.requests.now ?? NaN)} usual={<Usual chart={s.requests} fmt={rps} worseUp={false} />}>
        <LiveChart {...common} lines={one(s.requests, c.fg)} usual={s.requests.usual} format={rpsAxis} />
      </Card>,
    )
  if (s.queue)
    items.push(
      <Card key="queue" testid="chart-queue" title={t('svc.queueLag')} value={number(s.queue.now, 0)}>
        <LiveChart {...common} lines={many(s.queue)} format={(v) => number(v, 0)} />
        <Legend chart={s.queue} />
      </Card>,
    )
  if (s.memory)
    items.push(
      <Card
        key="mem"
        testid="chart-memory"
        title={t('svc.memory')}
        value={bytes(Math.max(...s.memory.lines.map((l) => [...l.values].reverse().find((v) => v != null) ?? 0)))}
        usual={s.memory.limit ? t('svc.limit', { v: bytes(s.memory.limit) }) : undefined}
        extra={s.restarts ? <span className="text-warn">{tn('svc.restarts', Math.round(s.restarts))}</span> : tn('svc.pods', s.memory.lines.length)}
      >
        <LiveChart {...common} lines={many(s.memory)} limit={s.memory.limit} format={bytes} />
        <Legend chart={s.memory} />
      </Card>,
    )
  if (s.cpu)
    items.push(
      <Card key="cpu" testid="chart-cpu" title={t('svc.cpu')} value={cores(Math.max(...s.cpu.lines.map((l) => lastOf(l.values) ?? 0)))}>
        <LiveChart {...common} lines={many(s.cpu)} format={cores} />
        <Legend chart={s.cpu} />
      </Card>,
    )
  if (items.length === 0) return <p className="text-sm text-muted-foreground">{t('svc.noSignals')}</p>
  return <div className="grid items-start gap-3 lg:grid-cols-2">{items}</div>
}

/** What is wrong, in words, before any chart. */
function Status({ svc, id }: { svc?: NowService; id: string }) {
  const { t } = useT()
  if (!svc) return null
  if (svc.state === 'affected')
    return (
      <div className="rounded-lg border border-dashed border-warn bg-warn/5 px-4 py-3 text-sm" data-testid="svc-status">
        <p className="font-medium">
          {t('svc.affected')}{' '}
          {(svc.because ?? []).map((r, i) => (
            <span key={r}>
              {i > 0 && ', '}
              <Link className="underline" to={`/systems/${encodeURIComponent(id)}/services/${encodeURIComponent(r)}`}>
                {r}
              </Link>
            </span>
          ))}
        </p>
        <p className="mt-0.5 text-muted-foreground">{t('svc.affectedHint')}</p>
      </div>
    )
  if (svc.alerts.length === 0) return null
  return (
    <ul className={cn('space-y-1 rounded-lg border px-4 py-3', svc.state === 'source' ? 'border-firing/60 bg-firing/5' : 'border-warn/60 bg-warn/5')} data-testid="svc-status">
      {svc.alerts.map((a) => (
        <li key={a.name + a.startsAt} className="text-sm">
          <span className={cn('font-medium', a.severity === 'critical' ? 'text-firing' : 'text-warn')}>{bareTitle(a.title, svc.name)}</span>
          {a.value && <span> — {a.value}</span>}
          <span className="text-muted-foreground"> · {t('now.since', { ago: timeAgo(a.startsAt) })}</span>
        </li>
      ))}
    </ul>
  )
}

/** What changed in the pods lately: a rollout or a restart next to a failure is the first suspect. */
function Changes({ id, service }: { id: string; service: string }) {
  const { t, tn } = useT()
  const q = useChanges(id, service)
  const d = q.data
  if (!d?.available) return null
  if (d.error) return <p className="text-xs text-muted-foreground">{t('svc.changes.error', { error: d.error })}</p>
  if (d.changes.length === 0)
    return (
      <p className="text-xs text-muted-foreground" data-testid="svc-changes">
        {d.noPods ? t('svc.changes.noPods') : t('svc.changes.none')}
        {!!d.notReady && <span className="text-warn"> · {tn('svc.changes.notReady', d.notReady)}</span>}
      </p>
    )
  const line = (c: (typeof d.changes)[number]) => {
    switch (c.kind) {
      case 'rollout':
        return t(c.from ? 'svc.changes.rollout' : 'svc.changes.rolloutFirst', { name: c.subject, from: c.from ?? '', to: c.to ?? '' })
      case 'scaling':
        return t('svc.changes.scaling', { name: c.subject, from: c.from ?? '', to: c.to ?? '' })
      case 'restart':
        return tn('svc.changes.restart', c.count ?? 1, { name: c.subject }) + (c.reason ? ` (${c.reason})` : '')
      default:
        return `${c.subject}: ${c.text ?? ''}`
    }
  }
  return (
    <section className="rounded-lg border bg-card px-3 py-2 text-sm" data-testid="svc-changes">
      <h3 className="mb-1 text-xs text-muted-foreground">{t('svc.changes.title')}</h3>
      <ul className="space-y-0.5">
        {d.changes.map((c, i) => (
          <li key={i} className={cn(c.kind === 'restart' || c.kind === 'warning' ? 'text-warn' : undefined)}>
            {c.at && <span className="mr-2 text-xs text-muted-foreground tabular-nums">{clock(c.at)}</span>}
            {line(c)}
          </li>
        ))}
      </ul>
    </section>
  )
}

function Requests({ id, service, q, setQ, s, median }: { id: string; service: string; q: RequestQuery; setQ: (q: RequestQuery) => void; s: ServiceSeries; median: number | null }) {
  const { t } = useT()
  const list = useServiceRequests(id, service, q)
  const ms = (v: number) => duration(v, t('unit.ms'), t('unit.s'))
  const whole = q.start <= Date.parse(s.start) / 1000 + 1
  const tab = (kind: 'errors' | 'slow', label: string, disabled?: boolean) => (
    <button
      type="button"
      disabled={disabled}
      aria-pressed={q.kind === kind}
      onClick={() => setQ({ ...q, kind, over: kind === 'slow' ? (median ?? undefined) : undefined })}
      className={cn('rounded-md px-2.5 py-1 text-sm disabled:opacity-40', q.kind === kind ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground hover:text-foreground')}
    >
      {label}
    </button>
  )
  return (
    <section className="rounded-lg border bg-card" data-testid="svc-requests">
      <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
        <h3 className="text-sm font-medium">{t('svc.req.title')}</h3>
        <div className="flex gap-0.5 rounded-lg bg-muted p-0.5">
          {tab('errors', t('svc.req.errors'))}
          {tab('slow', t('svc.req.slow'), !median)}
        </div>
        <span className="text-xs text-muted-foreground">
          {whole ? t('svc.req.whole') : t('svc.req.window', { from: clock(q.start), to: clock(q.end) })}
          {q.operation && <> · <span className="font-mono">{q.operation}</span></>}
        </span>
        {(!whole || q.operation) && (
          <button type="button" className="text-xs underline underline-offset-2" onClick={() => setQ({ ...q, start: Date.parse(s.start) / 1000, end: Date.parse(s.end) / 1000 + s.step, operation: undefined })}>
            {t('svc.req.reset')}
          </button>
        )}
        {list.isFetching && <Loader2 className="size-4 animate-spin text-muted-foreground" />}
      </div>
      {list.isError ? (
        <p className="px-3 py-3 text-sm text-destructive">{list.error.message}</p>
      ) : !list.data ? (
        <Skeleton className="m-3 h-20" />
      ) : list.data.requests.length === 0 ? (
        <p className="px-3 py-4 text-sm text-muted-foreground">{t(q.kind === 'errors' ? 'svc.req.noErrors' : 'svc.req.noSlow')}</p>
      ) : (
        <ul className="divide-y">
          {list.data.requests.map((h) => (
            <li key={h.traceId}>
              <Link
                to={`/systems/${encodeURIComponent(id)}/requests/${h.traceId}?from=${encodeURIComponent(service)}${h.spanId ? `&span=${h.spanId}` : ''}`}
                className="flex flex-wrap items-baseline gap-x-3 px-3 py-2 text-sm hover:bg-muted/50"
                data-testid="svc-request"
              >
                <span className="w-12 shrink-0 text-xs text-muted-foreground tabular-nums">{clock(h.start)}</span>
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{h.span ?? h.rootName ?? h.traceId}</span>
                <span className={cn('text-xs tabular-nums', q.kind === 'errors' ? 'text-firing' : 'text-warn')}>{ms(h.spanMs ?? h.durationMs)}</span>
                {h.rootService && h.rootService !== service && <span className="text-xs text-muted-foreground">{t('svc.req.from', { name: h.rootService })}</span>}
                <ChevronRight className="size-4 self-center text-muted-foreground" />
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function Operations({ s, active, onPick }: { s: ServiceSeries; active?: string; onPick: (op: string) => void }) {
  const { t } = useT()
  if (s.operations.length === 0) return null
  const ms = (v: number | null) => duration(v, t('unit.ms'), t('unit.s'))
  return (
    <section className="rounded-lg border bg-card" data-testid="svc-operations">
      <h3 className="border-b px-3 py-2 text-sm font-medium">{t('svc.ops.title')}</h3>
      <table className="w-full text-sm">
        <thead className="text-xs text-muted-foreground">
          <tr>
            <th className="px-3 py-1.5 text-left font-normal">{t('svc.ops.name')}</th>
            <th className="px-3 py-1.5 text-right font-normal">{t('svc.requests')}</th>
            <th className="px-3 py-1.5 text-right font-normal">{t('svc.errors')}</th>
            <th className="px-3 py-1.5 text-right font-normal">p95</th>
          </tr>
        </thead>
        <tbody>
          {s.operations.slice(0, 10).map((o) => (
            <tr key={o.name} className={cn('cursor-pointer border-t hover:bg-muted/50', active === o.name && 'bg-muted')} onClick={() => onPick(o.name)}>
              <td className="max-w-0 truncate px-3 py-1.5 font-mono text-xs" title={o.name}>
                {o.name}
              </td>
              <td className="px-3 py-1.5 text-right tabular-nums">{rate(o.requests, t('unit.perSec'), t('unit.perMin'))}</td>
              <td className={cn('px-3 py-1.5 text-right tabular-nums', o.errorRatio ? 'text-firing' : 'text-muted-foreground')}>{percent(o.errorRatio)}</td>
              <td className="px-3 py-1.5 text-right tabular-nums">{ms(o.p95)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  )
}

function Links({ id, svc, all }: { id: string; svc?: NowService; all: NowService[] }) {
  const { t } = useT()
  if (!svc) return null
  const byName = new Map(all.map((s) => [s.name, s]))
  const calls = svc.dependsOn.map((n) => byName.get(n)).filter((x): x is NowService => !!x)
  const callers = all.filter((s) => s.dependsOn.includes(svc.name))
  if (calls.length === 0 && callers.length === 0) return null
  const dot = (s: NowService) => (s.state === 'source' ? 'bg-firing' : s.state === 'ok' ? 'bg-ok' : 'bg-warn')
  const list = (items: NowService[]) =>
    items.map((s) => (
      <Link key={s.name} to={`/systems/${encodeURIComponent(id)}/services/${encodeURIComponent(s.name)}`} className="inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-sm hover:bg-muted/60">
        <span className={cn('size-2 rounded-full', dot(s))} />
        {s.name}
      </Link>
    ))
  return (
    <section className="space-y-2 rounded-lg border bg-card px-3 py-2.5 text-sm" data-testid="svc-links">
      {calls.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="mr-1 text-muted-foreground">{t('svc.calls')}</span>
          {list(calls)}
        </div>
      )}
      {callers.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="mr-1 text-muted-foreground">{t('svc.calledBy')}</span>
          {list(callers)}
        </div>
      )}
    </section>
  )
}

export default function ServicePage() {
  const { id = '', service = '' } = useParams()
  const { t } = useT()
  const [params, setParams] = useSearchParams()
  const range = (RANGES as readonly string[]).includes(params.get('range') ?? '') ? (params.get('range') as Range) : '1h'
  const series = useServiceSeries(id, service, range)
  const now = useSystemNow(id)
  const svc = now.data?.services.find((s) => s.name === service)
  const s = series.data
  const [picked, setPicked] = useState<{ q: RequestQuery; at: number | null } | null>(null)

  // The requests shown follow the range until a moment or an operation is picked.
  // Slow means slower than the median of the range: Tempo is asked for those, the slowest kept.
  const median = s ? (lastOf(s.latency?.lines.find((l) => l.label === 'p50')?.values) ?? null) : null
  const failed = s?.errors?.lines[0]?.values.some((v) => !!v) ?? false
  const defaultQ: RequestQuery | null = s
    ? {
        kind: failed || !median ? 'errors' : 'slow',
        start: Date.parse(s.start) / 1000,
        end: Date.parse(s.end) / 1000 + s.step,
        over: failed || !median ? undefined : median,
      }
    : null
  const q = picked?.q ?? defaultQ
  const pickMoment = (ts: number) => {
    if (!s || !q) return
    const half = Math.max(s.step * 2, 90)
    setPicked({ q: { ...q, start: ts - half, end: ts + half }, at: ts })
  }

  return (
    <div className="mx-auto w-full max-w-6xl space-y-4 p-4 md:p-6" data-testid="service-page">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-2">
        <Link to={`/systems/${encodeURIComponent(id)}`} className="text-sm text-muted-foreground hover:text-foreground hover:underline">
          {id}
        </Link>
        <ChevronRight className="size-4 text-muted-foreground" />
        <h1 className="text-xl font-semibold tracking-tight">{service}</h1>
        {svc && svc.state !== 'ok' && (
          <span className={cn('rounded px-1.5 text-xs leading-5 font-medium', svc.state === 'source' ? 'bg-firing text-white' : 'bg-warn/20 text-warn')}>{t(`now.state.${svc.state}`)}</span>
        )}
        <div className="ml-auto flex gap-0.5 rounded-lg bg-muted p-0.5" data-testid="svc-range">
          {RANGES.map((r) => (
            <button
              key={r}
              type="button"
              aria-pressed={range === r}
              onClick={() => (setPicked(null), setParams((p) => (p.set('range', r), p), { replace: true }))}
              className={cn('rounded-md px-2.5 py-1 text-sm', range === r ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground hover:text-foreground')}
            >
              {t(`range.${r}`)}
            </button>
          ))}
        </div>
      </div>
      <Status svc={svc} id={id} />
      <Changes id={id} service={service} />
      {series.isError ? (
        <p className="text-sm text-destructive">{series.error.message}</p>
      ) : !s ? (
        <div className="grid gap-3 lg:grid-cols-2">
          <Skeleton className="h-48" />
          <Skeleton className="h-48" />
        </div>
      ) : (
        <>
          <Charts s={s} onPick={pickMoment} marked={picked?.at ?? null} />
          {s.requests && <p className="-mt-1 text-xs text-muted-foreground">{t('svc.pickHint')}</p>}
          <div className="grid items-start gap-3 lg:grid-cols-[3fr_2fr]">
            {!s.requests ? (
              <div />
            ) : now.data?.traces !== false && q ? (
              <Requests id={id} service={service} q={q} setQ={(nq) => setPicked({ q: nq, at: picked?.at ?? null })} s={s} median={median} />
            ) : (
              <p className="text-sm text-muted-foreground">{t('svc.noTraces')}</p>
            )}
            <div className="space-y-3">
              <Operations s={s} active={q?.operation} onPick={(op) => q && setPicked({ q: { ...q, operation: q.operation === op ? undefined : op }, at: picked?.at ?? null })} />
              <Links id={id} svc={svc} all={now.data?.services ?? []} />
            </div>
          </div>
        </>
      )}
    </div>
  )
}
