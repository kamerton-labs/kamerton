// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { BaseEdge, EdgeLabelRenderer, getBezierPath, Handle, Position, type EdgeProps, type Node, type NodeProps } from '@xyflow/react'
import { Globe, X } from 'lucide-react'
import { cn } from 'cn'
import { StateDot } from '@/components/common'
import { useT } from '@/i18n'
import type { RuleState } from '@/lib/model/rules'
import type { Service } from '@/lib/model/types'
import { typeIcon, typeShort } from './meta'

export type ServiceNodeData = {
  service: Service
  rules: number
  state: RuleState
  /** Dimmed while another service is selected and this one is unrelated. */
  dim?: boolean
}
export type ServiceNodeType = Node<ServiceNodeData, 'service'>

const STATE_TEXT = { pending: 'node.state.pending', warning: 'node.state.warning', firing: 'node.state.firing' } as const

export function ServiceNode({ data, selected }: NodeProps<ServiceNodeType>) {
  const s = data.service
  const { t, tn } = useT()
  const Icon = typeIcon[s.type] ?? Globe
  return (
    <div
      className={cn(
        'group w-[210px] rounded-lg border bg-card px-3 py-2 text-card-foreground shadow-xs transition-[opacity,box-shadow]',
        selected && 'ring-2 ring-primary/70',
        data.state === 'firing' && 'border-firing/70 bg-firing/5',
        (data.state === 'pending' || data.state === 'warning') && 'border-warn/70',
        data.state === 'warning' && 'bg-warn/5',
        data.dim && 'opacity-40',
      )}
    >
      <Handle type="target" position={Position.Left} className="size-2.5! border-2! bg-background!" />
      <div className="flex items-center gap-1.5">
        <StateDot state={data.state} />
        <span className="min-w-0 flex-1 truncate text-sm font-medium" title={s.name}>
          {s.name}
        </span>
        {s.critical && <span className="rounded bg-foreground/5 px-1 text-[10px] font-medium text-muted-foreground">{t('node.critical')}</span>}
      </div>
      <div className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
        <Icon className="size-3.5" />
        <span>{typeShort(s.type)}</span>
        <span>·</span>
        <span>{tn('count.rules', data.rules)}</span>
      </div>
      {(data.state === 'firing' || data.state === 'pending' || data.state === 'warning') && (
        <div className={cn('mt-1 text-xs font-medium', data.state === 'firing' ? 'text-firing' : 'text-warn')}>{t(STATE_TEXT[data.state])}</div>
      )}
      <Handle
        type="source"
        position={Position.Right}
        className="size-3! border-2! border-primary/60! bg-background! transition-transform group-hover:scale-125"
        title={t('node.handle')}
      />
    </div>
  )
}

export type DependencyEdgeData = { onDelete?: () => void; readOnly?: boolean }

export function DependencyEdge({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, markerEnd, selected, data, style }: EdgeProps) {
  const [path, labelX, labelY] = getBezierPath({ sourceX, sourceY, sourcePosition, targetX, targetY, targetPosition })
  const d = data as DependencyEdgeData | undefined
  const { t } = useT()
  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} style={{ ...style, strokeWidth: selected ? 2.5 : 1.25 }} />
      {selected && !d?.readOnly && (
        <EdgeLabelRenderer>
          <button
            type="button"
            className="nodrag nopan pointer-events-auto absolute flex items-center gap-1 rounded-md border bg-popover px-1.5 py-0.5 text-xs shadow-sm hover:bg-destructive/10 hover:text-destructive"
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
            onClick={() => d?.onDelete?.()}
          >
            <X className="size-3" /> {t('node.deleteEdge')}
          </button>
        </EdgeLabelRenderer>
      )}
    </>
  )
}
