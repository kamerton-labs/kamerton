// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { hintRu } from '@/i18n/schema-ru'
import { getMessage, setMessage } from './message'
import { addRule, addableKinds, deriveRules, removeRule, setRule } from './rules'
import { getTimeZone, offsetLabel, offsetMinutes, setTimeZone, zoneLabel } from './timezone'
import type { Description } from './types'
import { parseDescriptionYaml, writeDescriptionYaml } from './yaml'

const exampleText = readFileSync(new URL('../../../../testdata/otel-demo/system.yaml', import.meta.url), 'utf8')

function example(): Description {
  const p = parseDescriptionYaml(exampleText)
  if (!p.ok) throw new Error(p.error)
  return p.desc
}

const traffic = (d: Description, s: string) => deriveRules(d).find((r) => r.id === `${s}/traffic`)

describe('rule "requests stopped arriving"', () => {
  it('is on for critical http/grpc services and offered for the others (mirrors the generator)', () => {
    const d = example()
    const on = deriveRules(d).filter((r) => r.kind === 'traffic').map((r) => r.service)
    expect(on).toEqual(['frontend-proxy', 'frontend', 'checkout', 'payment', 'cart', 'product-catalog'])
    const pay = traffic(d, 'payment')!
    expect(pay).toMatchObject({ enabled: true, threshold: 10, for: '2m', severity: 'critical', removable: false, alert: 'KamertonRequestsStopped' })
    expect(pay.sentence).toBe('Requests below 10% of normal for 2 min')
    expect(addableKinds(d, 'email')).toContain('traffic')
    expect(addableKinds(d, 'payment')).not.toContain('traffic')
    expect(addableKinds(d, 'valkey-cart')).not.toContain('traffic')
  })

  it('is turned off on a critical service instead of being deleted', () => {
    let d = setRule(example(), 'payment/traffic', { enabled: false })
    expect(d.spec.services.find((s) => s.name === 'payment')!.trafficDrop).toEqual({ enabled: false })
    expect(traffic(d, 'payment')!.enabled).toBe(false)
    d = setRule(d, 'payment/traffic', { enabled: true })
    expect(d.spec.services.find((s) => s.name === 'payment')!.trafficDrop).toBeUndefined()
    d = removeRule(d, 'payment/traffic')
    expect(traffic(d, 'payment')!.enabled).toBe(false)
  })

  it('is added to a quiet service, edited, and removed again', () => {
    let d = addRule(example(), 'email', { kind: 'traffic', threshold: 0, for: '5m', severity: 'warning' })
    expect(d.spec.services.find((s) => s.name === 'email')!.trafficDrop).toEqual({ enabled: true, threshold: 0, for: '5m', severity: 'warning' })
    expect(traffic(d, 'email')).toMatchObject({ enabled: true, threshold: 0, removable: true, sentence: 'Requests stopped arriving completely for 5 min' })
    d = setRule(d, 'email/traffic', { threshold: 25 })
    expect(d.spec.services.find((s) => s.name === 'email')!.trafficDrop!.threshold).toBe(0.25)
    // Turned off, the card stays; deleted, it goes back to the "add" menu.
    d = setRule(d, 'email/traffic', { enabled: false })
    expect(traffic(d, 'email')!.enabled).toBe(false)
    d = removeRule(d, 'email/traffic')
    expect(traffic(d, 'email')).toBeUndefined()
    expect(d.spec.services.find((s) => s.name === 'email')!.trafficDrop).toBeUndefined()
  })

  it('follows defaults.trafficDrop', () => {
    const d = example()
    d.spec.defaults = { ...d.spec.defaults, trafficDrop: { enabled: true, threshold: 0.2, for: '3m' } }
    expect(traffic(d, 'email')).toMatchObject({ enabled: true, threshold: 20, for: '3m', removable: false })
    d.spec.defaults.trafficDrop = { enabled: false }
    expect(traffic(d, 'payment')).toBeUndefined()
  })

  it("keeps the team's text in trafficDrop.message", () => {
    const d = example()
    const rule = traffic(d, 'payment')!
    const withText = setMessage(d, rule, { title: 'Payments get no requests since {since}' })
    expect(withText.spec.services.find((s) => s.name === 'payment')!.trafficDrop).toEqual({ message: { title: 'Payments get no requests since {since}' } })
    expect(getMessage(withText, rule)?.title).toBe('Payments get no requests since {since}')
    const cleared = setMessage(withText, rule, {})
    expect(cleared.spec.services.find((s) => s.name === 'payment')!.trafficDrop).toBeUndefined()
  })
})

describe('time zone of notifications', () => {
  it('is set and removed in the YAML next to the language', () => {
    const d = setTimeZone(example(), 'Asia/Almaty')
    expect(getTimeZone(d)).toBe('Asia/Almaty')
    const text = writeDescriptionYaml(exampleText, d)
    expect(text).toContain('  timeZone: Asia/Almaty')
    expect(text).toContain('# replace with your chat ID') // comments survive
    const back = setTimeZone(d, '')
    expect(getTimeZone(back)).toBe('')
    expect(back.spec.notifications?.receivers).toHaveLength(2)
    const bare: Description = { apiVersion: 'kamerton.dev/v1alpha1', kind: 'SystemDescription', metadata: { name: 'x' }, spec: { services: [] } }
    expect(setTimeZone(setTimeZone(bare, 'Europe/Berlin'), '').spec.notifications).toBeUndefined()
  })

  it('shows offsets the way the core writes them', () => {
    expect(offsetLabel(0)).toBe('UTC')
    expect(offsetLabel(300)).toBe('UTC+5')
    expect(offsetLabel(330)).toBe('UTC+5:30')
    expect(offsetLabel(-210)).toBe('UTC-3:30')
    expect(offsetMinutes('Asia/Almaty', new Date('2026-11-16T04:00:00Z'))).toBe(300)
    expect(offsetMinutes('Europe/Berlin', new Date('2026-07-01T00:00:00Z'))).toBe(120)
    expect(offsetMinutes('No/Such_Zone')).toBeNaN()
    expect(zoneLabel('Asia/Almaty')).toBe('UTC+5')
    expect(zoneLabel('Europe/Berlin')).toBe('UTC+1, summer UTC+2')
    expect(zoneLabel('')).toBe('UTC')
  })
})

describe('YAML hints in Russian', () => {
  it('explains the new fields', () => {
    expect(hintRu(['spec', 'services', '*', 'trafficDrop'])).toContain('запросы перестали поступать')
    expect(hintRu(['spec', 'defaults', 'trafficDrop', 'threshold'])).toContain('0 - только когда запросов нет совсем')
    expect(hintRu(['spec', 'notifications', 'timeZone'])).toContain('Asia/Almaty')
  })
})
