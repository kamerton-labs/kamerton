// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useEffect, useRef } from 'react'
import uPlot from 'uplot'
import { getLang } from '@/i18n'
import 'uplot/dist/uPlot.min.css'

export interface LiveLine {
  label: string
  values: (number | null)[]
  /** Concrete CSS color: canvas cannot resolve var(). */
  color: string
  width?: number
  dash?: number[]
}

interface Props {
  timestamps: number[]
  lines: LiveLine[]
  /** The usual level, drawn as a dashed line to compare with at a glance. */
  usual?: number | null
  /** A line not to cross, such as a memory limit. */
  limit?: number | null
  format: (v: number) => string
  height?: number
  /** A click on the chart picks a moment: unix seconds. */
  onPick?: (ts: number) => void
  /** A picked moment to mark. */
  marked?: number | null
  muted: string
  limitColor: string
}

/** A chart of one signal that starts at zero and always shows the usual level and the limit. */
export default function LiveChart({ timestamps, lines, usual, limit, format, height = 160, onPick, marked, muted, limitColor }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const pick = useRef(onPick)
  useEffect(() => {
    pick.current = onPick
  }, [onPick])

  useEffect(() => {
    const el = host.current
    if (!el) return
    const axis = { stroke: muted, grid: { stroke: 'rgba(128,128,128,0.12)' }, ticks: { show: false }, font: '11px ui-sans-serif, system-ui' }
    const hline = (u: uPlot, v: number, color: string, dash: number[]) => {
      const y = u.valToPos(v, 'y', true)
      const ctx = u.ctx
      ctx.save()
      ctx.strokeStyle = color
      ctx.lineWidth = 1.25 * devicePixelRatio
      ctx.setLineDash(dash.map((d) => d * devicePixelRatio))
      ctx.beginPath()
      ctx.moveTo(u.bbox.left, y)
      ctx.lineTo(u.bbox.left + u.bbox.width, y)
      ctx.stroke()
      ctx.restore()
    }
    const opts: uPlot.Options = {
      width: el.clientWidth || 300,
      height,
      legend: { show: false },
      cursor: { drag: { x: false, y: false }, points: { size: 6 } },
      padding: [8, 4, 0, 0],
      axes: [
        {
          ...axis,
          size: 24,
          values: (_u, splits) => {
            const long = splits.length > 1 && splits[splits.length - 1] - splits[0] > 36 * 3600
            const loc = getLang() === 'ru' ? 'ru-RU' : 'en-GB'
            return splits.map((v) =>
              new Date(v * 1000).toLocaleString(loc, long ? { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' } : { hour: '2-digit', minute: '2-digit' }),
            )
          },
        },
        { ...axis, size: 56, values: (_u, splits) => splits.map((v) => format(v)) },
      ],
      scales: {
        x: { time: true },
        y: {
          range: (_u, _min, max) => {
            const top = Math.max(max || 0, usual ?? 0, limit ?? 0)
            return [0, top > 0 ? top * 1.15 : 1]
          },
        },
      },
      series: [{}, ...lines.map((l) => ({ label: l.label, stroke: l.color, width: l.width ?? 1.5, dash: l.dash, points: { show: false }, spanGaps: false }))],
      hooks: {
        draw: [
          (u) => {
            if (usual != null && usual > 0) hline(u, usual, muted, [4, 4])
            if (limit != null && limit > 0) hline(u, limit, limitColor, [2, 3])
            if (marked != null) {
              const x = u.valToPos(marked, 'x', true)
              const ctx = u.ctx
              ctx.save()
              ctx.fillStyle = 'rgba(128,128,128,0.18)'
              ctx.fillRect(x - 3 * devicePixelRatio, u.bbox.top, 6 * devicePixelRatio, u.bbox.height)
              ctx.restore()
            }
          },
        ],
        ready: [
          (u) => {
            u.over.style.cursor = pick.current ? 'pointer' : 'default'
            u.over.addEventListener('click', () => {
              const idx = u.cursor.idx
              if (idx != null && pick.current) pick.current(timestamps[idx])
            })
          },
        ],
      },
    }
    const plot = new uPlot(opts, [timestamps, ...lines.map((l) => l.values)] as uPlot.AlignedData, el)
    const ro = new ResizeObserver(() => plot.setSize({ width: el.clientWidth, height }))
    ro.observe(el)
    return () => {
      ro.disconnect()
      plot.destroy()
    }
  }, [timestamps, lines, usual, limit, format, height, marked, muted, limitColor])

  return <div ref={host} className="w-full" />
}
