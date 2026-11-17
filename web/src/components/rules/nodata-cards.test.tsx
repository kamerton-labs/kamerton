// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from '@/i18n'
import { hintRu } from '@/i18n/schema-ru'
import { EditorContext, type EditorState } from '@/lib/editor'
import { deriveRules, setRule } from '@/lib/model/rules'
import { setMessage } from '@/lib/model/message'
import type { Description } from '@/lib/model/types'
import { parseDescriptionYaml } from '@/lib/model/yaml'
import { schemaAt, type SchemaNode } from '@/lib/yaml-schema'
// ?raw: this file is type-checked with the app, which has no Node types for node:fs.
import exampleText from '../../../../testdata/otel-demo/system.yaml?raw'
import schemaText from '../../../../internal/schema/systemdescription.schema.json?raw'
import { RuleEditor } from './RuleCard'
import RulesView from './RulesView'

const schema = JSON.parse(schemaText) as SchemaNode

function example(): Description {
  const p = parseDescriptionYaml(exampleText)
  if (!p.ok) throw new Error(p.error)
  return p.desc
}

function render(desc: Description, el: ReactElement) {
  const state: EditorState = {
    systemId: 'otel-demo',
    draft: { text: exampleText, saved: exampleText, error: null } as unknown as EditorState['draft'],
    desc,
    readOnly: false,
    edit: () => {},
    destinations: [],
    issues: [],
  }
  return renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <EditorContext value={state}>{el}</EditorContext>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

afterEach(() => setLang('en', false))

describe('rules list', () => {
  it('shows the system rule first, under the system name, and counts rules as the core does', () => {
    const html = render(example(), <RulesView />)
    expect(html).toContain('195 of 195 rules enabled')
    const first = html.indexOf('data-testid="rule-system"')
    expect(first).toBeGreaterThan(0)
    expect(first).toBeLessThan(html.indexOf('frontend-proxy'))
    expect(html.slice(first, first + 300)).toContain('otel-demo')
    expect(html).toContain('whole system')
    expect(html).toContain('No span metrics from any service for over 3 min at an hour that normally has traffic')
    expect(html).not.toContain('>_system<')
  })
})

describe('no-data cards', () => {
  it('edit "no data for" instead of a pending period and hide the severity of the system rule', () => {
    const d = example()
    const sys = deriveRules(d)[0]
    const html = render(d, <RuleEditor rule={sys} />)
    expect(html).toContain('No data for')
    expect(html).not.toContain('Holds for at least')
    expect(html).not.toContain('Same as service')
    expect(html).not.toContain('Delete')
  })

  it('renders the service card in Russian with the core default text in the preview', () => {
    setLang('ru', false)
    let d = example()
    d.spec.notifications = { ...d.spec.notifications, language: 'ru' }
    const card = () => deriveRules(d).find((r) => r.id === 'cart/noData')!
    d = setMessage(d, card(), { title: 'Корзина молчит' })
    const html = render(d, <RuleEditor rule={card()} />)
    expect(html).toContain('Нет данных дольше')
    expect(html).toContain('Сервис, у которого в этот час обычно есть трафик')
    expect(html).toContain('Корзина молчит')
    expect(html).toContain('Kamerton не видит этот сервис: в это время у него обычно есть трафик, но span-метрик нет уже 3m')
    expect(html).toContain('Проверьте, что его поды работают')
  })

  it('show the no-data note on the requests-stopped card', () => {
    const d = example()
    const traffic = deriveRules(d).find((r) => r.id === 'cart/traffic')!
    expect(render(d, <RuleEditor rule={traffic} />)).toContain('the &quot;No data&quot; rule stays silent meanwhile')
    const off = setRule(d, 'cart/noData', { enabled: false })
    expect(render(off, <RuleEditor rule={deriveRules(off).find((r) => r.id === 'cart/traffic')!} />)).not.toContain('No data')
  })

  it('edit the pending period of the pods card and show its fixed condition', () => {
    let d = example()
    const card = () => deriveRules(d).find((r) => r.id === 'cart/noReadyPods')!
    d = setMessage(d, card(), { text: 'Pods of {service}: {threshold}' })
    const html = render(d, <RuleEditor rule={card()} />)
    expect(html).toContain('Holds for at least')
    expect(html).toContain('Pods of cart: at least one ready pod')
  })
})

describe('YAML editor', () => {
  it('knows the new fields and has Russian hints for them', () => {
    for (const path of [
      ['spec', 'noData', 'after'],
      ['spec', 'defaults', 'noReadyPods', 'for'],
      ['spec', 'services', '*', 'noData', 'severity'],
      ['spec', 'telemetry', 'containers', 'readyMetric'],
    ]) {
      expect(schemaAt(schema, path), path.join('.')).toBeDefined()
      expect(hintRu(path), path.join('.')).toBeTruthy()
    }
    expect(hintRu(['spec', 'noData'])).toContain('Kamerton не видит систему')
  })
})
