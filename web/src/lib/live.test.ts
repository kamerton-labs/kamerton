// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from '@/i18n'
import { availability, bareTitle, failurePath, firstFailure, percent, problems, rateAxis, requestOf, rows, windowText, type NowService, type Span, type SystemNow } from './live'

afterEach(() => setLang('en'))

const svc = (name: string, dependsOn: string[], state: NowService['state'] = 'ok', because?: string[]): NowService => ({
  name,
  type: 'http',
  dependsOn,
  state,
  because,
  alerts: [],
  requests: null,
  errorRatio: null,
  p95: null,
})

const now = (services: NowService[]): SystemNow => ({ alerting: true, metrics: true, traces: true, logs: true, services, incidents: [], edges: [], slo: [], at: '' })

describe('the map', () => {
  it('marks the way from an affected service to the source', () => {
    const n = now([svc('proxy', ['front'], 'affected', ['pay']), svc('front', ['checkout', 'ad'], 'affected', ['pay']), svc('checkout', ['pay'], 'affected', ['pay']), svc('ad', []), svc('pay', [], 'source')])
    expect([...failurePath(n)].sort()).toEqual(['checkout->pay', 'front->checkout', 'proxy->front'])
  })

  it('puts sources before warnings and leaves the affected out', () => {
    const n = now([svc('b', [], 'warning'), svc('a', [], 'affected', ['c']), svc('c', [], 'source')])
    expect(problems(n).map((s) => s.name)).toEqual(['c', 'b'])
  })

  it('does not repeat the service name of an alert title', () => {
    expect(bareTitle('payment: errors above 5%', 'payment')).toBe('errors above 5%')
    expect(bareTitle('Errors above 5%', 'payment')).toBe('Errors above 5%')
  })
})

describe('numbers', () => {
  it('keeps the digits that tell a small share from none', () => {
    expect(percent(0)).toBe('0%')
    expect(percent(0.0004)).toBe('0.04%')
    expect(percent(0.123)).toBe('12%')
    expect(availability(0.97989)).toBe('97.98%')
    expect(availability(1)).toBe('100%')
  })

  it('writes an SLO window in words', () => {
    expect(windowText('30d', (n) => `${n} days`, (n) => `${n} hours`)).toBe('30 days')
    expect(windowText('weird', (n) => `${n}`, (n) => `${n}`)).toBe('weird')
  })

  it('keeps one unit on a rate axis', () => {
    setLang('ru')
    const slow = rateAxis(0.05, '/с', '/мин')
    expect([slow(0.05), slow(0)]).toEqual(['3/мин', '0/мин'])
    expect(rateAxis(6, '/с', '/мин')(6)).toBe('6/с')
  })
})

const span = (id: string, parent: string | undefined, startMs: number, durationMs: number, error = false, service = 's'): Span => ({ id, parent, service, name: id, kind: 'server', startMs, durationMs, error, attrs: [], events: [] })

describe('one request', () => {
  const spans = [span('root', undefined, 0, 100), span('a', 'root', 1, 50), span('tiny', 'root', 2, 3), span('b', 'a', 2, 40, true), span('tinyErr', 'b', 3, 0.1, true)]

  it('folds small steps that lead nowhere, never a failure or its way', () => {
    expect(rows(spans, true).map((r) => r.span.id)).toEqual(['root', 'a', 'b', 'tinyErr'])
    expect(rows(spans, false)).toHaveLength(5)
    expect(rows(spans, true).find((r) => r.span.id === 'a')?.onErrorPath).toBe(true)
  })

  it('names the deepest failed step as where it broke', () => {
    expect(firstFailure(spans)?.id).toBe('tinyErr')
  })

  it('looks past a step that did not fail to the failure under it', () => {
    // proxy fails because pay failed two levels down, through a frontend step that did not.
    const chain = [span('proxy', undefined, 0, 40, true), span('front', 'proxy', 1, 38), span('checkout', 'front', 2, 30, true), span('pay', 'checkout', 3, 5, true), span('other', 'checkout', 9, 2)]
    expect(firstFailure(chain)?.id).toBe('pay')
  })

  it('cuts one request out of a long session', () => {
    const session = [span('session', undefined, 0, 3_600_000), span('req', 'session', 1_000_000, 400), span('step', 'req', 1_000_010, 300), span('other', 'session', 2_000_000, 200)]
    const r = requestOf(session, 'step')!
    expect(r.map((s) => s.id)).toEqual(['req', 'step'])
    expect(r[0].startMs).toBe(0)
    expect(r[0].parent).toBeUndefined()
    expect(r[1].startMs).toBe(10)
    expect(requestOf(spans, 'b')).toBeNull()
  })
})
