// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLang } from '@/i18n'
import type { StackDiscovery, StackSet, StacksResponse } from '@/lib/api'

const state: { stacks?: StacksResponse; discovery?: StackDiscovery } = {}
const mutation = () => ({ mutate: vi.fn(), isPending: false, isError: false, error: null })

vi.mock('@/lib/api', () => ({
  useStacks: () => ({ isLoading: false, isError: false, isFetching: false, data: state.stacks, refetch: vi.fn() }),
  useStackDiscovery: () => ({ isLoading: false, isError: false, isFetching: false, data: state.discovery, refetch: vi.fn() }),
  useSettings: () => ({ data: { externalURL: '', linksEnabled: false, mode: 'cluster', namespace: 'kamerton', telegram: { available: true, reason: '' }, auth: { enabled: false } } }),
  useVersion: () => ({ data: { version: '0.1.0', commit: 'x', apiVersion: 'kamerton.dev/v1alpha1' } }),
  checkAddress: vi.fn(),
  useCreateStack: mutation,
  useUpdateStack: mutation,
  useDeleteStack: mutation,
  useSetStackAddress: mutation,
  useResetStackAddress: mutation,
}))

const { default: StatusPage } = await import('@/pages/StatusPage')
const { default: StackBlock } = await import('./StackBlock')
const { default: DiscoverPanel } = await import('./DiscoverPanel')

const render = (el: React.ReactElement) => renderToStaticMarkup(<MemoryRouter>{el}</MemoryRouter>)

const builtin: StackSet = {
  id: 'main',
  // Earlier cores send the name of the built-in set in Russian; the UI shows its own.
  name: 'Основной',
  builtin: true,
  programs: [
    { kind: 'prometheus', url: 'http://kamerton-kps-prometheus:9090', origin: 'bundled', flagURL: 'http://kamerton-kps-prometheus:9090' },
    { kind: 'alertmanager', url: 'http://am.team:9093', origin: 'own', flagURL: 'http://kamerton-kps-alertmanager:9093', overridden: true },
    { kind: 'grafana', url: '', origin: 'none' },
    { kind: 'loki', url: '', origin: 'none' },
    { kind: 'tempo', url: '', origin: 'none' },
    { kind: 'otel-collector', url: '', origin: 'none' },
  ],
  ruleLabels: {},
  alertmanagerConfigLabels: {},
  health: [
    { name: 'prometheus', url: 'http://kamerton-kps-prometheus:9090', configured: true, external: false, origin: 'bundled', healthy: true, state: 'ok', version: '3.5.0' },
    { name: 'alertmanager', url: 'http://am.team:9093', configured: true, external: true, origin: 'own', healthy: false, state: 'wrong_program', message: 'Answers, but it is not Alertmanager.' },
    { name: 'grafana', configured: false, external: false, origin: 'none', healthy: null },
    { name: 'loki', configured: false, external: false, origin: 'none', healthy: null },
    { name: 'tempo', configured: false, external: false, origin: 'none', healthy: null },
    { name: 'otel-collector', configured: false, external: false, origin: 'none', healthy: null },
  ],
  usedBy: ['otel-demo'],
  warnings: [],
}

beforeEach(() => {
  state.stacks = undefined
  state.discovery = undefined
})
afterEach(() => setLang('en', false))

describe('status page', () => {
  it('shows one set as "Observability programs" with six program cards', () => {
    state.stacks = { sets: [builtin], discovery: { available: false, reason: 'Discovery works only in a cluster' }, warnings: [] }
    const html = render(<StatusPage />)
    expect(html).toContain('Kamerton status')
    expect(html).toContain('Observability programs')
    expect(html).not.toContain('Set &quot;Main&quot;')
    for (const k of ['prometheus', 'alertmanager', 'grafana', 'loki', 'tempo', 'otel-collector']) expect(html).toContain(`data-testid="program-main-${k}"`)
    expect(html).toContain('installed with Kamerton')
    expect(html).toContain('your own')
    expect(html).toContain('not connected')
    expect(html).toContain('version 3.5.0')
    expect(html).toContain('a different program answers')
    expect(html).toContain('At install: http://kamerton-kps-alertmanager:9093')
    expect(html).toContain('Find installed')
    expect(html).toContain('data-testid="discover-button"')
    expect(html).toContain('data-testid="add-stack"')
    expect(html).toContain('Discovery works only in a cluster')
    expect(html).toContain('href="/systems/otel-demo"')
  })

  it('renders in Russian after the language is switched', () => {
    setLang('ru', false)
    state.stacks = { sets: [builtin], discovery: { available: false, reason: 'Discovery works only in a cluster' }, warnings: [] }
    const html = render(<StatusPage />)
    expect(html).toContain('Состояние Kamerton')
    expect(html).toContain('Программы наблюдения')
    expect(html).toContain('установлена вместе с Kamerton')
    expect(html).toContain('версия 3.5.0')
    expect(html).toContain('Найти установленные')
    expect(html).toContain('data-testid="discover-button"')
  })

  it('names sets and shows warnings when there are several', () => {
    const team: StackSet = { ...builtin, id: 'team-b', name: 'Team B', builtin: false, usedBy: [], warnings: ['Rules of systems in this set are picked up by several Prometheus instances'], ruleLabels: { team: 'b' } }
    state.stacks = { sets: [builtin, team], discovery: { available: true }, warnings: ['Prometheus a/x, b/y pick up all rules'] }
    const html = render(<StatusPage />)
    expect(html).toContain('Set &quot;Main&quot;')
    expect(html).not.toContain('Основной')
    expect(html).toContain('>main<')
    expect(html).toContain('Set &quot;Team B&quot;')
    expect(html).toContain('data-testid="stack-block-team-b"')
    expect(html).toContain('No system uses this set')
    expect(html).toContain('several Prometheus instances')
    expect(html).toContain('pick up all rules')
    expect(html).toContain('1 label for the operator')
  })
})

describe('found programs', () => {
  it('says why discovery is unavailable', () => {
    state.discovery = { available: false, reason: 'Discovery works only in a cluster: Kamerton runs without the controller.' }
    const html = render(<DiscoverPanel sets={[builtin]} onClose={() => {}} onCreateSet={() => {}} />)
    expect(html).toContain('data-testid="discover-unavailable"')
    expect(html).toContain('runs without the controller')
  })

  it('groups found programs with namespace, state and the set already using them', () => {
    state.discovery = {
      available: true,
      found: [
        { kind: 'alertmanager', namespace: 'monitoring', service: 'alertmanager-operated', url: 'http://alertmanager-operated.monitoring.svc:9093', healthy: true, state: 'ok', message: 'Running', usedBy: [], version: '0.28.1' },
        {
          kind: 'prometheus',
          namespace: 'monitoring',
          service: 'prometheus-operated',
          url: 'http://prometheus-operated.monitoring.svc:9090',
          healthy: true,
          state: 'ok',
          message: 'Running',
          usedBy: ['main'],
          operator: { kind: 'Prometheus', namespace: 'monitoring', name: 'kps', labels: { release: 'kps' }, selectsAll: false, selectsNone: false, hasExpressions: false, namespaces: 'in all namespaces' },
        },
        { kind: 'prometheus', namespace: 'team-a', service: 'prometheus', url: 'http://prometheus.team-a.svc:9090', healthy: false, state: 'no_answer', message: 'Not responding: nothing answers at this address.', usedBy: [] },
      ],
      operator: { available: true, prometheuses: [], alertmanagers: [] },
      warnings: [],
    }
    const html = render(<DiscoverPanel sets={[builtin]} onClose={() => {}} onCreateSet={() => {}} />)
    expect(html.indexOf('Prometheus</h3>')).toBeLessThan(html.indexOf('Alertmanager</h3>'))
    expect(html).toContain('data-testid="found-monitoring-prometheus-operated"')
    expect(html).toContain('namespace team-a')
    expect(html).toContain('already in set &quot;Main&quot;')
    expect(html).toContain('selects objects with labels release=kps, in all namespaces')
    expect(html).toContain('not responding')
    expect(html).toContain('version 0.28.1')
    expect(html).toContain('data-testid="create-from-found"')
  })
})

describe('set block', () => {
  it('has an actions menu and an address button per program', () => {
    const html = render(<StackBlock set={builtin} total={2} />)
    expect(html).toContain('data-testid="stack-actions-main"')
    expect(html).toContain('data-testid="change-address-main-prometheus"')
  })
})
