// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { getLang, t, tn } from '@/i18n'
import type { Incident, SystemSummary } from '@/lib/api'

export function timeAgo(iso: string): string {
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return t('time.justNow')
  const m = Math.round(s / 60)
  if (m < 60) return tn('time.minutesAgo', m)
  const h = Math.round(m / 60)
  if (h < 48) return tn('time.hoursAgo', h)
  const d = Math.round(h / 24)
  return tn('time.daysAgo', d)
}

/** "started 5 minutes ago" / "started just now" for an alert's start time. */
export function sinceText(iso: string): string {
  const ago = timeAgo(iso)
  return ago === t('time.justNow') ? t('time.startedJustNow') : t('time.started', { ago })
}

/** State of a system in a few words (the system list and the system header); incidents name their causes. */
export function systemStateText(s: Pick<SystemSummary, 'state' | 'alerts'>, incidents: Incident[] = []): string {
  if (incidents.length > 0 && (s.state === 'firing' || s.state === 'warning')) {
    const roots = [...new Set(incidents.flatMap((i) => (i.roots.length ? i.roots : [t('incident.systemRoot')])))]
    return tn('incident.state', incidents.length, { roots: roots.slice(0, 2).join(', ') + (roots.length > 2 ? ` +${roots.length - 2}` : '') })
  }
  switch (s.state) {
    case 'firing':
      return t('system.state.firing', { n: s.alerts?.critical ?? 0 }) + (s.alerts?.warning ? t('system.state.firingWarnings', { n: s.alerts.warning }) : '')
    case 'warning':
      return t('system.state.warning', { n: s.alerts?.warning ?? 0 })
    case 'ok':
      return t('system.state.ok')
    case 'invalid':
      return t('system.state.invalid')
    default:
      return t('system.state.unknown')
  }
}

export function dateTime(iso: string): string {
  return new Date(iso).toLocaleString(getLang() === 'ru' ? 'ru-RU' : 'en-GB')
}

/** Only absolute http(s) URLs from server data become links, never javascript: or data:. Else null. */
export function httpUrl(raw: string | undefined): string | null {
  if (!raw) return null
  try {
    const u = new URL(raw.trim())
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : null
  } catch {
    return null
  }
}

/** Resolves a CSS custom property (e.g. --firing) to a color usable on canvas. */
export function cssColor(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

/** A one-line version: a release as "v0.1.0", a commit build as 7 characters, a branch build as is. */
export function shortVersion(v: string): string {
  const s = v.trim()
  const release = /^v?(\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.]+)?)$/.exec(s)
  if (release && !s.endsWith('-dirty')) return `v${release[1]}`
  const commit = /(?:^|-g|^sha-)([0-9a-f]{7,40})(?:-dirty)?$/.exec(s)
  if (commit) return commit[1].slice(0, 7)
  return s.length > 12 ? `${s.slice(0, 12)}…` : s
}
