// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// The system as a picture: services are nodes, "depends on" is an arrow from
// the caller to the service it calls. The same picture shows live state.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Background,
  Controls,
  MarkerType,
  ReactFlow,
  ReactFlowProvider,
  useNodesState,
  useReactFlow,
  type Connection,
  type Edge,
  type NodeChange,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { PanelLeftClose, PanelLeftOpen, Plus } from 'lucide-react'
import { cn } from 'cn'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useT } from '@/i18n'
import { useEditor } from '@/lib/editor'
import { addService, connect, disconnect, moveNodes, positionsOf, toCanvas, uniqueName } from '@/lib/model/canvas'
import { deriveRules, serviceState } from '@/lib/model/rules'
import { NAME_RE, SERVICE_TYPES, type Service, type ServiceType } from '@/lib/model/types'
import { useTheme } from '@/lib/theme'
import DestinationsPanel from './Destinations'
import { DND_TYPE } from './meta'
import { DependencyEdge, ServiceNode, type ServiceNodeType } from './nodes'
import Palette, { type PaletteItem } from './Palette'
import ServicePanel from './ServicePanel'

const nodeTypes = { service: ServiceNode }
const edgeTypes = { dependency: DependencyEdge }

interface Props {
  selected: string | null
  onSelect: (name: string | null) => void
}

/** A palette item dropped on the canvas; anything else dragged in (another tab, a file) is ignored. */
function parsePaletteItem(raw: string): PaletteItem | null {
  if (!raw) return null
  try {
    const v = JSON.parse(raw) as Partial<{ kind: string; type: string; name: string }>
    if (v.kind === 'type' && typeof v.type === 'string' && (SERVICE_TYPES as string[]).includes(v.type)) return { kind: 'type', type: v.type as ServiceType }
    if (v.kind === 'discovered' && typeof v.name === 'string' && v.name) return { kind: 'discovered', name: v.name }
  } catch {
    // Not ours.
  }
  return null
}

function newService(item: PaletteItem, taken: (n: string) => string): Service {
  if (item.kind === 'discovered') {
    const safe = item.name.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 63) || 'service'
    const name = taken(safe)
    return { name, type: 'http', ...(name !== item.name ? { telemetryName: item.name } : {}) }
  }
  switch (item.type) {
    case 'queue': {
      const name = taken('new-queue')
      return { name, type: 'queue', queue: { topic: name, maxLag: 1000 } }
    }
    case 'database':
      return { name: taken('new-database'), type: 'database' }
    default:
      return { name: taken('new-service'), type: item.type }
  }
}

function Canvas({ selected, onSelect }: Props) {
  const { desc, edit, readOnly, states, systemId } = useEditor()
  const { theme } = useTheme()
  const rf = useReactFlow()
  const wrapper = useRef<HTMLDivElement>(null)
  const [paletteWanted, setPaletteOpen] = useState(true)
  // On narrower screens the side panel of a selected service needs the room.
  const paletteOpen = paletteWanted && !(selected && window.innerWidth < 1280)
  const [mobileAdd, setMobileAdd] = useState(false)
  const { t } = useT()

  const ruleCounts = useMemo(() => {
    const m = new Map<string, number>()
    for (const r of deriveRules(desc)) if (r.enabled) m.set(r.service, (m.get(r.service) ?? 0) + 1)
    return m
  }, [desc])

  const present = useMemo(() => new Set(desc.spec.services.flatMap((s) => [s.name, s.telemetryName ?? s.name])), [desc])

  const related = useMemo(() => {
    if (!selected) return null
    const s = desc.spec.services.find((x) => x.name === selected)
    const set = new Set<string>([selected, ...(s?.dependsOn ?? [])])
    for (const x of desc.spec.services) if (x.dependsOn?.includes(selected)) set.add(x.name)
    return set
  }, [desc, selected])

  const derived = useMemo(() => {
    const { nodes, edges } = toCanvas(desc)
    return {
      nodes: nodes.map<ServiceNodeType>((n) => ({
        id: n.id,
        type: 'service',
        position: n.position,
        selected: n.id === selected,
        data: { service: n.data.service, rules: ruleCounts.get(n.id) ?? 0, state: serviceState(n.id, states), dim: !!related && !related.has(n.id) },
      })),
      edges: edges.map<Edge>((e) => ({
        ...e,
        type: 'dependency',
        markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
        data: { onDelete: () => edit((d) => disconnect(d, e.source, e.target)), readOnly },
        animated: serviceState(e.source, states) === 'firing' && serviceState(e.target, states) === 'firing',
        style: related && !(e.source === selected || e.target === selected) ? { opacity: 0.25 } : undefined,
      })),
    }
  }, [desc, selected, ruleCounts, states, related, edit, readOnly])

  const [nodes, setNodes, onNodesChange] = useNodesState<ServiceNodeType>(derived.nodes)
  useEffect(() => setNodes(derived.nodes), [derived.nodes, setNodes])

  // First render: fit once the nodes have been measured.
  const fitted = useRef(false)
  useEffect(() => {
    if (fitted.current || nodes.length === 0) return
    fitted.current = true
    requestAnimationFrame(() => void rf.fitView({ padding: 0.08, maxZoom: 1 }))
  }, [nodes.length, rf])

  // Keep the selected service visible next to its panel.
  useEffect(() => {
    if (!selected || !wrapper.current) return
    const n = rf.getNode(selected)
    if (!n) return
    const { x, y, zoom } = rf.getViewport()
    const width = wrapper.current.clientWidth
    const visible = Math.max(width - 420, width * 0.3)
    const right = (n.position.x + 220) * zoom + x
    const left = n.position.x * zoom + x
    if (right > visible || left < 0) rf.setViewport({ x: x - (right > visible ? right - visible + 24 : left - 24), y, zoom }, { duration: 250 })
  }, [selected, rf])

  const handleNodesChange = useCallback(
    (changes: NodeChange<ServiceNodeType>[]) => {
      // Deleting services happens in the panel, never by a stray key press.
      onNodesChange(changes.filter((c) => c.type !== 'remove' && c.type !== 'select'))
    },
    [onNodesChange],
  )

  const add = useCallback(
    (item: PaletteItem, at?: { x: number; y: number }) => {
      if (readOnly) return
      // A click places the service under the others; a drop, where it was dropped.
      const svc = newService(item, (n) => uniqueName(desc, n))
      if (!NAME_RE.test(svc.name)) return
      const next = addService(desc, svc, at ? { x: at.x - 105, y: at.y - 30 } : undefined)
      edit(() => next)
      onSelect(svc.name)
      if (!at) {
        const p = positionsOf(next)[svc.name]
        if (p) requestAnimationFrame(() => void rf.setCenter(p.x + 105, p.y + 30, { zoom: Math.max(rf.getZoom(), 0.8), duration: 300 }))
      }
    },
    [desc, edit, onSelect, readOnly, rf],
  )

  return (
    <div className="relative flex h-full min-h-0">
      <aside className={cn('hidden shrink-0 border-r bg-sidebar/60 transition-[width] md:block', paletteOpen ? 'w-64' : 'w-10')}>
        <div className="flex items-center justify-between px-2 py-2">
          {paletteOpen && <span className="pl-1 text-sm font-medium">{t('canvas.panel')}</span>}
          <Button variant="ghost" size="icon-sm" onClick={() => setPaletteOpen(!paletteOpen)} aria-label={paletteOpen ? t('canvas.hidePanel') : t('canvas.showPanel')}>
            {paletteOpen ? <PanelLeftClose /> : <PanelLeftOpen />}
          </Button>
        </div>
        {paletteOpen && (
          <div className="h-[calc(100%-44px)] overflow-y-auto px-2 pb-3">
            <DestinationsPanel className="mb-4 w-full" />
            <Palette systemId={systemId} disabled={readOnly} onAdd={(i) => add(i)} present={present} />
            <p className="mt-4 text-xs leading-relaxed text-muted-foreground">
              {t('canvas.paletteHint')}
            </p>
          </div>
        )}
      </aside>

      <div
        ref={wrapper}
        className="relative min-w-0 flex-1"
        onDragOver={(e) => {
          if (e.dataTransfer.types.includes(DND_TYPE)) {
            e.preventDefault()
            e.dataTransfer.dropEffect = 'move'
          }
        }}
        onDrop={(e) => {
          const item = parsePaletteItem(e.dataTransfer.getData(DND_TYPE))
          if (!item) return
          e.preventDefault()
          add(item, rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }))
        }}
      >
        <ReactFlow
          nodes={nodes}
          edges={derived.edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={handleNodesChange}
          onNodeClick={(_, n) => onSelect(n.id)}
          onPaneClick={() => onSelect(null)}
          onNodeDragStop={(_, __, moved) => {
            if (readOnly) return
            edit((d) => moveNodes(d, Object.fromEntries(moved.map((n) => [n.id, n.position]))))
          }}
          onConnect={(c: Connection) => c.source && c.target && edit((d) => connect(d, c.source, c.target))}
          onEdgesDelete={(es) => edit((d) => es.reduce((acc, e) => disconnect(acc, e.source, e.target), d))}
          deleteKeyCode={readOnly ? null : ['Backspace', 'Delete']}
          nodesDraggable={!readOnly}
          nodesConnectable={!readOnly}
          colorMode={theme}
          minZoom={0.2}
          maxZoom={1.5}
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={20} size={1} />
          <Controls showInteractive={false} position="bottom-left" />
        </ReactFlow>

        <Button className="absolute right-3 bottom-3 z-10 shadow-md md:hidden" onClick={() => setMobileAdd(true)} disabled={readOnly}>
          <Plus /> {t('canvas.service')}
        </Button>
        {desc.spec.services.length === 1 && !selected && (
          <div className="pointer-events-none absolute inset-x-0 bottom-16 z-10 mx-auto w-fit max-w-[90%] rounded-md bg-popover/95 px-3 py-2 text-center text-xs text-muted-foreground shadow-sm">
            {t('canvas.emptyHint')}
          </div>
        )}
      </div>

      {/* A side panel on wide screens, a sheet over the canvas on narrow ones. */}
      {selected && (
        <aside className="absolute inset-y-0 right-0 z-20 w-full max-w-[400px] border-l bg-background shadow-lg">
          <ServicePanel key={selected} name={selected} onClose={() => onSelect(null)} onRenamed={(n) => onSelect(n)} />
        </aside>
      )}

      <Dialog open={mobileAdd} onOpenChange={setMobileAdd}>
        <DialogContent className="max-h-[85svh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t('canvas.addTitle')}</DialogTitle>
          </DialogHeader>
          <Palette
            present={present}
            systemId={systemId}
            disabled={readOnly}
            onAdd={(i) => {
              setMobileAdd(false)
              add(i)
            }}
          />
          <DestinationsPanel className="w-full" />
        </DialogContent>
      </Dialog>
    </div>
  )
}

export default function SystemCanvas(props: Props) {
  return (
    <ReactFlowProvider>
      <Canvas {...props} />
    </ReactFlowProvider>
  )
}
