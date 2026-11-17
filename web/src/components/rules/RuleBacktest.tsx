// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// How often a rule would have fired over the history in Prometheus, for the
// threshold now in the draft.

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { History, Loader2 } from 'lucide-react'
import TimeSeriesChart from '@/components/charts/TimeSeriesChart'
import { ApiError, backtestRule, type RuleBacktestResult } from '@/lib/api'
import { useDebounced, useEditor } from '@/lib/editor'
import { useT } from '@/i18n'
import { cssColor } from '@/lib/format'
import { setRule, type RuleItem } from '@/lib/model/rules'
import { writeDescriptionYaml } from '@/lib/model/yaml'
import type { Description } from '@/lib/model/types'

const RANGES = [
  { v: '1d', label: 'backtest.range.1d' },
  { v: '3d', label: 'backtest.range.3d' },
  { v: '7d', label: 'backtest.range.7d' },
] as const

interface Props {
  rule: RuleItem
  /** Description to test against (defaults to the editor draft). */
  desc?: Description
}

export default function RuleBacktest({ rule, desc: override }: Props) {
  const { systemId, draft, desc } = useEditor()
  const [range, setRange] = useState<(typeof RANGES)[number]['v']>('3d')
  const { t } = useT()
  const base = override ?? desc
  // The rule is tested as if it were on, so a person can try a threshold
  // before turning it on.
  const yaml = useMemo(() => {
    const d = rule.enabled ? base : setRule(base, rule.id, { enabled: true })
    try {
      return writeDescriptionYaml(draft.error ? '' : draft.text, d)
    } catch {
      return ''
    }
  }, [base, rule.enabled, rule.id, draft.text, draft.error])
  const key = useDebounced(JSON.stringify([rule.id, rule.threshold, rule.for, rule.custom ?? null, range]), 700)
  const q = useQuery<RuleBacktestResult, Error>({
    queryKey: ['backtest', systemId, key],
    queryFn: () =>
      backtestRule(systemId, { yaml, service: rule.service, alert: rule.alert, operation: rule.alertOperation || undefined, range, step: '1m' }),
    enabled: yaml !== '',
    retry: false,
    staleTime: 5 * 60_000,
  })
  const rangeLabel = t(RANGES.find((r) => r.v === range)!.label)

  return (
    <div className="space-y-2 rounded-md border bg-muted/30 p-2.5">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <History className="size-3.5" /> {t('backtest.title')}
        </div>
        <div className="flex gap-0.5 rounded-md bg-muted p-0.5" role="group" aria-label={t('backtest.period')}>
          {RANGES.map((r) => (
            <button
              key={r.v}
              type="button"
              aria-pressed={range === r.v}
              onClick={() => setRange(r.v)}
              className={`rounded px-1.5 py-0.5 text-[11px] ${range === r.v ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground'}`}
            >
              {t(r.label)}
            </button>
          ))}
        </div>
      </div>
      <Body q={q} rangeLabel={rangeLabel} />
    </div>
  )
}

function Body({ q, rangeLabel }: { q: ReturnType<typeof useQuery<RuleBacktestResult, Error>>; rangeLabel: string }) {
  const { t } = useT()
  if (q.isLoading || q.isFetching)
    return (
      <div className="flex items-center gap-2 py-3 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" /> {t('backtest.loading')}
      </div>
    )
  if (q.isError) {
    const e = q.error
    const code = e instanceof ApiError ? e.code : ''
    const text =
      code === 'not_configured'
        ? t('backtest.noProm')
        : code === 'backtest_failed'
          ? t('backtest.promFailed', { error: e.message })
          : code === 'invalid'
            ? t('backtest.invalid')
            : code === 'no_rule'
              ? t('backtest.noRule')
              : t('backtest.failed', { error: e.message })
    return <p className="py-1 text-sm text-muted-foreground">{text}</p>
  }
  if (!q.data) return null
  return <Result r={q.data} rangeLabel={rangeLabel} />
}

function Result({ r, rangeLabel }: { r: RuleBacktestResult; rangeLabel: string }) {
  const { t, tn, rich } = useT()
  const { timestamps, series } = useMemo(() => {
    const start = Date.parse(r.start) / 1000
    const end = Date.parse(r.end) / 1000
    const step = Math.max(60, Math.ceil((end - start) / 400))
    const eps = r.episodes.map((e) => [Date.parse(e.firingAt) / 1000, Date.parse(e.resolvedAt) / 1000])
    const ts: number[] = []
    const v: number[] = []
    for (let t = start; t <= end; t += step) {
      ts.push(t)
      v.push(eps.some(([a, b]) => a <= t + step && t < b) ? 1 : 0)
    }
    const c = cssColor('--firing', '#e5484d')
    return { timestamps: ts, series: [{ label: t('backtest.series'), stroke: c, fill: 'color-mix(in oklab, ' + c + ' 25%, transparent)', values: v }] }
  }, [r, t])
  const ongoing = r.episodes.some((e) => e.ongoing)
  return (
    <div className="space-y-1.5">
      <p className="text-sm">
        {r.firings === 0 ? (
          t('backtest.never', { range: rangeLabel })
        ) : (
          <>
            {rich('backtest.fired', { range: rangeLabel, n: <span className="font-semibold tabular-nums">{tn('backtest.times', r.firings)}</span> })}
            {ongoing ? t('backtest.ongoing') : ''}.
          </>
        )}
      </p>
      {r.firings > 0 && <TimeSeriesChart timestamps={timestamps} series={series} height={56} stepped compact />}
    </div>
  )
}
