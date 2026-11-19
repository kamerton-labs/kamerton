// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// One request: where it went, where it broke, and what the services wrote while it ran. The
// failed step is picked from the start; small steps that lead nowhere are folded away.

import { useMemo, useState } from 'react'
import { ChevronRight } from 'lucide-react'
import { Link, useParams, useSearchParams } from 'react-router'
import { cn } from 'cn'
import { Skeleton } from '@/components/ui/skeleton'
import { getLang, useT } from '@/i18n'
import { ApiError } from '@/lib/api'
import { duration, firstFailure, orderAttrs, requestOf, rows, useTrace, type LogLine, type Span, type TraceView } from '@/lib/live'

function serviceLink(id: string, t: TraceView, telemetryName: string) {
  const name = t.services[telemetryName]
  return name ? `/systems/${encodeURIComponent(id)}/services/${encodeURIComponent(name)}` : null
}

function Waterfall({ spans, selected, onSelect, quiet }: { spans: Span[]; selected?: string; onSelect: (id: string) => void; quiet: boolean }) {
  const { t } = useT()
  const list = useMemo(() => rows(spans, quiet), [spans, quiet])
  const total = Math.max(...spans.map((s) => s.startMs + s.durationMs), 0.001)
  const ms = (v: number) => duration(v, t('unit.ms'), t('unit.s'))
  return (
    <div className="text-sm" data-testid="waterfall">
      <div className="grid grid-cols-[minmax(0,45%)_1fr] border-b pb-1 text-xs text-muted-foreground">
        <span>{t('req.step')}</span>
        <div className="relative h-4">
          {[0, 0.25, 0.5].map((f) => (
            <span key={f} className="absolute" style={{ left: `${f * 100}%` }}>
              {ms(total * f)}
            </span>
          ))}
          <span className="absolute right-0">{ms(total)}</span>
        </div>
      </div>
      {list.map(({ span: s, depth, onErrorPath }) => (
        <button
          key={s.id}
          type="button"
          onClick={() => onSelect(s.id)}
          className={cn('grid w-full grid-cols-[minmax(0,45%)_1fr] items-center gap-0 border-b border-border/50 py-1 text-left hover:bg-muted/50', selected === s.id && 'bg-muted')}
          data-testid="span-row"
          data-error={s.error || undefined}
        >
          <span className="flex min-w-0 items-baseline gap-1.5 pr-2" style={{ paddingLeft: Math.min(depth, 12) * 12 }}>
            <span className={cn('shrink-0 text-xs', s.error ? 'font-medium text-firing' : 'text-muted-foreground')}>{s.service}</span>
            <span className={cn('truncate font-mono text-xs', onErrorPath && !s.error && 'text-warn')} title={s.name}>
              {s.name}
            </span>
          </span>
          <span className="relative h-4">
            <span
              className={cn('absolute top-1 h-2 rounded-sm', s.error ? 'bg-firing' : onErrorPath ? 'bg-warn/70' : 'bg-foreground/30')}
              style={{ left: `${(s.startMs / total) * 100}%`, width: `max(2px, ${(s.durationMs / total) * 100}%)` }}
            />
            <span className="absolute top-0 text-[10px] text-muted-foreground tabular-nums" style={{ left: `min(calc(${((s.startMs + s.durationMs) / total) * 100}% + 4px), calc(100% - 3rem))` }}>
              {ms(s.durationMs)}
            </span>
          </span>
        </button>
      ))}
    </div>
  )
}

function Details({ id, trace, span, logs }: { id: string; trace: TraceView; span: Span; logs: number }) {
  const { t } = useT()
  const [all, setAll] = useState(false)
  const { key, rest } = orderAttrs(span.attrs)
  const link = serviceLink(id, trace, span.service)
  const exceptions = span.events.filter((e) => e.name === 'exception')
  return (
    <div className="space-y-3 text-sm" data-testid="span-details">
      <div>
        <div className="flex flex-wrap items-baseline gap-x-2">
          {link ? (
            <Link to={link} className="font-medium hover:underline">
              {span.service}
            </Link>
          ) : (
            <span className="font-medium">{span.service}</span>
          )}
          <span className="text-xs text-muted-foreground">
            {span.kind} · {duration(span.durationMs, t('unit.ms'), t('unit.s'))}
          </span>
        </div>
        <p className="font-mono text-xs [overflow-wrap:anywhere]">{span.name}</p>
      </div>
      {span.error && (
        <p className="rounded-md bg-firing/10 px-2 py-1.5 text-firing [overflow-wrap:anywhere]" data-testid="span-error">
          {span.status || t('req.failed')}
        </p>
      )}
      {exceptions.map((e, i) => {
        const msg = e.attrs.find((a) => a.key === 'exception.message')?.value
        const stack = e.attrs.find((a) => a.key === 'exception.stacktrace')?.value
        return (
          <div key={i} className="space-y-1">
            {msg && <p className="font-mono text-xs [overflow-wrap:anywhere]">{msg}</p>}
            {stack && (
              <details>
                <summary className="cursor-pointer text-xs text-muted-foreground">{t('req.stack')}</summary>
                <pre className="mt-1 max-h-64 overflow-auto rounded bg-muted p-2 text-[11px] leading-snug">{stack}</pre>
              </details>
            )}
          </div>
        )
      })}
      {key.length > 0 && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
          {(all ? [...key, ...rest] : key).map((a) => (
            <div key={a.key} className="contents">
              <dt className="text-muted-foreground">{a.key}</dt>
              <dd className="font-mono [overflow-wrap:anywhere]">{a.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {rest.length > 0 && (
        <button type="button" className="text-xs underline underline-offset-2" onClick={() => setAll((v) => !v)}>
          {all ? t('req.lessAttrs') : t('req.allAttrs', { n: String(rest.length) })}
        </button>
      )}
      <p className="text-xs text-muted-foreground">{logs > 0 ? t('req.spanLogs', { n: String(logs) }) : t('req.spanNoLogs')}</p>
    </div>
  )
}

function Logs({ trace, logs, start, selected, onSelect, onlySpan, setOnlySpan }: { trace: TraceView; logs: LogLine[]; start: number; selected?: string; onSelect: (id: string) => void; onlySpan: boolean; setOnlySpan: (v: boolean) => void }) {
  const { t } = useT()
  const list = onlySpan && selected ? logs.filter((l) => l.spanId === selected) : logs
  const tone = (l: LogLine) => (/^(error|fatal|critical)/.test(l.level ?? '') ? 'text-firing' : /^warn/.test(l.level ?? '') ? 'text-warn' : '')
  return (
    <section className="rounded-lg border bg-card" data-testid="trace-logs">
      <div className="flex flex-wrap items-center gap-3 border-b px-3 py-2">
        <h2 className="text-sm font-medium">{t('req.logs', { n: String(logs.length) })}</h2>
        {selected && logs.length > 0 && (
          <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <input type="checkbox" checked={onlySpan} onChange={(e) => setOnlySpan(e.target.checked)} />
            {t('req.onlySpan')}
          </label>
        )}
      </div>
      {trace.logsError ? (
        <p className="px-3 py-3 text-sm text-destructive">{trace.logsError}</p>
      ) : list.length === 0 ? (
        <p className="px-3 py-3 text-sm text-muted-foreground">{logs.length === 0 ? t('req.noLogs') : t('req.noSpanLogs')}</p>
      ) : (
        <ul className="max-h-[50svh] overflow-auto font-mono text-xs">
          {list.map((l, i) => (
            <li key={i}>
              <button
                type="button"
                onClick={() => l.spanId && onSelect(l.spanId)}
                className={cn('grid w-full grid-cols-[4.5rem_8rem_1fr] gap-2 px-3 py-1 text-left hover:bg-muted/50', l.spanId && l.spanId === selected && 'bg-muted', tone(l))}
              >
                <span className="text-muted-foreground tabular-nums">+{duration(Date.parse(l.time) - start, t('unit.ms'), t('unit.s'))}</span>
                <span className="truncate text-muted-foreground">{l.service}</span>
                <span className="[overflow-wrap:anywhere] whitespace-pre-wrap">{l.line}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

export default function RequestPage() {
  const { id = '', trace: traceId = '' } = useParams()
  const [params] = useSearchParams()
  const from = params.get('from')
  const { t, tn } = useT()
  const q = useTrace(id, traceId)
  const [picked, setPicked] = useState<string | undefined>()
  const [quiet, setQuiet] = useState(true)
  const [onlySpan, setOnlySpan] = useState(false)
  const tr = q.data
  // A long session (the load generator's) is shown as the one request the step belongs to.
  const focus = params.get('span') ?? undefined
  const scoped = useMemo(() => (tr ? requestOf(tr.spans, focus ?? firstFailure(tr.spans)?.id) : null), [tr, focus])
  const spans = scoped ?? tr?.spans ?? []
  const ids = new Set(spans.map((s) => s.id))
  const logs = tr ? (scoped ? tr.logs.filter((l) => l.spanId && ids.has(l.spanId)) : tr.logs) : []
  const total = Math.max(...spans.map((s) => s.startMs + s.durationMs), 0)
  const origin = tr ? Date.parse(tr.start) + (scoped ? (tr.spans.find((s) => s.id === scoped[0]?.id)?.startMs ?? 0) : 0) : 0
  const failure = firstFailure(spans)
  const selectedId = picked ?? failure?.id ?? focus ?? spans[0]?.id
  const selected = spans.find((s) => s.id === selectedId)
  const services = new Set(spans.map((s) => s.service)).size
  const shown = rows(spans, quiet).length
  const ms = (v: number) => duration(v, t('unit.ms'), t('unit.s'))
  return (
    <div className="mx-auto w-full max-w-7xl space-y-4 p-4 md:p-6" data-testid="request-page">
      <div className="flex flex-wrap items-center gap-x-2 text-sm text-muted-foreground">
        <Link to={`/systems/${encodeURIComponent(id)}`} className="hover:text-foreground hover:underline">
          {id}
        </Link>
        {from && (
          <>
            <ChevronRight className="size-4" />
            <Link to={`/systems/${encodeURIComponent(id)}/services/${encodeURIComponent(from)}`} className="hover:text-foreground hover:underline">
              {from}
            </Link>
          </>
        )}
        <ChevronRight className="size-4" />
        <span className="font-mono text-xs">{traceId.slice(0, 12)}…</span>
      </div>
      {q.isLoading ? (
        <Skeleton className="h-64" />
      ) : q.isError || !tr ? (
        <p className="text-sm text-destructive">{q.error instanceof ApiError && q.error.status === 404 ? t('req.gone') : q.error?.message}</p>
      ) : (
        <>
          <div>
            <h1 className="text-xl leading-snug font-semibold tracking-tight">
              {failure ? (
                <>
                  <span className="text-firing">{t('req.brokeIn', { service: failure.service })}</span>
                  <span className="font-normal">: </span>
                  <span className="font-mono text-base font-normal">{failure.name}</span>
                </>
              ) : (
                t('req.ok', { d: ms(total) })
              )}
            </h1>
            <p className="mt-0.5 text-sm text-muted-foreground">
              {new Date(origin).toLocaleString(getLang() === 'ru' ? 'ru-RU' : 'en-GB', { dateStyle: 'medium', timeStyle: 'medium' })} · {ms(total)} · {tn('req.services', services)} · {tn('req.steps', spans.length)}
            </p>
            {scoped && <p className="mt-1 text-xs text-muted-foreground">{t('req.partOf', { d: ms(tr.durationMs), n: String(tr.spans.length) })}</p>}
          </div>
          <div className="grid items-start gap-4 lg:grid-cols-[1fr_22rem]">
            <section className="min-w-0 rounded-lg border bg-card p-3">
              <Waterfall spans={spans} selected={selectedId} onSelect={setPicked} quiet={quiet} />
              {shown < spans.length || !quiet ? (
                <button type="button" className="mt-2 text-xs underline underline-offset-2" onClick={() => setQuiet((v) => !v)} data-testid="waterfall-toggle">
                  {quiet ? t('req.showAll', { n: String(spans.length - shown) }) : t('req.showQuiet')}
                </button>
              ) : null}
            </section>
            <aside className="rounded-lg border bg-card p-3 lg:sticky lg:top-4">
              {selected ? <Details id={id} trace={tr} span={selected} logs={logs.filter((l) => l.spanId === selected.id).length} /> : null}
            </aside>
          </div>
          <Logs trace={tr} logs={logs} start={origin} selected={selectedId} onSelect={setPicked} onlySpan={onlySpan} setOnlySpan={setOnlySpan} />
        </>
      )}
    </div>
  )
}
