// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Time zone of notification times (spec.notifications.timeZone). The core
// writes the UTC offset into message templates (internal/generator/timezone.go);
// the UI shows it the same way: "UTC+5".

import { t } from '@/i18n'
import { clone, type Description } from './types'

/** The zone of the description, or '' (UTC). */
export function getTimeZone(desc: Description): string {
  return desc.spec.notifications?.timeZone ?? ''
}

/** Sets the zone ('' removes the setting: UTC). */
export function setTimeZone(desc: Description, zone: string): Description {
  const next = clone(desc)
  const z = zone.trim()
  if (z) {
    next.spec.notifications = { ...(next.spec.notifications ?? {}), timeZone: z }
  } else if (next.spec.notifications) {
    delete next.spec.notifications.timeZone
    if (Object.keys(next.spec.notifications).length === 0) delete next.spec.notifications
  }
  return next
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/** IANA zones known to the browser, with UTC first. */
export function allTimeZones(): string[] {
  let zones: string[]
  try {
    zones = Intl.supportedValuesOf('timeZone')
  } catch {
    zones = []
  }
  return ['UTC', ...zones.filter((z) => z !== 'UTC')]
}

/** Offset of a zone at a moment, in minutes east of UTC; NaN for an unknown zone. */
export function offsetMinutes(zone: string, at: Date = new Date()): number {
  if (!zone || zone === 'UTC') return 0
  try {
    const name = new Intl.DateTimeFormat('en-US', { timeZone: zone, timeZoneName: 'longOffset' }).formatToParts(at).find((p) => p.type === 'timeZoneName')?.value ?? ''
    const m = /GMT([+-])(\d{1,2})(?::(\d{2}))?/.exec(name)
    if (!m) return 0 // "GMT" alone
    return (m[1] === '-' ? -1 : 1) * (Number(m[2]) * 60 + Number(m[3] ?? 0))
  } catch {
    return NaN
  }
}

/** "UTC", "UTC+5", "UTC-3", "UTC+5:30" (same as OffsetLabel in the core). */
export function offsetLabel(minutes: number): string {
  if (!Number.isFinite(minutes) || minutes === 0) return 'UTC'
  const sign = minutes < 0 ? '-' : '+'
  const abs = Math.abs(minutes)
  const h = Math.floor(abs / 60)
  const m = abs % 60
  return `UTC${sign}${h}${m ? `:${String(m).padStart(2, '0')}` : ''}`
}

/** Label of a zone now, and its other offset when it changes for summer: "UTC+1, summer UTC+2". */
export function zoneLabel(zone: string, now: Date = new Date()): string {
  if (!zone) return 'UTC'
  const year = now.getUTCFullYear()
  const jan = offsetMinutes(zone, new Date(Date.UTC(year, 0, 15)))
  const jul = offsetMinutes(zone, new Date(Date.UTC(year, 6, 15)))
  if (!Number.isFinite(jan)) return zone
  if (jan === jul) return offsetLabel(jan)
  // Winter is the smaller offset in both hemispheres' standard time.
  const [std, dst] = jan < jul ? [jan, jul] : [jul, jan]
  return t('tz.withSummer', { std: offsetLabel(std), dst: offsetLabel(dst) })
}
