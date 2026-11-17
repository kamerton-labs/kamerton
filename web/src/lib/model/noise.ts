// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Noise of rules (how often they fired and cleared by themselves) joined to
// the cards, and the core's suggested setting turned into a card edit.

import type { NoiseRule, NoiseSuggestion, NoiseWindow } from '@/lib/api'
import { formatDuration, quantityToMiB, ruleMatches, setRule, thresholdSpec, thresholdUnit, type RuleItem, type RulePatch } from './rules'
import type { Description } from './types'

export const RANGES = [
  { v: '1d', label: 'backtest.range.1d' },
  { v: '3d', label: 'backtest.range.3d' },
  { v: '7d', label: 'backtest.range.7d' },
] as const satisfies readonly { v: NoiseWindow; label: string }[]

export type Range = (typeof RANGES)[number]['v']

/** The window of "noisy" in the rules list ("this week"); the card's block opens on it too. */
export const NOISY_RANGE: Range = '7d'

/** Counters of the card; the SLO card stands for two alerts, so they are added up. */
export function noiseFor(rule: RuleItem, list: NoiseRule[] | undefined): NoiseRule | undefined {
  const mine = (list ?? []).filter((x) => ruleMatches(rule, x)).sort((a, b) => (b.fired ?? 0) - (a.fired ?? 0))
  if (mine.length <= 1) return mine[0]
  const sum = (f: (x: NoiseRule) => number | undefined) => mine.reduce((n, x) => n + (f(x) ?? 0), 0)
  return {
    ...mine[0],
    fired: sum((x) => x.fired),
    short: sum((x) => x.short),
    longestSeconds: Math.max(...mine.map((x) => x.longestSeconds ?? 0)),
    feedback: { useful: sum((x) => x.feedback?.useful), noise: sum((x) => x.feedback?.noise) },
    noisy: mine.some((x) => x.noisy),
    suggestion: mine.find((x) => x.suggestion)?.suggestion,
  }
}

/** Cards the core calls noisy: its flag is the one definition (internal/noise/noise.go Noisy). */
export function noisyRules(rules: RuleItem[], list: NoiseRule[] | undefined): RuleItem[] {
  return list?.length ? rules.filter((r) => noiseFor(r, list)?.noisy) : []
}

type Format = 'share' | 'number' | 'integer' | 'quantity' | 'duration'

interface Target {
  path: string
  format: Format
}

/** Description fields the card edits, as internal/generator/tunable.go names them (relative to the service). */
function targets(rule: RuleItem): { threshold?: Target; for?: Target } {
  const op = `operations[${rule.operation ?? ''}].`
  const thr = (path: string, format: Format = 'number') => ({ path, format })
  const dur = (path: string) => ({ path, format: 'duration' as const })
  switch (rule.kind) {
    case 'errors':
    case 'errorsPerOp':
      return { threshold: thr('errorRate.threshold', 'share'), for: dur('errorRate.for') }
    case 'opErrors':
      return { threshold: thr(op + 'errorRate.threshold', 'share'), for: dur(op + 'errorRate.for') }
    case 'latency':
      return { threshold: thr('latency.thresholdMs'), for: dur('latency.for') }
    case 'latencyPerOp':
      return { threshold: thr('latency.thresholdMs') }
    case 'opLatency':
      return { threshold: thr(op + 'latency.thresholdMs'), for: dur(op + 'latency.for') }
    case 'latencyAboveNormal':
      return { threshold: thr('latency.aboveNormalFactor') }
    case 'traffic':
      return { for: dur('trafficDrop.for') }
    case 'noReadyPods':
      return { for: dur('noReadyPods.for') }
    case 'queueLag':
      return { threshold: thr('queue.maxLag'), for: dur('queue.for') }
    case 'restarts':
      return { threshold: thr('saturation.maxRestartsPerHour', 'integer') }
    case 'memory':
      return { threshold: thr('saturation.memory', 'quantity'), for: dur('saturation.for') }
    case 'memoryLimit':
      return { threshold: thr('saturation.memoryLimitShare', 'share'), for: dur('saturation.for') }
    case 'cpu':
      return { threshold: thr('saturation.cpuCores'), for: dur('saturation.for') }
    case 'cpuLimit':
      return { threshold: thr('saturation.cpuLimitShare', 'share'), for: dur('saturation.for') }
    case 'cpuAboveNormal':
      return { threshold: thr('saturation.cpuAboveNormalFactor'), for: dur('saturation.for') }
    case 'custom': {
      const p = `customRules[${rule.customName ?? ''}].`
      return { threshold: rule.custom?.condition ? thr(p + 'condition.threshold') : undefined, for: dur(p + 'for') }
    }
    default:
      return {}
  }
}

const DURATION = /^(\d+(ms|s|m|h|d|w|y))+$/

const seconds = (d: string) =>
  [...d.matchAll(/(\d+)(ms|s|m|h|d|w|y)/g)].reduce((n, p) => n + Number(p[1]) * ({ ms: 0.001, s: 1, m: 60, h: 3600, d: 86400, w: 604800, y: 31536000 } as Record<string, number>)[p[2]], 0)

const tidy = (n: number) => Math.round(n * 1e6) / 1e6

/** A value in the description's form turned into the card's: a share becomes percent, a quantity MiB. */
function cardValue(format: string | undefined, raw: string | undefined): number | string | null {
  const v = (raw ?? '').trim()
  if (v === '') return null
  if (format === 'duration') return DURATION.test(v) ? v : null
  if (format === 'quantity') {
    const mib = quantityToMiB(v)
    return Number.isFinite(mib) && mib > 0 ? tidy(mib) : null
  }
  const n = Number(v)
  if (!Number.isFinite(n)) return null
  if (format === 'share') return tidy(n * 100)
  if (format === 'integer') return Number.isInteger(n) ? n : null
  if (format === 'number') return n
  return null
}

const withUnit = (v: string, unit: string) => (!unit ? v : unit.startsWith('%') ? `${v}${unit}` : `${v} ${unit}`)

/** Unit of a value after cardValue: the card's when the path is the card's field. */
function displayUnit(rule: RuleItem, s: NoiseSuggestion): string {
  const own = s.field === 'threshold' && targets(rule).threshold?.path === s.path
  if (own) return rule.kind === 'custom' ? (rule.custom?.unit ?? s.unit ?? '') : (thresholdSpec(rule.kind)?.unit ?? '')
  if (s.format === 'share') return '%'
  if (s.format === 'quantity') return thresholdUnit('memory')
  return s.unit ?? ''
}

export interface SuggestionView {
  field: 'threshold' | 'for'
  current: string
  suggested: string
  /** null: the card has no such field, or the value is in a form the UI does not know. */
  patch: RulePatch | null
}

/** The suggestion in the card's own units, with the edit that writes it into the card. */
export function suggestionView(rule: RuleItem, s: NoiseSuggestion | undefined): SuggestionView | null {
  if (!s?.suggested || (s.field !== 'threshold' && s.field !== 'for')) return null
  const show = (raw: string | undefined) => {
    const v = cardValue(s.format, raw)
    if (v === null) return withUnit(raw ?? '', s.unit ?? '')
    if (typeof v === 'string') return formatDuration(v)
    return withUnit(String(Math.round(v * 1000) / 1000), displayUnit(rule, s))
  }
  return { field: s.field, current: show(s.current), suggested: show(s.suggested), patch: suggestionPatch(rule, s) }
}

/** The edit that writes the suggestion into the card, or null when the card has no field at that path in that form. */
export function suggestionPatch(rule: RuleItem, s: NoiseSuggestion | undefined): RulePatch | null {
  if (!s || (s.field !== 'threshold' && s.field !== 'for')) return null
  const target = targets(rule)[s.field]
  if (!target || target.path !== s.path || target.format !== s.format) return null
  const v = cardValue(s.format, s.suggested)
  if (v === null) return null
  if (typeof v === 'string') return rule.for === '' ? null : { for: v }
  const spec = thresholdSpec(rule.kind)
  if (!Number.isFinite(rule.threshold) || (spec && (v < spec.min || (spec.max !== undefined && v > spec.max)))) return null
  return { threshold: v }
}

export function suggestionApplied(rule: RuleItem, patch: RulePatch | null): boolean {
  if (!patch) return false
  if (patch.threshold !== undefined) return Math.abs(rule.threshold - patch.threshold) < 1e-9
  if (patch.for !== undefined) return seconds(rule.for) === seconds(patch.for)
  return false
}

// setRule writes to the service (or the operation, or the custom rule), never
// to defaults: the same place tunable.go writes an inherited value.
export function applySuggestion(desc: Description, rule: RuleItem, s: NoiseSuggestion | undefined): Description {
  const patch = suggestionPatch(rule, s)
  return patch ? setRule(desc, rule.id, patch) : desc
}
