// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import type { ActiveAlert, AlertsResponse, Incident, IncidentAlert, IncidentEvidence } from '@/lib/api'
import { httpUrl } from '@/lib/format'

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
export const isIncident = (i: Incident): boolean => isSystemIncident(i) || i.affected.length > 0 || hasEvidence(i.evidence)

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
