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
})
