// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Sets of programs: display names and helpers shared by the status page, the
// sidebar and the system editor.

import { t } from '@/i18n'
import type { CheckState, FoundProgram, ProgramKind, ProgramOrigin, StackComponent, StackSet } from '@/lib/api'

export const KINDS: ProgramKind[] = ['prometheus', 'alertmanager', 'grafana', 'loki', 'tempo', 'otel-collector']

export const programInfo: Record<ProgramKind, { title: string; example: string }> = {
  prometheus: { title: 'Prometheus', example: 'http://prometheus-operated.monitoring:9090' },
  alertmanager: { title: 'Alertmanager', example: 'http://alertmanager-operated.monitoring:9093' },
  grafana: { title: 'Grafana', example: 'http://grafana.monitoring' },
  loki: { title: 'Loki', example: 'http://loki.monitoring:3100' },
  tempo: { title: 'Tempo', example: 'http://tempo.monitoring:3200' },
  'otel-collector': { title: 'OpenTelemetry Collector', example: 'http://otel-collector.monitoring:13133' },
}

/** What a program does, in a few words. */
export const programRole = (kind: ProgramKind): string => t(`stacks.role.${kind}` as const)

export const originLabel = (o: ProgramOrigin): string => t(`stacks.origin.${o}` as const)

/** ID of the built-in set; its name is shown from the dictionary, whatever name the core sends. */
export const MAIN_STACK = 'main'

export function stackName(id: string | undefined, name: string | undefined): string {
  if (id === MAIN_STACK) return t('stacks.mainName')
  return name || id || ''
}

export type Tone = 'ok' | 'warn' | 'bad' | 'muted'

export function programState(c: StackComponent | undefined): { tone: Tone; text: string } {
  if (!c || !c.configured) return { tone: 'muted', text: t('stacks.state.notConnected') }
  if (c.healthy) return { tone: 'ok', text: t('stacks.check.ok') }
  return { tone: c.state === 'not_ready' ? 'warn' : 'bad', text: checkText(c.state) }
}

export function checkText(state?: CheckState): string {
  switch (state) {
    case 'ok':
      return t('stacks.check.ok')
    case 'not_ready':
      return t('stacks.check.notReady')
    case 'wrong_program':
      return t('stacks.check.wrongProgram')
    case 'no_access':
      return t('stacks.check.noAccess')
    case 'bad_url':
      return t('stacks.check.badUrl')
    default:
      return t('stacks.check.noAnswer')
  }
}

export const checkTone = (state?: CheckState): Tone => (state === 'ok' ? 'ok' : state === 'not_ready' ? 'warn' : 'bad')

/** Sets that systems use; the built-in set when no system uses any. */
export function setsInUse(sets: StackSet[]): StackSet[] {
  const used = sets.filter((s) => s.usedBy.length > 0)
  return used.length > 0 ? used : sets.filter((s) => s.builtin)
}

/** Whether Prometheus and Alertmanager of every set in use work; problem names the first that does not. */
export function coreHealth(sets: StackSet[] | undefined): { state: 'ok' | 'problem' | 'unknown'; problem?: string } {
  if (!sets) return { state: 'unknown' }
  const several = sets.length > 1
  for (const set of setsInUse(sets)) {
    for (const kind of ['prometheus', 'alertmanager'] as const) {
      const c = set.health.find((h) => h.name === kind)
      const where = several ? t('stacks.health.inSet', { name: stackName(set.id, set.name) }) : ''
      const title = programInfo[kind].title
      if (!c?.configured) {
        return {
          state: 'problem',
          problem:
            kind === 'prometheus'
              ? t('stacks.health.promMissing', { title, where })
              : t('stacks.health.amMissing', { title, where }),
        }
      }
      if (!c.healthy) return { state: 'problem', problem: `${title} ${checkText(c.state)}${where}` }
    }
  }
  return { state: 'ok' }
}

/** "key=value" lines ↔ labels (the advanced part of the set form). */
export function labelsToText(l: Record<string, string> | undefined): string {
  return Object.entries(l ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}

export function textToLabels(text: string): { labels: Record<string, string>; error?: string } {
  const labels: Record<string, string> = {}
  for (const raw of text.split(/[\n,]/)) {
    const line = raw.trim()
    if (!line) continue
    const i = line.indexOf('=')
    if (i <= 0) return { labels, error: t('stacks.labels.format', { line }) }
    labels[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return { labels }
}

/** Kamerton puts these labels on every object itself. */
const ownLabels: Record<string, string> = { 'app.kubernetes.io/managed-by': 'kamerton', 'kamerton.dev/managed': 'true' }

/** Labels a found Prometheus / Alertmanager needs on Kamerton's objects (its selector), minus those Kamerton always sets. */
export function suggestedLabels(f: FoundProgram | undefined): Record<string, string> | undefined {
  if (!f?.operator || f.operator.selectsNone) return undefined
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(f.operator.labels)) if (ownLabels[k] !== v) out[k] = v
  return out
}

/** Found programs grouped by kind, in the usual order. */
export function groupFound(found: FoundProgram[]): { kind: ProgramKind; items: FoundProgram[] }[] {
  return KINDS.map((kind) => ({ kind, items: found.filter((f) => f.kind === kind) })).filter((g) => g.items.length > 0)
}

/** Name of the set a system uses, shown only when there is more than one set. */
export function stackBadge(sets: number, name: string | undefined): string | null {
  return sets > 1 && name ? name : null
}

export interface StackDraft {
  name?: string
  urls?: Partial<Record<ProgramKind, string>>
  ruleLabels?: Record<string, string>
  alertmanagerConfigLabels?: Record<string, string>
  /** Why the labels were filled in (found selectors). */
  labelsNote?: string
}

/** Builds a new-set draft from the selected found programs (one per kind). */
export function draftFromFound(found: FoundProgram[]): StackDraft {
  const urls: Partial<Record<ProgramKind, string>> = {}
  let ruleLabels: Record<string, string> | undefined
  let alertmanagerConfigLabels: Record<string, string> | undefined
  const notes: string[] = []
  for (const f of found) {
    if (urls[f.kind]) continue
    urls[f.kind] = f.url
    const l = suggestedLabels(f)
    if (l && f.kind === 'prometheus') {
      ruleLabels = l
      notes.push(t('stacks.draft.ruleLabels', { ref: `${f.operator!.namespace}/${f.operator!.name}` }))
    }
    if (l && f.kind === 'alertmanager') {
      alertmanagerConfigLabels = l
      notes.push(t('stacks.draft.routeLabels', { ref: `${f.operator!.namespace}/${f.operator!.name}` }))
    }
  }
  const ns = found[0]?.namespace
  return {
    name: ns ? t('stacks.draft.name', { ns }) : '',
    urls,
    ruleLabels,
    alertmanagerConfigLabels,
    labelsNote: notes.length ? t('stacks.draft.note', { notes: notes.join('; ') }) : undefined,
  }
}

