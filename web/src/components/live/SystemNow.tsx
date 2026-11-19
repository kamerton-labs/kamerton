// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// The first screen of a system: is everything all right, and if not, which service is the cause
// and which only suffer from it. One sentence on top, the map under it; a service opens its page.

import { useMemo, useState } from 'react'
import { Background, Handle, MarkerType, Position, ReactFlow, ReactFlowProvider, type Edge, type Node, type NodeProps } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { ArrowRight, ChevronRight, X } from 'lucide-react'
import { Link, useNavigate } from 'react-router'
import { cn } from 'cn'
import { Skeleton } from '@/components/ui/skeleton'
import { useT } from '@/i18n'
import { timeAgo } from '@/lib/format'
import { availability, bareTitle, clock, failurePath, percent, problems, rate, duration, useCalls, useSystemNow, windowText, type NowService, type SLO, type SystemNow as Now } from '@/lib/live'
import type { Positions } from '@/lib/model/canvas'

type LiveNodeData = { s: NowService; because: string[] }
type LiveNodeType = Node<LiveNodeData, 'live'>

function LiveNode({ data }: NodeProps<LiveNodeType>) {
  const { t } = useT()
  const s = data.s
  const alert = s.alerts.find((a) => a.severity === 'critical') ?? s.alerts[0]
  return (
    <div
      className={cn(
        'w-[200px] cursor-pointer rounded-lg border bg-card px-3 py-2 text-card-foreground transition-shadow hover:shadow-md',
        s.state === 'source' && 'border-2 border-firing bg-firing/5',
        s.state === 'warning' && 'border-2 border-warn bg-warn/5',
        s.state === 'affected' && 'border-dashed border-warn',
        s.state === 'ok' && 'border-border',
      )}
      data-testid={`live-node-${s.name}`}
      data-state={s.state}
    >
      <Handle type="target" position={Position.Left} className="invisible" />
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate text-sm font-medium" title={s.name}>
          {s.name}
        </span>
        {s.state === 'source' && <span className="rounded bg-firing px-1.5 text-[10px] leading-4 font-semibold text-white uppercase">{t('now.node.source')}</span>}
        {s.state === 'warning' && <span className="rounded bg-warn px-1.5 text-[10px] leading-4 font-semibold text-black uppercase">{t('now.node.warning')}</span>}
      </div>
      <div className="mt-0.5 truncate text-xs text-muted-foreground" title={alert?.title}>
        {(s.state === 'source' || s.state === 'warning') && alert ? (
          <span className={s.state === 'source' ? 'text-firing' : 'text-warn'}>{bareTitle(alert.title, s.name)}</span>
        ) : s.state === 'affected' ? (
          <span className="text-warn">{t('now.node.because', { roots: data.because.join(', ') })}</span>
        ) : s.requests != null ? (
          <>
            {rate(s.requests, t('unit.perSec'), t('unit.perMin'))}
            {s.p95 != null && <> · {duration(s.p95, t('unit.ms'), t('unit.s'))}</>}
            {!!s.errorRatio && <span className="text-warn"> · {percent(s.errorRatio)}</span>}
          </>
        ) : (
          t(`now.type.${s.type}` as 'now.type.http')
        )}
      </div>
      <Handle type="source" position={Position.Right} className="invisible" />
    </div>
  )
}

const nodeTypes = { live: LiveNode }

/** Calls between two services fail when more than this share does: the arrow turns red. */
const EDGE_FAILING = 0.01

function Map({ now, positions, onOpen, onEdge, edge }: { now: Now; positions: Positions; onOpen: (name: string) => void; onEdge: (from: string, to: string) => void; edge: string | null }) {
  const { t } = useT()
  const path = useMemo(() => failurePath(now), [now])
  const calls = useMemo(() => new globalThis.Map(now.edges.map((e) => [`${e.from}->${e.to}`, e])), [now])
  const nodes: LiveNodeType[] = useMemo(
    () =>
      now.services.map((s, i) => ({
        id: s.name,
        type: 'live',
        position: positions[s.name] ?? { x: (i % 4) * 270, y: Math.floor(i / 4) * 110 },
        data: { s, because: s.because ?? [] },
        draggable: false,
        connectable: false,
      })),
    [now, positions],
  )
  const names = new Set(now.services.map((s) => s.name))
  const edges: Edge[] = now.services.flatMap((s) =>
    s.dependsOn
      .filter((d) => names.has(d))
      .map((d) => {
        const id = `${s.name}->${d}`
        const hot = path.has(id)
        const c = calls.get(id)
        const failing = (c?.errorRatio ?? 0) > EDGE_FAILING
        const color = failing ? 'var(--firing)' : hot ? 'var(--warn)' : 'var(--muted-foreground)'
        const picked = edge === id
        return {
          id,
          source: s.name,
          target: d,
          animated: hot,
          interactionWidth: 18,
          label: failing ? t('now.edge.failing', { share: percent(c?.errorRatio) }) : undefined,
          labelStyle: { fill: 'var(--firing)', fontSize: 11, fontWeight: 600 },
          labelBgStyle: { fill: 'var(--background)' },
          markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14, color },
          style: failing || hot || picked ? { stroke: color, strokeWidth: picked ? 3 : 2 } : { stroke: color, strokeOpacity: 0.35, strokeWidth: 1.25 },
        }
      }),
  )
  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      onNodeClick={(_, n) => onOpen(n.id)}
      onEdgeClick={(_, e) => onEdge(e.source, e.target)}
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable={false}
      fitView
      fitViewOptions={{ padding: 0.15 }}
      minZoom={0.3}
      proOptions={{ hideAttribution: true }}
    >
      <Background gap={24} size={1} />
    </ReactFlow>
  )
}

/** The calls from one service to another: how many, how many fail, how long, and the failed ones. */
function CallsPanel({ id, from, to, queue, onClose }: { id: string; from: string; to: string; queue: boolean; onClose: () => void }) {
  const { t } = useT()
  const q = useCalls(id, from, to, !queue)
  const c = q.data?.calls
  const ms = (v: number | null | undefined) => duration(v, t('unit.ms'), t('unit.s'))
  const svcLink = (n: string) => `/systems/${encodeURIComponent(id)}/services/${encodeURIComponent(n)}`
  return (
    <div className="absolute top-3 right-3 z-10 w-[min(360px,calc(100%-1.5rem))] rounded-lg border bg-card p-3 text-sm shadow-lg" data-testid="calls-panel">
      <div className="mb-2 flex items-center gap-1.5">
        <Link to={svcLink(from)} className="font-medium hover:underline">
          {from}
        </Link>
        <ArrowRight className="size-4 text-muted-foreground" />
        <Link to={svcLink(to)} className="font-medium hover:underline">
          {to}
        </Link>
        <button type="button" className="ml-auto rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground" onClick={onClose} aria-label={t('common.close')}>
          <X className="size-4" />
        </button>
      </div>
      {queue ? (
        <p className="text-muted-foreground">
          {t('now.edge.queue')}{' '}
          <Link to={svcLink(to)} className="text-foreground underline">
            {t('now.edge.queueOpen', { name: to })}
          </Link>
        </p>
      ) : q.isError ? (
        <p className="text-destructive">{q.error.message}</p>
      ) : !c ? (
        <Skeleton className="h-16" />
      ) : !c.requests ? (
        <p className="text-muted-foreground">{t('now.edge.noCalls')}</p>
      ) : (
        <>
          <dl className="grid grid-cols-3 gap-2">
            <div>
              <dt className="text-xs text-muted-foreground">{t('now.edge.calls')}</dt>
              <dd className="font-semibold tabular-nums">{rate(c.requests.now, t('unit.perSec'), t('unit.perMin'))}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">{t('svc.errors')}</dt>
              <dd className={cn('font-semibold tabular-nums', (c.errors?.now ?? 0) > EDGE_FAILING && 'text-firing')}>{percent(c.errors?.now)}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted-foreground">p95</dt>
              <dd className="font-semibold tabular-nums">{ms(c.latency?.now)}</dd>
            </div>
          </dl>
          <p className="mt-1 text-xs text-muted-foreground">{t('now.edge.hint', { to })}</p>
        </>
      )}
      {q.data && q.data.failed.length > 0 && (
        <div className="mt-3">
          <h4 className="mb-1 text-xs font-medium text-muted-foreground">{t('now.edge.failed')}</h4>
          <ul className="max-h-48 divide-y overflow-auto">
            {q.data.failed.slice(0, 8).map((h) => (
              <li key={h.traceId}>
                <Link to={`/systems/${encodeURIComponent(id)}/requests/${h.traceId}?from=${encodeURIComponent(to)}${h.spanId ? `&span=${h.spanId}` : ''}`} className="flex items-baseline gap-2 py-1 hover:bg-muted/50">
                  <span className="w-11 shrink-0 text-xs text-muted-foreground tabular-nums">{clock(h.start)}</span>
                  <span className="min-w-0 flex-1 truncate font-mono text-xs">{h.span}</span>
                  <span className="text-xs text-firing tabular-nums">{ms(h.spanMs)}</span>
                  <ChevronRight className="size-3.5 self-center text-muted-foreground" />
                </Link>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

/** One line per failing service: what is wrong, since when, and what it drags along. */
function Headline({ now, id }: { now: Now; id: string }) {
  const { t, tn } = useT()
  const list = problems(now)
  if (!now.alerting)
    return (
      <div className="text-sm text-muted-foreground" data-testid="now-headline">
        <p className="font-medium text-foreground">{t('now.noAlerting')}</p>
        {now.message && <p className="mt-0.5 font-mono text-xs [overflow-wrap:anywhere]">{now.message}</p>}
      </div>
    )
  if (list.length === 0) {
    const affected = now.services.filter((s) => s.state === 'affected')
    return (
      <div data-testid="now-headline">
        <p className="text-lg font-semibold">{t('now.quiet')}</p>
        <p className="text-sm text-muted-foreground">
          {tn('now.quietServices', now.services.length)}
          {affected.length > 0 && ' ' + t('now.quietAffected', { names: affected.map((s) => s.name).join(', ') })}
        </p>
      </div>
    )
  }
  return (
    <ul className="space-y-3" data-testid="now-headline">
      {list.map((s) => {
        const alert = s.alerts.find((a) => a.severity === 'critical') ?? s.alerts[0]
        const dragged = now.services.filter((x) => x.because?.includes(s.name)).map((x) => x.name)
        return (
          <li key={s.name} className="flex flex-wrap items-start gap-x-4 gap-y-2">
            <div className="min-w-0 flex-1">
              <p className="text-lg leading-snug font-semibold">
                <span className={s.state === 'source' ? 'text-firing' : 'text-warn'}>{t(s.state === 'source' ? 'now.failureIn' : 'now.warningIn', { name: s.name })}</span>
                {alert && <span className="font-normal"> — {bareTitle(alert.title, s.name)}</span>}
              </p>
              <p className="mt-0.5 text-sm text-muted-foreground">
                {alert?.value && <span>{alert.value} · </span>}
                {s.since && t('now.since', { ago: timeAgo(s.since) })}
                {dragged.length > 0 && <span> · {t('now.drags', { names: dragged.join(', ') })}</span>}
                {s.alerts.length > 1 && <span> · {tn('now.moreAlerts', s.alerts.length - 1)}</span>}
              </p>
            </div>
            <Link
              to={`/systems/${encodeURIComponent(id)}/services/${encodeURIComponent(s.name)}`}
              className="inline-flex h-8 items-center gap-1 rounded-md bg-foreground px-3 text-sm font-medium text-background hover:opacity-90"
              data-testid={`now-open-${s.name}`}
            >
              {t('now.open', { name: s.name })} <ArrowRight className="size-4" />
            </Link>
          </li>
        )
      })}
    </ul>
  )
}

function Budget({ slo, id }: { slo: SLO; id: string }) {
  const { t, tn } = useT()
  const left = slo.budgetLeft
  const share = left == null ? 0 : Math.max(0, Math.min(1, left))
  const tone = left == null ? 'bg-muted-foreground/40' : left <= 0 ? 'bg-firing' : left < 0.25 ? 'bg-warn' : 'bg-ok'
  return (
    <Link to={`/systems/${encodeURIComponent(id)}/services/${encodeURIComponent(slo.service)}`} className="group flex min-w-[220px] flex-1 flex-col gap-1 rounded-md px-2 py-1.5 hover:bg-muted/60" data-testid={`slo-${slo.service}`}>
      <div className="flex items-baseline justify-between gap-2 text-sm">
        <span className="font-medium group-hover:underline">{slo.service}</span>
        <span className="text-xs text-muted-foreground">{t('now.slo.goal', { objective: String(slo.objective).replace('.', t('decimal')), window: windowText(slo.window, (n) => tn('now.days', n), (n) => tn('now.hours', n)) })}</span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-muted">
        <div className={cn('h-full rounded-full', tone)} style={{ width: `${share * 100}%` }} />
      </div>
      <div className="text-xs text-muted-foreground">
        {slo.availability == null
          ? t('now.slo.noData')
          : left != null && left <= 0
            ? <span className="text-firing">{t('now.slo.spent', { availability: availability(slo.availability) })}</span>
            : t('now.slo.left', { availability: availability(slo.availability), left: percent(left) })}
      </div>
    </Link>
  )
}

/** The system now, inside the system page. */
export default function SystemNow({ id, positions }: { id: string; positions: Positions }) {
  const { t } = useT()
  const now = useSystemNow(id)
  const navigate = useNavigate()
  const [edge, setEdge] = useState<{ from: string; to: string } | null>(null)
  if (now.isLoading) return <Skeleton className="m-4 h-[60svh]" />
  if (now.isError || !now.data) return <p className="p-6 text-sm text-destructive">{now.error?.message ?? t('now.error')}</p>
  const d = now.data
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="space-y-3 border-b px-4 py-4 md:px-6">
        <Headline now={d} id={id} />
        {d.slo.length > 0 && (
          <div className="-mx-2 flex flex-wrap gap-1" data-testid="now-slo">
            {d.slo.map((s) => (
              <Budget key={s.service} slo={s} id={id} />
            ))}
          </div>
        )}
        {!d.metrics && <p className="text-xs text-muted-foreground">{t('now.noMetrics')}</p>}
      </div>
      <div className="relative min-h-[320px] flex-1" data-testid="now-map">
        <ReactFlowProvider>
          <Map
            now={d}
            positions={positions}
            edge={edge ? `${edge.from}->${edge.to}` : null}
            onOpen={(name) => void navigate(`/systems/${encodeURIComponent(id)}/services/${encodeURIComponent(name)}`)}
            onEdge={(from, to) => setEdge({ from, to })}
          />
        </ReactFlowProvider>
        {edge && <CallsPanel id={id} from={edge.from} to={edge.to} queue={d.services.find((s) => s.name === edge.to)?.type === 'queue'} onClose={() => setEdge(null)} />}
        <p className="pointer-events-none absolute bottom-2 left-3 text-xs text-muted-foreground">{d.edges.length > 0 ? t('now.mapHintEdges') : t('now.mapHint')}</p>
      </div>
    </div>
  )
}
