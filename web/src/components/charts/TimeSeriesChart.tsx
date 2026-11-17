// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useEffect, useRef } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'

export interface Series {
  label: string
  /** Concrete CSS color (canvas cannot resolve var()); see cssColor. */
  stroke: string
  fill?: string
  values: (number | null)[]
}

interface Props {
  /** Unix timestamps in seconds. */
  timestamps: number[]
  series: Series[]
  height?: number
  stepped?: boolean
  /** Small chart without legend and y axis. */
  compact?: boolean
}

/** Thin uPlot wrapper that follows the container width. */
export default function TimeSeriesChart({ timestamps, series, height = 200, stepped = false, compact = false }: Props) {
  const host = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = host.current
    if (!el) return
    const axis = { stroke: 'currentColor', grid: { stroke: 'rgba(128,128,128,0.15)' }, ticks: { stroke: 'rgba(128,128,128,0.25)' } }
    const opts: uPlot.Options = {
      width: el.clientWidth || 300,
      height,
      legend: { show: !compact },
      cursor: { show: !compact },
      axes: compact ? [{ ...axis, size: 24, font: '10px sans-serif' }, { show: false }] : [axis, axis],
      scales: { x: { time: true } },
      series: [
        {},
        ...series.map((s) => ({
          label: s.label,
          stroke: s.stroke,
          fill: s.fill,
          width: 1.5,
          paths: stepped ? uPlot.paths.stepped?.({ align: 1 }) : undefined,
        })),
      ],
    }
    const data: uPlot.AlignedData = [timestamps, ...series.map((s) => s.values)]
    const plot = new uPlot(opts, data, el)
    const ro = new ResizeObserver(() => plot.setSize({ width: el.clientWidth, height }))
    ro.observe(el)
    return () => {
      ro.disconnect()
      plot.destroy()
    }
  }, [timestamps, series, height, stepped, compact])

  return <div ref={host} className="w-full text-muted-foreground" />
}
