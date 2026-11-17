// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Description <-> canvas. Services are nodes, dependsOn entries are edges
// (caller -> dependency). Positions live in the metadata annotation
// kamerton.dev/layout: stored with the document but outside spec, so they
// never change what is generated.

import { clone, type Description, type Service } from './types'

export const LAYOUT_ANNOTATION = 'kamerton.dev/layout'

export interface Point {
  x: number
  y: number
}
export type Positions = Record<string, Point>

export interface CanvasNode {
  id: string
  position: Point
  data: { service: Service }
}

export interface CanvasEdge {
  id: string
  source: string
  target: string
}

export const COLUMN_WIDTH = 270
export const ROW_HEIGHT = 110

/** Reads node positions from the layout annotation; invalid entries are ignored. */
export function readLayout(desc: Description): Positions {
  const raw = desc.metadata.annotations?.[LAYOUT_ANNOTATION]
  if (!raw) return {}
  try {
    const v = JSON.parse(raw) as unknown
    if (!v || typeof v !== 'object' || Array.isArray(v)) return {}
    const out: Positions = {}
    for (const [k, p] of Object.entries(v as Record<string, unknown>)) {
      if (Array.isArray(p) && p.length === 2 && p.every((n) => typeof n === 'number' && Number.isFinite(n))) out[k] = { x: p[0], y: p[1] }
    }
    return out
  } catch {
    return {}
  }
}

/** Returns a copy of desc with the positions of its services in the layout annotation. */
export function writeLayout(desc: Description, positions: Positions): Description {
  const next = clone(desc)
  const names = new Set(next.spec.services.map((s) => s.name))
  const compact: Record<string, [number, number]> = {}
  for (const s of next.spec.services) {
    const p = positions[s.name]
    if (p && names.has(s.name)) compact[s.name] = [Math.round(p.x), Math.round(p.y)]
  }
  const annotations = { ...(next.metadata.annotations ?? {}) }
  if (Object.keys(compact).length > 0) annotations[LAYOUT_ANNOTATION] = JSON.stringify(compact)
  else delete annotations[LAYOUT_ANNOTATION]
  if (Object.keys(annotations).length > 0) next.metadata.annotations = annotations
  else delete next.metadata.annotations
  return next
}

/** Layered layout: column is the longest call chain leading to the service. */
export function autoArrange(services: Service[]): Positions {
  const names = new Set(services.map((s) => s.name))
  const callers = new Map<string, string[]>()
  for (const s of services) for (const d of s.dependsOn ?? []) if (names.has(d)) callers.set(d, [...(callers.get(d) ?? []), s.name])
  const depth = new Map<string, number>()
  const visiting = new Set<string>()
  const depthOf = (n: string): number => {
    const known = depth.get(n)
    if (known !== undefined) return known
    if (visiting.has(n)) return 0
    visiting.add(n)
    const ds = (callers.get(n) ?? []).map((c) => depthOf(c) + 1)
    visiting.delete(n)
    const d = ds.length ? Math.max(...ds) : 0
    depth.set(n, d)
    return d
  }
  for (const s of services) depthOf(s.name)
  // Services nobody calls sit just left of what they call, not in the
  // first column (e.g. queue consumers next to their queue).
  for (const s of services) {
    if (callers.has(s.name)) continue
    const targets = (s.dependsOn ?? []).filter((d) => names.has(d)).map((d) => depth.get(d) ?? 0)
    if (targets.length) depth.set(s.name, Math.max(0, Math.min(...targets) - 1))
  }
  const columns = new Map<number, string[]>()
  for (const s of services) {
    const d = depth.get(s.name) ?? 0
    columns.set(d, [...(columns.get(d) ?? []), s.name])
  }
  const out: Positions = {}
  const rowOf = new Map<string, number>()
  for (const d of [...columns.keys()].sort((a, b) => a - b)) {
    const col = columns.get(d)!
    // Order by the average row of callers (barycenter), then by name.
    const key = (n: string) => {
      const rows = (callers.get(n) ?? []).map((c) => rowOf.get(c)).filter((r): r is number => r !== undefined)
      return rows.length ? rows.reduce((a, b) => a + b, 0) / rows.length : Number.MAX_SAFE_INTEGER
    }
    col.sort((a, b) => key(a) - key(b) || a.localeCompare(b))
    col.forEach((n, i) => {
      rowOf.set(n, i)
      out[n] = { x: d * COLUMN_WIDTH, y: i * ROW_HEIGHT }
    })
  }
  return out
}

/** Stored positions, or an automatic layout when none are stored. Services without a position go in a row below. */
export function positionsOf(desc: Description): Positions {
  const stored = readLayout(desc)
  const services = desc.spec.services
  const known = services.filter((s) => stored[s.name])
  if (known.length === 0) return autoArrange(services)
  const out: Positions = {}
  for (const s of known) out[s.name] = stored[s.name]
  const missing = services.filter((s) => !stored[s.name])
  if (missing.length > 0) {
    const maxY = Math.max(...known.map((s) => stored[s.name].y))
    const minX = Math.min(...known.map((s) => stored[s.name].x))
    missing.forEach((s, i) => (out[s.name] = { x: minX + i * COLUMN_WIDTH, y: maxY + ROW_HEIGHT * 1.4 }))
  }
  return out
}

export const edgeId = (from: string, to: string) => `${from}->${to}`

export function toCanvas(desc: Description): { nodes: CanvasNode[]; edges: CanvasEdge[] } {
  const pos = positionsOf(desc)
  const names = new Set(desc.spec.services.map((s) => s.name))
  const nodes = desc.spec.services.map((s) => ({ id: s.name, position: pos[s.name], data: { service: s } }))
  const edges = desc.spec.services.flatMap((s) =>
    (s.dependsOn ?? []).filter((d) => names.has(d)).map((d) => ({ id: edgeId(s.name, d), source: s.name, target: d })),
  )
  return { nodes, edges }
}

/** Applies nodes, edges and positions back to base. Everything else in the description is kept. */
export function fromCanvas(base: Description, nodes: CanvasNode[], edges: CanvasEdge[]): Description {
  const next = clone(base)
  const ids = new Set(nodes.map((n) => n.id))
  const byName = new Map(next.spec.services.map((s) => [s.name, s]))
  const services: Service[] = next.spec.services.filter((s) => ids.has(s.name))
  for (const n of nodes) if (!byName.has(n.id)) services.push(clone(n.data.service))
  const out = new Map<string, string[]>()
  for (const e of edges) {
    if (!ids.has(e.source) || !ids.has(e.target) || e.source === e.target) continue
    const list = out.get(e.source) ?? []
    if (!list.includes(e.target)) list.push(e.target)
    out.set(e.source, list)
  }
  for (const s of services) {
    const wanted = out.get(s.name) ?? []
    // Keep references to services that are not on the canvas (written by
    // hand, possibly invalid) untouched: the canvas cannot show them.
    const hidden = (s.dependsOn ?? []).filter((d) => !ids.has(d) && !byName.has(d))
    const kept = (s.dependsOn ?? []).filter((d) => wanted.includes(d))
    const added = wanted.filter((d) => !kept.includes(d))
    const deps = [...kept, ...added, ...hidden]
    if (deps.length > 0) s.dependsOn = deps
    else delete s.dependsOn
  }
  next.spec.services = services
  const positions: Positions = {}
  for (const n of nodes) positions[n.id] = n.position
  return writeLayout(pruneRemoved(next, base), positions)
}

/** Drops references (routes) to services that no longer exist. */
function pruneRemoved(next: Description, base: Description): Description {
  const names = new Set(next.spec.services.map((s) => s.name))
  const removed = base.spec.services.map((s) => s.name).filter((n) => !names.has(n))
  if (removed.length === 0) return next
  const r = next.spec.notifications?.routing
  if (r?.routes) {
    r.routes = r.routes.map((rt) => ({ ...rt, services: rt.services.filter((s) => !removed.includes(s)) })).filter((rt) => rt.services.length > 0)
    if (r.routes.length === 0) delete r.routes
  }
  return next
}

export function addService(desc: Description, service: Service, at?: Point): Description {
  const next = clone(desc)
  // Keep everything where the person sees it now; a new service goes where
  // it was dropped, or under the others.
  const pos = positionsOf(desc)
  next.spec.services.push(service)
  const all = Object.values(pos)
  pos[service.name] = at ?? (all.length ? { x: Math.min(...all.map((p) => p.x)), y: Math.max(...all.map((p) => p.y)) + ROW_HEIGHT * 1.4 } : { x: 0, y: 0 })
  return writeLayout(next, pos)
}

export function removeService(desc: Description, name: string): Description {
  const { nodes, edges } = toCanvas(desc)
  return fromCanvas(
    desc,
    nodes.filter((n) => n.id !== name),
    edges.filter((e) => e.source !== name && e.target !== name),
  )
}

export function connect(desc: Description, from: string, to: string): Description {
  if (from === to) return desc
  const { nodes, edges } = toCanvas(desc)
  if (edges.some((e) => e.source === from && e.target === to)) return desc
  return fromCanvas(desc, nodes, [...edges, { id: edgeId(from, to), source: from, target: to }])
}

export function disconnect(desc: Description, from: string, to: string): Description {
  const { nodes, edges } = toCanvas(desc)
  return fromCanvas(desc, nodes, edges.filter((e) => !(e.source === from && e.target === to)))
}

export function moveNodes(desc: Description, moved: Positions): Description {
  return writeLayout(desc, { ...positionsOf(desc), ...moved })
}

/** Renames a service everywhere it is referenced. */
export function renameService(desc: Description, from: string, to: string): Description {
  if (from === to) return desc
  const pos = positionsOf(desc)
  const next = clone(desc)
  for (const s of next.spec.services) {
    if (s.name === from) s.name = to
    if (s.dependsOn) s.dependsOn = s.dependsOn.map((d) => (d === from ? to : d))
  }
  for (const rt of next.spec.notifications?.routing?.routes ?? []) rt.services = rt.services.map((s) => (s === from ? to : s))
  if (pos[from]) {
    pos[to] = pos[from]
    delete pos[from]
  }
  return writeLayout(next, pos)
}

export function updateService(desc: Description, name: string, fn: (s: Service) => void): Description {
  const next = clone(desc)
  const s = next.spec.services.find((x) => x.name === name)
  if (s) fn(s)
  return next
}

/** A name not used by any service: base, base-2, base-3, … */
export function uniqueName(desc: Description, base: string): string {
  const taken = new Set(desc.spec.services.map((s) => s.name))
  if (!taken.has(base)) return base
  for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`
}
