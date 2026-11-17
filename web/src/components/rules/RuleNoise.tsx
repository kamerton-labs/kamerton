// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// How often the rule fired and cleared by itself, and the quieter setting
// the core suggests. Advice only: Apply edits the card, saving stays manual.

import { useState } from 'react'
import { Loader2 } from 'lucide-react'
import FeedbackButtons from '@/components/alerts/FeedbackButtons'
import { Button } from '@/components/ui/button'
import { useT } from '@/i18n'
import { ApiError, useNoise, type NoiseRule, type NoiseSuggestion } from '@/lib/api'
import { useEditor } from '@/lib/editor'
import { isDirty } from '@/lib/model/draft'
import { NOISY_RANGE, RANGES, noiseFor, suggestionApplied, suggestionView, type Range } from '@/lib/model/noise'
import { formatDuration, isNoDataKind, setRule, type RuleItem } from '@/lib/model/rules'
import RangeSwitch from './RangeSwitch'

export default function RuleNoise({ rule }: { rule: RuleItem }) {
  const { systemId } = useEditor()
  const [range, setRange] = useState<Range>(NOISY_RANGE)
  const q = useNoise(systemId, range)
  const { t, tn } = useT()
  if (!q.data) {
    // An older core has no such endpoint: the card stays as it was.
    if (q.isError && q.error instanceof ApiError && q.error.status === 404) return null
    if (!q.isError)
      return (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground" data-testid="rule-noise-loading">
          <Loader2 className="size-3.5 animate-spin" /> {t('noise.loading')}
        </p>
      )
    return <p className="text-xs text-muted-foreground">{t('noise.failed', { error: q.error.message })}</p>
  }
  if (q.data.available === false)
    return q.data.reason ? (
      <p className="text-xs text-muted-foreground" data-testid="rule-noise-unavailable">
        {q.data.reason}
      </p>
    ) : null
  const n = noiseFor(rule, q.data.rules)
  // Kept after a switch, so the person can switch back.
  if (!n && range === NOISY_RANGE) return null
  // While another window loads the previous numbers stay: name the window they are for.
  const rangeLabel = t((RANGES.find((r) => r.v === q.data.window) ?? RANGES.find((r) => r.v === range)!).label)
  return (
    <div className="space-y-1.5 rounded-md border p-2.5" data-testid="rule-noise">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-medium text-muted-foreground">{t('noise.title')}</span>
        <RangeSwitch value={range} onChange={setRange} label={t('noise.period')} />
      </div>
      {n?.fired ? (
        <Counters n={n} rangeLabel={rangeLabel} shortUnder={q.data.shortUnder} />
      ) : !n?.consequences ? (
        <p className="text-sm text-muted-foreground">{t('noise.quiet', { range: rangeLabel })}</p>
      ) : null}
      {n?.consequences ? (
        <p className="text-xs text-muted-foreground" data-testid="rule-noise-consequences">
          {n.fired ? tn('noise.consequences', n.consequences) : tn('noise.consequencesOnly', n.consequences, { range: rangeLabel })}
        </p>
      ) : null}
      {n?.suggestion && <Suggestion rule={rule} s={n.suggestion} longFrom={q.data.longFrom} />}
      {n && (
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <FeedbackButtons system={systemId} alert={n.alert ?? rule.alert} service={rule.service} operation={n.operation ?? rule.alertOperation} className="-ml-2" />
          <Marks n={n} />
        </div>
      )}
    </div>
  )
}

function Counters({ n, rangeLabel, shortUnder }: { n: NoiseRule; rangeLabel: string; shortUnder?: string }) {
  const { tn } = useT()
  return (
    <p className="text-sm" data-testid="rule-noise-counts">
      {tn('noise.fired', n.fired ?? 0, { range: rangeLabel })}
      {n.short && shortUnder ? tn('noise.short', n.short, { d: formatDuration(shortUnder) }) : ''}
    </p>
  )
}

function Marks({ n }: { n: NoiseRule }) {
  const { tn } = useT()
  const parts = [
    n.feedback?.noise ? tn('noise.markedNoise', n.feedback.noise) : '',
    n.feedback?.useful ? tn('noise.markedUseful', n.feedback.useful) : '',
  ].filter(Boolean)
  if (parts.length === 0) return null
  return (
    <span className="text-xs text-muted-foreground" data-testid="rule-noise-marks">
      {parts.join(' · ')}
    </span>
  )
}

/** 90 -> "1m30s", the form formatDuration reads. */
const durationOf = (sec: number) => {
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  return (m ? `${m}m` : '') + (s || !m ? `${s}s` : '')
}

function Suggestion({ rule, s, longFrom }: { rule: RuleItem; s: NoiseSuggestion; longFrom?: string }) {
  const { edit, readOnly, draft } = useEditor()
  const { t, tn } = useT()
  const view = suggestionView(rule, s)
  if (!view) return null
  const field = view.field === 'for' ? t(isNoDataKind(rule.kind) ? 'ruleCard.after' : 'ruleCard.for') : t('ruleCard.threshold')
  const vars = { field, suggested: view.suggested, current: view.current }
  const miss = s.wouldMiss ? tn('noise.missed', s.wouldMiss) : longFrom ? t('noise.missedNoneLong', { d: formatDuration(longFrom) }) : t('noise.missedNone')
  const effect = s.wouldFire === undefined ? '' : t('noise.effect', { fire: tn('noise.wouldFire', s.wouldFire), miss })
  const applied = suggestionApplied(rule, view.patch)
  const patch = view.patch
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <p className="min-w-0 flex-1 text-sm" data-testid="rule-noise-suggestion">
        {effect ? t('noise.suggest', { ...vars, effect }) : t('noise.suggestPlain', vars)}
        {view.field === 'for' && s.delaySeconds ? <span data-testid="rule-noise-delay"> {t('noise.delay', { d: formatDuration(durationOf(s.delaySeconds)) })}</span> : null}
      </p>
      {patch && !applied && (
        <Button variant="outline" size="xs" disabled={readOnly} onClick={() => edit((d) => setRule(d, rule.id, patch))} data-testid="rule-noise-apply">
          {t('noise.apply')}
        </Button>
      )}
      {applied && isDirty(draft) && (
        <span className="text-xs text-muted-foreground" data-testid="rule-noise-applied">
          {t('noise.applied')}
        </span>
      )}
    </div>
  )
}
