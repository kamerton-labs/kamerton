// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from '@/i18n'
import { addEmailDestination, channelUseLabel, conditionChips, conditionSentence, deriveDestinations, servicesLabel } from './routing'
import type { Description } from './types'
import { parseDescriptionYaml } from './yaml'

const exampleText = readFileSync(new URL('../../../../testdata/otel-demo/system.yaml', import.meta.url), 'utf8')

afterEach(() => setLang('en', false))

function example(): Description {
  const p = parseDescriptionYaml(exampleText)
  if (!p.ok) throw new Error(p.error)
  return p.desc
}

describe('condition chips', () => {
  it('shows severities and the scope', () => {
    expect(conditionChips({ severities: ['critical', 'warning'], services: [] }).map((c) => c.label)).toEqual(['Critical', 'Warnings', 'All services'])
    expect(conditionChips({ severities: ['critical'], services: ['a', 'b', 'c'] }).map((c) => c.label)).toEqual(['Critical', '3 services'])
    expect(conditionChips({ severities: ['warning'], services: ['cart'] }).map((c) => c.label)).toEqual(['Warnings', 'cart'])
  })
  it('warns when nothing is sent', () => {
    expect(conditionChips({ severities: [], services: [] })).toEqual([{ label: 'Nothing is sent', tone: 'muted' }])
  })
  it('counts services', () => {
    expect(servicesLabel(['a'])).toBe('a')
    expect(servicesLabel(['a', 'b', 'c', 'd', 'e'])).toBe('5 services')
  })
  it('counts services with Russian plural forms', () => {
    setLang('ru', false)
    expect(servicesLabel(['a', 'b', 'c'])).toBe('3 сервиса')
    expect(servicesLabel(['a', 'b', 'c', 'd', 'e'])).toBe('5 сервисов')
    expect(servicesLabel(Array.from({ length: 21 }, (_, i) => `s${i}`))).toBe('21 сервис')
  })
})

describe('use label on the Channels page', () => {
  it('says what a system sends', () => {
    expect(channelUseLabel({ severities: ['critical', 'warning'], services: [] })).toBe('all alerts')
    expect(channelUseLabel({ severities: ['critical'], services: ['a', 'b'] })).toBe('critical · 2 services')
    expect(channelUseLabel({ severities: ['warning'], services: ['cart'] })).toBe('warnings · cart')
  })
})

describe('summary sentence of the condition dialog', () => {
  it('reads like a sentence', () => {
    expect(conditionSentence({ severities: ['critical'], services: ['a', 'b', 'c'], only: true })).toBe('This chat gets: critical alerts for 3 services.')
    expect(conditionSentence({ severities: ['critical', 'warning'], services: [], only: false })).toBe('This chat gets: critical alerts and warnings for all services.')
    expect(conditionSentence({ severities: ['warning'], services: ['cart'], only: true }, 'email')).toBe('These addresses get: warnings for service cart.')
    expect(conditionSentence({ severities: ['critical'], services: Array.from({ length: 21 }, (_, i) => `s${i}`), only: true })).toBe(
      'This chat gets: critical alerts for 21 services.',
    )
  })
  it('explains empty choices', () => {
    expect(conditionSentence({ severities: [], services: [], only: false })).toBe('This chat gets nothing.')
    expect(conditionSentence({ severities: ['critical'], services: [], only: true })).toBe('Pick at least one service.')
  })
})

describe('email recipients from the Channels page', () => {
  const server = { smarthost: 'smtp.yandex.ru:465', from: 'alerts@example.com', username: 'alerts@example.com', hasPassword: true, secretName: 'kamerton-smtp', secretKey: 'password' }

  it('adds a receiver named by the core and routes to it', () => {
    const d = addEmailDestination(example(), { receiver: 'email-ab12cd34', to: ['a@example.com', 'b@example.com'] }, server, { severities: ['critical'], services: ['checkout'] })
    expect(d.spec.notifications?.receivers?.at(-1)).toEqual({
      name: 'email-ab12cd34',
      email: {
        to: ['a@example.com', 'b@example.com'],
        from: 'alerts@example.com',
        smarthost: 'smtp.yandex.ru:465',
        authUsername: 'alerts@example.com',
        authPasswordSecret: { name: 'kamerton-smtp', key: 'password' },
      },
    })
    expect(d.spec.notifications?.routing?.routes?.at(-1)).toEqual({ receiver: 'email-ab12cd34', severities: ['critical'], services: ['checkout'] })
    const dest = deriveDestinations(d).find((x) => x.receiver === 'email-ab12cd34')
    expect(dest).toMatchObject({ kind: 'email', severities: ['critical'], services: ['checkout'] })
  })

  it('writes no login or Secret for a server without a password', () => {
    const d = addEmailDestination(example(), { receiver: 'email-1', to: ['a@example.com'] }, { ...server, username: '', hasPassword: false }, { severities: ['warning'], services: [] })
    expect(d.spec.notifications?.receivers?.at(-1)?.email).toEqual({ to: ['a@example.com'], from: 'alerts@example.com', smarthost: 'smtp.yandex.ru:465' })
    expect(d.spec.notifications?.routing?.warning).toContain('email-1')
  })
})
