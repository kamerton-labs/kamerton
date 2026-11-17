// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Team-written notification text of one rule. Placeholders and checks mirror
// internal/description CheckMessageText.

import { t, tl, type Lang } from '@/i18n'
import { BUILTIN, isNoDataKind, thresholdUnit, type RuleItem, type RuleKind } from './rules'
import { clone, type Description, type NotificationMessage, type Service } from './types'

export const PLACEHOLDER_KEYS = ['value', 'threshold', 'service', 'operation', 'system', 'since'] as const
export type PlaceholderKey = (typeof PLACEHOLDER_KEYS)[number]

export const placeholderLabel = (key: PlaceholderKey): string => t(`message.ph.${key}` as const)

const KNOWN = new Set<string>(PLACEHOLDER_KEYS)

/** Problem with one field, in plain words, or '' (same rules as the core). */
export function checkMessageText(text: string): string {
  for (const m of text.matchAll(/\{([^{}]*)\}/g)) {
    if (!KNOWN.has(m[1])) return t('message.check.unknown', { name: `{${m[1]}}`, known: PLACEHOLDER_KEYS.map((k) => `{${k}}`).join(', ') })
  }
  if (/[{}]/.test(text.replace(/\{([^{}]*)\}/g, ''))) return t('message.check.braces')
  const tag = /<\s*\/?\s*[a-zA-Z!?][^>]*>/.exec(text)
  if (tag) return t('message.check.markup', { tag: tag[0] })
  return ''
}

/** Language of the notification messages of a description: en unless it says ru (as the core does). */
export function notificationLang(desc: Description): Lang {
  return desc.spec.notifications?.language === 'ru' ? 'ru' : 'en'
}

/** Sets the language of notification messages; en, the default, removes the setting. */
export function setNotificationLang(desc: Description, lang: Lang): Description {
  const next = clone(desc)
  if (lang === 'ru') {
    next.spec.notifications = { ...(next.spec.notifications ?? {}), language: 'ru' }
  } else if (next.spec.notifications) {
    delete next.spec.notifications.language
    if (Object.keys(next.spec.notifications).length === 0) delete next.spec.notifications
  }
  return next
}

type Where = { get: (s: Service) => NotificationMessage | undefined; set: (s: Service, m: NotificationMessage | undefined) => void }

function where(rule: RuleItem): Where | null {
  const op = (s: Service) => s.operations?.find((o) => o.name === rule.operation)
  switch (rule.kind) {
    case 'errors':
    case 'errorsPerOp':
      return { get: (s) => s.errorRate?.message, set: (s, m) => (s.errorRate = { ...(s.errorRate ?? {}), message: m }) }
    case 'latency':
    case 'latencyPerOp':
    case 'latencyAboveNormal':
      return { get: (s) => s.latency?.message, set: (s, m) => (s.latency = { ...(s.latency ?? {}), message: m }) }
    case 'opErrors':
      return { get: (s) => op(s)?.errorRate?.message, set: (s, m) => op(s) && (op(s)!.errorRate = { ...(op(s)!.errorRate ?? {}), message: m }) }
    case 'opLatency':
      return { get: (s) => op(s)?.latency?.message, set: (s, m) => op(s) && (op(s)!.latency = { ...(op(s)!.latency ?? {}), message: m }) }
    case 'memory':
    case 'memoryLimit':
      return { get: (s) => s.saturation?.memoryMessage, set: (s, m) => (s.saturation = { ...(s.saturation ?? {}), memoryMessage: m }) }
    case 'cpu':
    case 'cpuLimit':
    case 'cpuAboveNormal':
      return { get: (s) => s.saturation?.cpuMessage, set: (s, m) => (s.saturation = { ...(s.saturation ?? {}), cpuMessage: m }) }
    case 'restarts':
      return { get: (s) => s.saturation?.restartsMessage, set: (s, m) => (s.saturation = { ...(s.saturation ?? {}), restartsMessage: m }) }
    case 'oomKilled':
      return { get: (s) => s.saturation?.oomKilledMessage, set: (s, m) => (s.saturation = { ...(s.saturation ?? {}), oomKilledMessage: m }) }
    case 'queueLag':
      return { get: (s) => s.queue?.message, set: (s, m) => s.queue && (s.queue.message = m) }
    case 'queueStopped':
      return { get: (s) => s.queue?.consumerStoppedMessage, set: (s, m) => s.queue && (s.queue.consumerStoppedMessage = m) }
    case 'slo':
      return { get: (s) => s.slo?.message, set: (s, m) => s.slo && (s.slo.message = m) }
    case 'traffic':
      return { get: (s) => s.trafficDrop?.message, set: (s, m) => (s.trafficDrop = { ...(s.trafficDrop ?? {}), message: m }) }
    case 'noData':
      return { get: (s) => s.noData?.message, set: (s, m) => (s.noData = { ...(s.noData ?? {}), message: m }) }
    case 'noReadyPods':
      return { get: (s) => s.noReadyPods?.message, set: (s, m) => (s.noReadyPods = { ...(s.noReadyPods ?? {}), message: m }) }
    case 'systemNoData':
      return null
    case 'custom':
      return {
        get: (s) => s.customRules?.find((c) => c.name === rule.customName)?.message,
        set: (s, m) => {
          const c = s.customRules?.find((x) => x.name === rule.customName)
          if (c) c.message = m
        },
      }
  }
}

export function getMessage(desc: Description, rule: RuleItem): NotificationMessage | undefined {
  if (rule.kind === 'systemNoData') return desc.spec.noData?.message
  const s = desc.spec.services.find((x) => x.name === rule.service)
  return s ? where(rule)?.get(s) : undefined
}

function prune(o: object | undefined) {
  if (!o) return
  const r = o as Record<string, unknown>
  for (const k of Object.keys(r)) if (r[k] === undefined) delete r[k]
}

/** Sets the team's text of a rule; empty fields remove it. */
export function setMessage(desc: Description, rule: RuleItem, msg: NotificationMessage): Description {
  const next = clone(desc)
  const clean: NotificationMessage = {}
  for (const k of ['title', 'text', 'action'] as const) if (msg[k]?.trim()) clean[k] = msg[k]!.trim()
  if (rule.kind === 'systemNoData') {
    const p = { ...(next.spec.noData ?? {}), message: Object.keys(clean).length ? clean : undefined }
    prune(p)
    if (Object.keys(p).length) next.spec.noData = p
    else delete next.spec.noData
    return next
  }
  const s = next.spec.services.find((x) => x.name === rule.service)
  const w = where(rule)
  if (!s || !w) return desc
  w.set(s, Object.keys(clean).length ? clean : undefined)
  // Drop empty containers left behind (errorRate: {message: undefined}).
  for (const k of ['errorRate', 'latency', 'saturation', 'noReadyPods'] as const) {
    prune(s[k])
    if (s[k] && Object.keys(s[k]!).length === 0) delete s[k]
  }
  for (const o of s.operations ?? []) {
    prune(o.errorRate)
    prune(o.latency)
  }
  // An empty trafficDrop is dropped only where the rule is on by default;
  // elsewhere it is what keeps the rule (see rules.ts).
  prune(s.trafficDrop)
  if (s.trafficDrop && Object.keys(s.trafficDrop).length === 0 && (next.spec.defaults?.trafficDrop?.enabled ?? !!s.critical)) delete s.trafficDrop
  // An empty noData is what turns the rule on for a queue or a database (see rules.ts).
  prune(s.noData)
  if (s.noData && Object.keys(s.noData).length === 0 && (s.type === 'http' || s.type === 'grpc')) delete s.noData
  prune(s.queue)
  prune(s.slo)
  for (const c of s.customRules ?? []) prune(c)
  return next
}

/** Placeholder values for the preview; since is shown with the offset label of the description's time zone. */
export function previewValues(rule: RuleItem, system: string, thresholdText: string, zoneLabel = 'UTC'): Record<string, string> {
  return {
    value: '…',
    threshold: thresholdText,
    service: rule.service,
    operation: rule.operation ?? (rule.kind === 'errorsPerOp' || rule.kind === 'latencyPerOp' ? 'oteldemo.Example/Call' : ''),
    system,
    since: `14:03 15.11 ${zoneLabel}`,
  }
}

export function fill(text: string, values: Record<string, string>): string {
  return text.replace(/\{(\w+)\}/g, (m, k: string) => (k in values ? values[k] : m))
}

/** The condition of rules without a threshold, as {threshold} reads in their notifications (internal/rules/localize.go). */
export function fixedThreshold(kind: RuleKind, lang: Lang): string {
  if (kind === 'oomKilled') return tl(lang, 'msg.fixed.oomKilled')
  if (kind === 'queueStopped') return tl(lang, 'msg.fixed.queueStopped')
  if (kind === 'noReadyPods') return tl(lang, 'msg.fixed.noReadyPods')
  return ''
}

const UNITS_MS: [string, number][] = [
  ['y', 31_536_000_000],
  ['w', 604_800_000],
  ['d', 86_400_000],
  ['h', 3_600_000],
  ['m', 60_000],
  ['s', 1000],
  ['ms', 1],
]

/** A duration as the core writes it in messages (Prometheus model.Duration: "180s" becomes "3m"). */
export function promDuration(d: string): string {
  const parts = [...d.matchAll(/(\d+)(ms|s|m|h|d|w|y)/g)]
  if (parts.length === 0) return d
  let ms = parts.reduce((n, p) => n + Number(p[1]) * UNITS_MS.find((u) => u[0] === p[2])![1], 0)
  if (ms === 0) return '0s'
  let out = ''
  for (const [u, size] of UNITS_MS) {
    if (ms >= size) {
      out += `${Math.floor(ms / size)}${u}`
      ms %= size
    }
  }
  return out
}

/** Kamerton's own text and first steps of the no-data rules (internal/generator/rulemessage.go); the team's parts replace them. */
export function defaultMessage(rule: RuleItem, lang: Lang): { text: string; action: string } | null {
  if (!isNoDataKind(rule.kind)) return null
  const k = rule.kind as 'noData' | 'systemNoData'
  const after = promDuration(rule.for || BUILTIN.noDataAfter)
  return { text: tl(lang, `msg.default.${k}.text` as const, { after }), action: tl(lang, `msg.default.${k}.action` as const, { after }) }
}

const TITLED = [
  'errors',
  'errorsPerOp',
  'opErrors',
  'latency',
  'latencyPerOp',
  'latencyAboveNormal',
  'opLatency',
  'memory',
  'memoryLimit',
  'cpu',
  'cpuLimit',
  'cpuAboveNormal',
  'restarts',
  'oomKilled',
  'queueLag',
  'queueStopped',
  'slo',
  'traffic',
  'noData',
  'noReadyPods',
  'systemNoData',
] as const satisfies readonly RuleKind[]

/** The library's default title of a rule in a notification language (internal/rules/localize.go); '' for custom rules. */
export function defaultTitle(kind: RuleKind, lang: Lang): string {
  return (TITLED as readonly string[]).includes(kind) ? tl(lang, `msg.title.${kind as (typeof TITLED)[number]}` as const) : ''
}

/** The threshold as {threshold} reads in a notification of that language. */
export function thresholdText(rule: RuleItem, lang: Lang): string {
  if (!Number.isFinite(rule.threshold)) return fixedThreshold(rule.kind, lang)
  const unit = thresholdUnit(rule.kind, lang)
  return `${rule.threshold}${unit.startsWith('%') ? unit : unit ? ` ${unit}` : ''}`
}
