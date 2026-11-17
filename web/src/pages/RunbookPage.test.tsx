// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLang } from '@/i18n'
import type { Runbook, RunbookAlert } from '@/lib/api'

const state: { data?: Runbook } = {}

vi.mock('@/lib/api', () => ({
  useRunbook: () => ({ isLoading: false, isError: false, data: state.data }),
}))

const { default: RunbookPage } = await import('./RunbookPage')

const render = (url: string) =>
  renderToStaticMarkup(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/systems/:id/runbooks/:service/:alert?" element={<RunbookPage />} />
      </Routes>
    </MemoryRouter>,
  )

const alert = (over: Partial<RunbookAlert>): RunbookAlert => ({ alert: 'KamertonNoReadyPods', severity: 'critical', signal: 'availability', title: 'No ready pods', summary: '', expr: 'up', ...over })

afterEach(() => setLang('en', false))

describe('runbook page', () => {
  it('renders the runbook of the whole system, not a service called _system', () => {
    state.data = {
      system: 'shop',
      systemID: 'shop',
      service: { name: '_system', system: true, dependsOn: [], calledBy: [] },
      alerts: [alert({ alert: 'KamertonTelemetryMissing', signal: 'no_data', title: 'Kamerton cannot see the system: no data arrives' })],
      markdown: '',
    }
    const html = render('/systems/shop/runbooks/_system/KamertonTelemetryMissing')
    expect(html).toContain('Runbook: system shop')
    expect(html).toContain('Alerts about the whole system rather than one service')
    expect(html).not.toContain('_system')
    expect(html).toContain('href="/alerts?system=shop"')
    expect(html).toContain('>no data<')
    expect(html).toContain('Kamerton receives no span metrics from any service of the system')
    expect(html).toContain('or a day after the last data.')
  })

  it('gives advice for the pods and per-service no-data alerts in both languages', () => {
    state.data = {
      system: 'shop',
      systemID: 'shop',
      service: { name: 'cart', type: 'grpc', critical: true, dependsOn: [], calledBy: [] },
      alerts: [alert({}), alert({ alert: 'KamertonServiceTelemetryMissing', signal: 'no_data', title: 'No data from the service' })],
      markdown: '',
    }
    let html = render('/systems/shop/runbooks/cart')
    expect(html).toContain('Runbook: cart')
    expect(html).toContain('>availability<')
    expect(html).toContain('No pod of the service is ready: it is down for its callers')
    expect(html).toContain('its own telemetry stopped, not the pipeline')
    setLang('ru', false)
    html = render('/systems/shop/runbooks/cart')
    expect(html).toContain('>доступность<')
    expect(html).toContain('Ни один под сервиса не готов')
    expect(html).toContain('остановилась его собственная телеметрия')
  })

  it('puts what helped before above the generic advice, and nothing when there is no experience', () => {
    const base: Runbook = { system: 'shop', systemID: 'shop-prod', service: { name: 'payment', type: 'grpc', critical: true }, alerts: [alert({})], markdown: '' }
    state.data = {
      ...base,
      experience: [
        { id: 'h-7', startedAt: '2026-10-27T09:14:00Z', durationSeconds: 312, cause: 'Memory limit too low', fix: 'Raised the limit to 512Mi' },
        { id: 'h-3', startedAt: '2026-10-01T10:00:00Z', durationSeconds: 5400, fix: 'Rolled back to v1.4.2' },
        { id: 'h-1', startedAt: '2026-09-01T10:00:00Z' },
      ],
    }
    let html = render('/systems/shop/runbooks/payment')
    const exp = html.indexOf('data-testid="runbook-experience"')
    expect(exp).toBeGreaterThan(0)
    expect(exp).toBeLessThan(html.indexOf('No pod of the service is ready'))
    expect(html).toContain('What helped before')
    expect(html.match(/data-testid="runbook-experience-item"/g)).toHaveLength(2)
    expect(html).toContain('What helped: </span>Raised the limit to 512Mi')
    expect(html).toContain('Cause: </span>Memory limit too low')
    expect(html).toContain('lasted 5 min')
    expect(html).toContain('lasted 1 h 30 min')
    expect(html).toContain('href="/alerts?view=history&amp;system=shop&amp;incident=h-7"')
    setLang('ru', false)
    html = render('/systems/shop/runbooks/payment')
    expect(html).toContain('Что помогало раньше')
    expect(html).toContain('Что помогло: </span>Raised the limit')
    setLang('en', false)
    for (const experience of [undefined, []]) {
      state.data = { ...base, experience }
      html = render('/systems/shop/runbooks/payment')
      expect(html).not.toContain('runbook-experience')
      expect(html).not.toContain('What helped')
    }
  })
})
