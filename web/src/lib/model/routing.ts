// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Every receiver of the description with the severities and services it gets.
// Same model as internal/telegram applyRouting/routingByChat: routing.critical
// and routing.warning cover every service, a route limits it to some services.

import { t, tn } from '@/i18n'
import { clone, type Description, type Receiver, type SecretKeyRef, type Severity } from './types'

export interface Destination {
  receiver: string
  kind: 'telegram' | 'email'
  chatID?: number
  emails?: string[]
  severities: Severity[]
  /** Empty means every service. */
  services: string[]
  /** Routing written by hand that this view cannot show exactly. */
  custom: boolean
}

const SEVS: Severity[] = ['critical', 'warning']

export function deriveDestinations(desc: Description): Destination[] {
  const n = desc.spec.notifications
  if (!n?.receivers) return []
  const r = n.routing ?? {}
  return n.receivers.map((rc) => {
    const all = SEVS.filter((sev) => (sev === 'critical' ? r.critical : r.warning)?.includes(rc.name))
    const routes = (r.routes ?? []).filter((rt) => rt.receiver === rc.name)
    let severities: Severity[] = all
    let services: string[] = []
    let custom = false
    if (routes.length > 0) {
      const sevs = new Set<Severity>(all)
      const svcs = new Set<string>()
      const first = [...routes[0].services].sort().join(',')
      for (const rt of routes) {
        if ([...rt.services].sort().join(',') !== first) custom = true
        for (const sev of rt.severities?.length ? rt.severities : SEVS) sevs.add(sev)
        for (const s of rt.services) svcs.add(s)
      }
      if (all.length > 0) custom = true
      severities = SEVS.filter((s) => sevs.has(s))
      services = all.length > 0 ? [] : [...svcs].sort()
    }
    return {
      receiver: rc.name,
      kind: rc.telegram ? 'telegram' : 'email',
      chatID: rc.telegram?.chatID,
      emails: rc.email?.to,
      severities,
      services,
      custom,
    }
  })
}

export function receiversFor(dests: Destination[], service: string, severity: Severity): Destination[] {
  return dests.filter((d) => d.severities.includes(severity) && (d.services.length === 0 || d.services.includes(service)))
}

function dropRefs(desc: Description, name: string) {
  const r = desc.spec.notifications?.routing
  if (!r) return
  const f = (l?: string[]) => {
    const out = (l ?? []).filter((x) => x !== name)
    return out.length ? out : undefined
  }
  r.critical = f(r.critical)
  r.warning = f(r.warning)
  const routes = (r.routes ?? []).filter((rt) => rt.receiver !== name)
  r.routes = routes.length ? routes : undefined
  for (const k of Object.keys(r) as (keyof typeof r)[]) if (r[k] === undefined) delete r[k]
}

/** Sets what one receiver gets. No severities means it gets nothing. */
export function setDestination(desc: Description, receiver: string, sel: { severities: Severity[]; services: string[] }): Description {
  const next = clone(desc)
  if (!next.spec.notifications?.receivers?.some((r) => r.name === receiver)) return desc
  dropRefs(next, receiver)
  const n = next.spec.notifications!
  n.routing = n.routing ?? {}
  const sevs = SEVS.filter((s) => sel.severities.includes(s))
  if (sevs.length === 0) return next
  if (sel.services.length === 0) {
    for (const sev of sevs) {
      const key = sev === 'critical' ? 'critical' : 'warning'
      n.routing[key] = [...(n.routing[key] ?? []), receiver]
    }
  } else {
    n.routing.routes = [...(n.routing.routes ?? []), { receiver, severities: sevs, services: [...new Set(sel.services)].sort() }]
  }
  return next
}

export function telegramReceiverName(chatID: number): string {
  return chatID < 0 ? `telegram-n${-chatID}` : `telegram-${chatID}`
}

/** Adds a Telegram chat as a receiver (same naming as the core) and routes to it. */
export function addTelegramDestination(
  desc: Description,
  chatID: number,
  secret: SecretKeyRef,
  sel: { severities: Severity[]; services: string[] },
): Description {
  const next = clone(desc)
  next.spec.notifications = next.spec.notifications ?? {}
  const n = next.spec.notifications
  n.receivers = n.receivers ?? []
  const existing = n.receivers.find((r) => r.telegram?.chatID === chatID)
  let name = existing?.name
  if (!name) {
    name = telegramReceiverName(chatID)
    const taken = new Set(n.receivers.map((r) => r.name))
    for (let i = 2; taken.has(name); i++) name = `${telegramReceiverName(chatID)}-${i}`
    const rc: Receiver = { name, telegram: { chatID, botTokenSecret: { ...secret } } }
    n.receivers.push(rc)
  }
  return setDestination(next, name, sel)
}

/** Mail server settings needed to write an email receiver (from GET /email). */
export interface EmailServerRef {
  smarthost?: string
  from?: string
  username?: string
  hasPassword: boolean
  secretName: string
  secretKey: string
}

/** Adds an email recipient as a receiver. The name (email-<id>) comes from the core so it can keep the address in sync. */
export function addEmailDestination(
  desc: Description,
  recipient: { receiver: string; to: string[] },
  server: EmailServerRef,
  sel: { severities: Severity[]; services: string[] },
): Description {
  const next = clone(desc)
  next.spec.notifications = next.spec.notifications ?? {}
  const n = next.spec.notifications
  n.receivers = n.receivers ?? []
  const email: NonNullable<Receiver['email']> = { to: [...recipient.to], from: server.from ?? '', smarthost: server.smarthost ?? '' }
  if (server.username) email.authUsername = server.username
  if (server.hasPassword) email.authPasswordSecret = { name: server.secretName, key: server.secretKey }
  const existing = n.receivers.find((r) => r.name === recipient.receiver)
  if (existing) existing.email = email
  else n.receivers.push({ name: recipient.receiver, email })
  return setDestination(next, recipient.receiver, sel)
}

/** Removes a receiver and everything that routes to it. */
export function removeDestination(desc: Description, receiver: string): Description {
  const next = clone(desc)
  const n = next.spec.notifications
  if (!n?.receivers) return desc
  dropRefs(next, receiver)
  n.receivers = n.receivers.filter((r) => r.name !== receiver)
  if (n.receivers.length === 0) delete n.receivers
  if (n.routing && Object.keys(n.routing).length === 0) delete n.routing
  if (Object.keys(n).length === 0) delete next.spec.notifications
  return next
}

export type ChipTone = 'critical' | 'warning' | 'neutral' | 'muted'

/** Condition of a receiver as short chips: "Critical", "Warnings", "All services" / "3 services". */
export function conditionChips(sel: { severities: Severity[]; services: string[] }): { label: string; tone: ChipTone }[] {
  if (sel.severities.length === 0) return [{ label: t('routing.chip.nothing'), tone: 'muted' }]
  const out: { label: string; tone: ChipTone }[] = []
  if (sel.severities.includes('critical')) out.push({ label: t('routing.chip.critical'), tone: 'critical' })
  if (sel.severities.includes('warning')) out.push({ label: t('routing.chip.warning'), tone: 'warning' })
  out.push({ label: servicesLabel(sel.services), tone: 'neutral' })
  return out
}

/** "All services", "checkout", "3 services". */
export function servicesLabel(services: string[]): string {
  if (services.length === 0) return t('routing.allServices')
  if (services.length === 1) return services[0]
  return tn('count.services', services.length)
}

/** What a system sends to a channel, for chips on the Channels page: "critical · 3 services". */
export function channelUseLabel(u: { severities: Severity[]; services: string[] }): string {
  const sev =
    u.severities.length >= 2
      ? t('routing.use.all')
      : u.severities[0] === 'critical'
        ? t('routing.use.critical')
        : u.severities[0] === 'warning'
          ? t('routing.use.warning')
          : t('routing.use.nothing')
  return u.services.length ? `${sev} · ${servicesLabel(u.services)}` : sev
}

/** One sentence for the bottom of the condition dialog. */
export function conditionSentence(sel: { severities: Severity[]; services: string[]; only: boolean }, target: 'chat' | 'email' = 'chat'): string {
  const chat = target === 'chat'
  if (sel.severities.length === 0) return t(chat ? 'routing.sentence.nothingChat' : 'routing.sentence.nothingEmail')
  if (sel.only && sel.services.length === 0) return t('routing.sentence.pickService')
  const what =
    sel.severities.length >= 2 ? t('routing.what.both') : sel.severities[0] === 'critical' ? t('routing.what.critical') : t('routing.what.warning')
  const n = sel.services.length
  const scope = !sel.only ? t('routing.scope.all') : n === 1 ? t('routing.scope.one', { name: sel.services[0] }) : tn('routing.scope.many', n)
  return t(chat ? 'routing.sentence.chat' : 'routing.sentence.email', { what, scope })
}
