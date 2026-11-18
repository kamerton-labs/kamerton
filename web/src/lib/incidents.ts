// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import type { ActiveAlert, AlertsResponse, HistoryAlert, HistoryIncident, Incident, IncidentAlert, IncidentEvidence, IncidentNote } from '@/lib/api'
import { httpUrl } from '@/lib/format'
import { formatDuration } from '@/lib/model/rules'

export interface AlertsFilter {
  alertname: string
  service: string
  system: string
}

/** Runbook and incident name of the alerts about the whole system (generator.SystemRunbook, triage.SystemRoot). */
export const SYSTEM_ROOT = '_system'

/** The incident of the alerts about the whole system: no roots, its alerts have no service. */
export const isSystemIncident = (i: Incident): boolean => i.roots.length === 0

const hasEvidence = (e: IncidentEvidence | undefined) => Boolean(e && (e.cause || e.explains?.length || hasEvidenceDetails(e)))

// The core puts every alerting service into some incident
// (internal/triage/incidents.go); one with no affected services and nothing
// found about its cause is a lone failure and stays a plain alert.
export const isIncident = (i: Incident): boolean => isSystemIncident(i) || i.affected.length > 0 || hasEvidence(i.evidence) || !!i.aftermath

export const isCritical = (i: Incident): boolean => i.rootAlerts.some((a) => a.severity === 'critical')

/** The service in its system, or the system itself for an alert without a service. */
export const serviceHref = (system: string, service: string) =>
  service ? `/systems/${encodeURIComponent(system)}?service=${encodeURIComponent(service)}` : `/systems/${encodeURIComponent(system)}`

export function runbookHref(system: string | undefined, service: string, name: string) {
  const tail = `runbooks/${encodeURIComponent(service || SYSTEM_ROOT)}/${encodeURIComponent(name)}`
  return system ? `/systems/${encodeURIComponent(system)}/${tail}` : `/${tail}`
}

const bySeverityThenNewest = (aCritical: boolean, aStart: string, bCritical: boolean, bStart: string) =>
  aCritical === bCritical ? Date.parse(bStart) - Date.parse(aStart) : aCritical ? -1 : 1

/** Incidents matching the filter, then delivered alerts that belong to none of the shown incidents. */
export function groupAlerts(data: AlertsResponse | undefined, f: AlertsFilter): { incidents: Incident[]; others: ActiveAlert[] } {
  const all = (data?.incidents ?? []).filter(isIncident)
  const grouped = new Set(all.map((i) => i.id))
  const matches = (a: { name: string; service: string }) => (!f.alertname || a.name === f.alertname) && (!f.service || a.service === f.service)
  const incidents = all
    .filter((i) => (!f.system || i.system === f.system) && [...i.rootAlerts, ...i.affectedAlerts].some(matches))
    .sort((a, b) => bySeverityThenNewest(isCritical(a), a.startsAt, isCritical(b), b.startsAt))
  const others = (data?.alerts ?? [])
    .filter((a) => !(a.incident && grouped.has(a.incident)) && matches(a) && (!f.system || a.system === f.system))
    .sort((a, b) => bySeverityThenNewest(a.severity === 'critical', a.startsAt, b.severity === 'critical', b.startsAt))
  return { incidents, others }
}

export const systemIncidents = (data: AlertsResponse | undefined, system: string): Incident[] =>
  (data?.incidents ?? []).filter((i) => isIncident(i) && i.system === system)

export const LOG_LINE_MAX = 300

export function capText(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max).trimEnd()}…` : s
}

export function hasEvidenceDetails(e: IncidentEvidence | undefined): boolean {
  return Boolean(e && (e.changes?.length || e.operation || e.logLine || httpUrl(e.tracesURL)))
}

export const alertTitle = (a: IncidentAlert) => a.title || a.name

/** A record of the incident history on the Alerts page. system is the name records carry (internal/server/history.go maps an ID to it); it narrows the list so the record is among the newest shown. */
export function historyHref(id: string | undefined, system?: string): string {
  const q = new URLSearchParams({ view: 'history' })
  if (system) q.set('system', system)
  if (id) q.set('incident', id)
  return `/alerts?${q}`
}

/** 312 -> "5 min", 5400 -> "1 h 30 min"; seconds only under a minute. */
export function incidentDuration(sec: number | undefined): string {
  if (sec === undefined || !Number.isFinite(sec) || sec < 0) return ''
  if (sec < 60) return formatDuration(`${Math.max(1, Math.round(sec))}s`)
  const m = Math.round(sec / 60)
  if (m < 60) return formatDuration(`${m}m`)
  const h = Math.floor(m / 60)
  if (h < 24) return formatDuration(`${h}h${m % 60}m`)
  return formatDuration(`${Math.floor(h / 24)}d${h % 24}h`)
}

// Caps of internal/history/history.go (MaxCause, MaxFix), counted in
// characters (runes) after trimming; longer text gets 400 too_long.
export const NOTE_CAUSE_MAX = 200
export const NOTE_FIX_MAX = 500

/** Characters as the core counts them: code points, not UTF-16 units. */
export const noteLength = (s: string) => [...s].length

const cap = (s: string, max: number) => [...s.trim()].slice(0, max).join('')

/** What is sent on save: trimmed and capped. Both fields are sent, so "" clears one. */
export function notePayload(cause: string, fix: string): { cause: string; fix: string } {
  return { cause: cap(cause, NOTE_CAUSE_MAX), fix: cap(fix, NOTE_FIX_MAX) }
}

/** "180 / 200" once a field reaches 80% of its cap, else null. */
export const noteCounter = (len: number, max: number): string | null => (len >= max * 0.8 ? `${len} / ${max}` : null)

export const hasNote = (n: IncidentNote | undefined): boolean => Boolean(n?.cause?.trim() || n?.fix?.trim())

export const historyAlertTitle = (a: HistoryAlert | undefined) => a?.title || a?.name || ''

/** Newest first; records without a start go last. */
export const byNewest = (list: HistoryIncident[]) =>
  [...list].sort((a, b) => (Date.parse(b.startedAt ?? '') || 0) - (Date.parse(a.startedAt ?? '') || 0))
