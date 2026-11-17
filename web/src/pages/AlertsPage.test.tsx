// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLang } from '@/i18n'
import type { ActiveAlert, AlertsResponse, Incident, IncidentAlert } from '@/lib/api'
import { systemStateText } from '@/lib/format'
import { groupAlerts } from '@/lib/incidents'

const state: { data?: AlertsResponse } = {}

vi.mock('@/lib/api', () => ({
  useAlerts: () => ({ isLoading: false, isError: false, data: state.data }),
}))

const { default: AlertsPage } = await import('./AlertsPage')

const render = (url = '/alerts') => renderToStaticMarkup(<MemoryRouter initialEntries={[url]}><AlertsPage /></MemoryRouter>)
const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()

function ia(over: Partial<IncidentAlert>): IncidentAlert {
  return { fingerprint: over.service ?? 'f', name: 'KamertonRequestsStopped', service: 'payment', severity: 'critical', startsAt: ago(12), muted: false, ...over }
}

function active(over: Partial<ActiveAlert>): ActiveAlert {
  return { name: 'KamertonMemoryHigh', system: 'shop', service: 'cart', severity: 'warning', startsAt: ago(3), summary: '', labels: { alertname: 'KamertonMemoryHigh' }, ...over }
}

function incident(over: Partial<Incident> = {}): Incident {
  return {
    id: 'shop:payment',
    system: 'shop',
    roots: ['payment'],
    rootAlerts: [ia({ title: 'Requests stopped arriving', value: '0 req/s' })],
    affected: ['checkout', 'frontend'],
    affectedAlerts: [
      ia({ fingerprint: 'c1', service: 'checkout', name: 'KamertonErrorRateHigh', title: 'Errors above threshold', value: '35%', muted: true }),
      ia({ fingerprint: 'fr1', service: 'frontend', name: 'KamertonLatencyHigh', severity: 'warning', title: 'Slow responses', muted: true }),
    ],
    startsAt: ago(12),
    ...over,
  }
}

const evidence = {
  collectedAt: ago(1),
  cause: 'payment: Requests stopped arriving since 09:18 UTC+5',
  changes: ['new version at 09:14 UTC+5 (v1.4.2 -> v1.4.3)', 'container payment restarted (OOMKilled)'],
  operation: 'oteldemo.PaymentService/Charge',
  operationFailures: 42,
  logLine: 'card declined: upstream timeout ' + 'x'.repeat(600),
  tracesURL: 'https://tempo.example.com/search?q=1',
}

function response(incidents: Incident[], alerts: ActiveAlert[] = []): AlertsResponse {
  const delivered = incidents.flatMap((i) => i.rootAlerts.map((a) => active({ ...a, system: i.system, summary: '', incident: i.id })))
  return { configured: true, alerts: [...delivered, ...alerts], muted: [], incidents }
}

const between = (html: string, testid: string) => {
  const start = html.indexOf(`data-testid="${testid}"`)
  return start < 0 ? '' : html.slice(start)
}

beforeEach(() => {
  state.data = undefined
})
afterEach(() => setLang('en', false))

describe('Alerts page with incidents', () => {
  it('shows the root, the cause, the affected services and the evidence', () => {
    state.data = response([incident({ evidence })])
    const html = render()
    expect(html).toContain('data-testid="incident"')
    expect(html).toContain('data-incident="shop:payment"')
    expect(html).toContain('Requests stopped arriving')
    expect(html).toContain('0 req/s')
    expect(html).toContain('started 12 minutes ago')
    expect(html).toContain('Cause: payment: Requests stopped arriving since 09:18 UTC+5')
    expect(html).toContain('href="/systems/shop?service=payment"')
    expect(html).toContain('href="/systems/shop?service=checkout"')
    expect(html).toContain('href="/systems/shop?service=frontend"')
    expect(html).toContain('href="/systems/shop/runbooks/payment/KamertonRequestsStopped"')
    expect(html).toContain('Also affected:')
    const ev = between(html, 'incident-evidence')
    expect(ev).toContain('What changed')
    expect(ev).toContain('v1.4.2 -&gt; v1.4.3')
    expect(ev).toContain('OOMKilled')
    expect(ev).toContain('oteldemo.PaymentService/Charge')
    expect(ev).toContain('42 failures')
    expect(ev).toContain('card declined: upstream timeout')
    expect(ev).not.toContain('x'.repeat(301))
    expect(ev).toContain('…')
    expect(ev).toMatch(/href="https:\/\/tempo\.example\.com\/search\?q=1"[^>]*target="_blank"[^>]*rel="noreferrer"/)
    expect(ev).toContain('Checked just now')
    expect(html).not.toContain('data-testid="alerts-other"')
  })

  it('names the root service as the cause when there is no evidence', () => {
    state.data = response([incident()])
    const html = render()
    expect(html).toContain('Cause: payment is failing, and the services that call it fail with it.')
    expect(html).not.toContain('data-testid="incident-evidence"')
    expect(html).not.toContain('Probable')
  })

  it('words the cause as probable when the evidence is not certain', () => {
    state.data = response([incident({ evidence: { cause: 'payment: errors 40%', likely: true } })])
    expect(render()).toContain('Probable cause: payment: errors 40%')
    state.data = response([incident({ roots: ['db', 'payment'], evidence: { likely: true } })])
    const html = render()
    expect(html).toContain('Probable cause: db, payment are failing')
    expect(html).not.toContain('data-testid="incident-evidence"')
  })

  it('keeps only http(s) trace links', () => {
    state.data = response([incident({ evidence: { tracesURL: 'javascript:alert(1)', operation: 'GET /pay' } })])
    const html = render()
    expect(html).toContain('GET /pay')
    expect(html).not.toContain('javascript:')
    expect(html).not.toContain('data-testid="incident-traces"')
  })

  it('collapses the muted alerts with a count and opens them for an affected service', () => {
    state.data = response([incident()])
    let html = render()
    const muted = between(html, 'incident-muted')
    expect(muted).toMatch(/^data-testid="incident-muted"/)
    expect(html).not.toMatch(/<details[^>]*open=""[^>]*data-testid="incident-muted"/)
    expect(muted).toContain('2 alerts of affected services')
    expect(muted).toContain('Not sent: muted because the cause is payment.')
    expect(muted.match(/data-testid="incident-muted-alert"/g)).toHaveLength(2)
    expect(muted).toContain('Errors above threshold')
    expect(muted).toContain('href="/systems/shop/runbooks/checkout/KamertonErrorRateHigh"')

    html = render('/alerts?service=checkout')
    expect(html).toMatch(/<details[^>]*open=""[^>]*data-testid="incident-muted"/)
  })

  it('keeps alerts outside incidents below them as before', () => {
    const lone: Incident = { ...incident(), id: 'shop:cart', roots: ['cart'], rootAlerts: [ia({ service: 'cart' })], affected: [], affectedAlerts: [] }
    state.data = response([incident()], [active({ incident: 'shop:cart', title: 'Memory above limit', threshold: '90%' })])
    state.data.incidents!.push(lone)
    const html = render()
    expect(html.match(/data-testid="incident"/g)).toHaveLength(1)
    expect(html.indexOf('data-testid="incidents"')).toBeLessThan(html.indexOf('data-testid="alerts-other"'))
    const other = between(html, 'alerts-other')
    expect(other).toContain('Other alerts')
    expect(other).toContain('data-testid="alert-card"')
    expect(other).toContain('Memory above limit')
    expect(other).toContain('href="/systems/shop?service=cart"')
    expect(other).toContain('href="/systems/shop/runbooks/cart/KamertonMemoryHigh"')
    expect(other).not.toContain('Requests stopped arriving')
  })

  it('keeps the empty state', () => {
    state.data = { configured: true, alerts: [], muted: [], incidents: [] }
    const html = render()
    expect(html).toContain('data-testid="alerts-empty"')
    expect(html).toContain('Nothing is firing right now')
  })

  it('renders in Russian', () => {
    setLang('ru', false)
    state.data = response([incident({ evidence: { ...evidence, likely: true } })])
    const html = render()
    expect(html).toContain('Вероятная причина: payment: Requests stopped arriving')
    expect(html).toContain('Также затронуты:')
    expect(html).toContain('2 оповещения затронутых сервисов')
    expect(html).toContain('Не отправлены: заглушены, потому что причина в payment.')
    expect(html).toContain('Что изменилось')
    expect(html).toContain('42 ошибки')
    expect(html).toContain('Открыть трейсы с ошибками')
  })
})

describe('Alerts page: system-wide and lone incidents', () => {
  const wide = (over: Partial<Incident> = {}): Incident => ({
    id: 'shop:_system',
    system: 'shop',
    roots: [],
    rootAlerts: [ia({ fingerprint: 'sys', service: '', name: 'KamertonTelemetryMissing', title: 'Kamerton cannot see the system: no data arrives', value: 'no span metrics from any of 3 services for over 3m; other alerts cannot fire' })],
    affected: [],
    affectedAlerts: [],
    startsAt: ago(6),
    ...over,
  })

  it('shows the whole system as the cause, with the core text, and no empty service', () => {
    const inc = wide({ affected: ['cart'], affectedAlerts: [ia({ fingerprint: 'c', service: 'cart', name: 'KamertonServiceTelemetryMissing', title: 'No data from the service', muted: true })] })
    state.data = response([inc])
    state.data.alerts[0].text = 'Kamerton cannot see this system: none of its services has sent span metrics for 3m. Until data returns no other alert of this system can fire, so silence does not mean that all is well.'
    const html = render()
    expect(html).toContain('data-incident="shop:_system"')
    expect(html).toContain('Kamerton cannot see the system: no data arrives')
    expect(html).toContain('none of its services has sent span metrics for 3m. Until data returns no other alert of this system can fire')
    expect(html).toContain('href="/systems/shop"')
    expect(html).toContain('href="/systems/shop/runbooks/_system/KamertonTelemetryMissing"')
    expect(html).toContain('Not sent: muted while no data arrives from the whole system.')
    expect(html).not.toContain('undefined')
    expect(html).not.toContain('service=&quot;')
    expect(html).not.toMatch(/service=["&]/)
    expect(html).not.toContain('The cause is')
  })

  it('falls back to its own wording and hides "also affected" when nothing else is affected', () => {
    state.data = response([wide()])
    const html = render()
    expect(html).toContain('Kamerton cannot see this system: none of its services sends span metrics. Until data returns no other alert of this system can fire')
    expect(html).not.toContain('data-testid="incident-affected"')
    expect(html).not.toContain('undefined')
  })

  it('shows a lone failure with evidence as an incident, with the services it explains', () => {
    const lone = incident({
      affected: [],
      affectedAlerts: [],
      rootAlerts: [ia({ name: 'KamertonNoReadyPods', title: 'No ready pods', value: '0 of 2 pods ready' })],
      evidence: { likely: true, cause: 'payment: No ready pods; Requests stopped arriving (alert pending)', explains: ['checkout'], changes: ['container payment restarted (OOMKilled)'] },
    })
    state.data = response([lone])
    const html = render()
    expect(html).toContain('data-testid="incident"')
    expect(html).not.toContain('data-testid="alert-card"')
    expect(html).toContain('Probable cause: payment: No ready pods; Requests stopped arriving (alert pending)')
    expect(between(html, 'incident-explains')).toContain('Explains the earlier alert about checkout')
    expect(html).toContain('data-testid="incident-evidence"')
    expect(html).not.toContain('data-testid="incident-affected"')
  })

  it('keeps a lone failure without evidence as a plain card, and a card without a service links the system', () => {
    const lone: Incident = { ...incident(), id: 'shop:cart', roots: ['cart'], rootAlerts: [ia({ service: 'cart' })], affected: [], affectedAlerts: [], evidence: { likely: false } }
    state.data = { configured: true, alerts: [active({ incident: 'shop:cart' }), active({ service: '', name: 'KamertonCustomX', title: 'Something system-wide' })], muted: [], incidents: [lone] }
    const html = render()
    expect(html).not.toContain('data-testid="incident"')
    expect(html.match(/data-testid="alert-card"/g)).toHaveLength(2)
    expect(html).toContain('href="/systems/shop/runbooks/_system/KamertonCustomX"')
    expect(html).not.toContain('shop · <')
  })

  it('names a system-wide incident in the system state', () => {
    expect(systemStateText({ state: 'firing', alerts: { critical: 1, warning: 0 } }, [wide()])).toBe('1 incident: no data')
  })
})

describe('incident grouping', () => {
  it('filters incidents by any of their services and alerts', () => {
    const data = response([incident()])
    expect(groupAlerts(data, { alertname: '', service: 'frontend', system: '' }).incidents).toHaveLength(1)
    expect(groupAlerts(data, { alertname: 'KamertonErrorRateHigh', service: 'checkout', system: '' }).incidents).toHaveLength(1)
    expect(groupAlerts(data, { alertname: 'KamertonErrorRateHigh', service: 'payment', system: '' }).incidents).toHaveLength(0)
    expect(groupAlerts(data, { alertname: '', service: '', system: 'other' }).incidents).toHaveLength(0)
  })

  it('names incidents in the system state', () => {
    expect(systemStateText({ state: 'firing', alerts: { critical: 1, warning: 0 } }, [incident()])).toBe('1 incident: payment')
    const many = [incident(), incident({ id: 'shop:db+queue', roots: ['db', 'queue'] })]
    expect(systemStateText({ state: 'firing', alerts: { critical: 3, warning: 0 } }, many)).toBe('2 incidents: payment, db +1')
    expect(systemStateText({ state: 'ok', alerts: { critical: 0, warning: 0 } }, [incident()])).toBe('all quiet')
    setLang('ru', false)
    expect(systemStateText({ state: 'warning', alerts: { critical: 0, warning: 2 } }, many)).toBe('2 инцидента: payment, db +1')
  })
})
