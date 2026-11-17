// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from '@/i18n'
import {
  LAYOUT_ANNOTATION,
  addService,
  autoArrange,
  connect,
  disconnect,
  fromCanvas,
  moveNodes,
  positionsOf,
  readLayout,
  removeService,
  renameService,
  toCanvas,
} from './canvas'
import { edit, initDraft, isDirty, setText } from './draft'
import { addRule, deriveRules, removeRule, setRule, ruleState } from './rules'
import { addTelegramDestination, deriveDestinations, receiversFor, removeDestination, setDestination } from './routing'
import type { Description } from './types'
import { parseDescriptionYaml, writeDescriptionYaml } from './yaml'

const exampleText = readFileSync(new URL('../../../../testdata/otel-demo/system.yaml', import.meta.url), 'utf8')

function example(): Description {
  const p = parseDescriptionYaml(exampleText)
  if (!p.ok) throw new Error(p.error)
  return p.desc
}

const deps = (d: Description) => Object.fromEntries(d.spec.services.map((s) => [s.name, s.dependsOn ?? []]))

describe('description -> canvas', () => {
  it('shows every service as a node and every dependency as an edge', () => {
    const d = example()
    const { nodes, edges } = toCanvas(d)
    expect(nodes).toHaveLength(16)
    const depCount = d.spec.services.reduce((n, s) => n + (s.dependsOn?.length ?? 0), 0)
    expect(edges).toHaveLength(depCount)
    expect(edges).toContainEqual({ id: 'checkout->payment', source: 'checkout', target: 'payment' })
  })

  it('arranges a description without layout: callers left of dependencies, no overlaps', () => {
    const d = example()
    expect(readLayout(d)).toEqual({})
    const pos = positionsOf(d)
    expect(pos['frontend-proxy'].x).toBeLessThan(pos.frontend.x)
    expect(pos.frontend.x).toBeLessThan(pos.checkout.x)
    expect(pos.checkout.x).toBeLessThan(pos.payment.x)
    const keys = new Set(Object.values(pos).map((p) => `${p.x},${p.y}`))
    expect(keys.size).toBe(16)
    expect(autoArrange(d.spec.services)).toEqual(pos)
  })

  it('uses stored positions and places services without one below the others', () => {
    const d = example()
    d.metadata.annotations = { [LAYOUT_ANNOTATION]: JSON.stringify({ frontend: [100, 50], checkout: [400, 300] }) }
    const pos = positionsOf(d)
    expect(pos.frontend).toEqual({ x: 100, y: 50 })
    expect(pos.checkout).toEqual({ x: 400, y: 300 })
    expect(pos.payment.y).toBeGreaterThan(300)
  })

  it('ignores a broken layout annotation', () => {
    const d = example()
    d.metadata.annotations = { [LAYOUT_ANNOTATION]: '{not json' }
    expect(positionsOf(d)).toEqual(autoArrange(d.spec.services))
  })
})

describe('canvas -> description', () => {
  it('round-trips without loss; only the layout annotation is added', () => {
    const d = example()
    const { nodes, edges } = toCanvas(d)
    const back = fromCanvas(d, nodes, edges)
    expect(back.spec).toEqual(d.spec)
    expect(back.apiVersion).toBe(d.apiVersion)
    expect(back.metadata.name).toBe(d.metadata.name)
    expect(readLayout(back)).toEqual(Object.fromEntries(nodes.map((n) => [n.id, n.position])))
    // Stable on a second round trip.
    const again = toCanvas(back)
    expect(fromCanvas(back, again.nodes, again.edges)).toEqual(back)
  })

  it('keeps the layout out of spec', () => {
    const d = moveNodes(example(), { frontend: { x: 1, y: 2 } })
    expect(JSON.stringify(d.spec)).not.toContain('layout')
    expect(d.metadata.annotations?.[LAYOUT_ANNOTATION]).toContain('"frontend":[1,2]')
  })

  it('draws and removes dependencies', () => {
    const d = connect(example(), 'email', 'quote')
    expect(deps(d).email).toEqual(['quote'])
    expect(connect(d, 'email', 'quote')).toBe(d)
    expect(connect(d, 'email', 'email')).toBe(d)
    const back = disconnect(d, 'email', 'quote')
    expect(back.spec.services.find((s) => s.name === 'email')?.dependsOn).toBeUndefined()
  })

  it('adds a service at the drop point and removes one with its references', () => {
    let d = addService(example(), { name: 'kafka', type: 'queue', queue: { topic: 'kafka', maxLag: 1000 } }, { x: 900, y: 10 })
    expect(positionsOf(d).kafka).toEqual({ x: 900, y: 10 })
    expect(d.spec.services).toHaveLength(17)
    d = setDestination(d, 'oncall-telegram', { severities: ['critical'], services: ['payment', 'checkout'] })
    d = removeService(d, 'payment')
    expect(d.spec.services.map((s) => s.name)).not.toContain('payment')
    expect(deps(d).checkout).not.toContain('payment')
    expect(readLayout(d).payment).toBeUndefined()
    expect(d.spec.notifications?.routing?.routes).toEqual([{ receiver: 'oncall-telegram', severities: ['critical'], services: ['checkout'] }])
  })

  it('renames a service everywhere', () => {
    let d = setDestination(example(), 'team-email', { severities: ['warning'], services: ['cart'] })
    d = renameService(d, 'cart', 'basket')
    expect(deps(d).frontend).toContain('basket')
    expect(deps(d).checkout).toContain('basket')
    expect(d.spec.notifications?.routing?.routes?.[0].services).toEqual(['basket'])
  })
})

describe('YAML stays the stored truth', () => {
  it('writes nothing when nothing changed', () => {
    expect(writeDescriptionYaml(exampleText, example())).toBe(exampleText)
  })

  it('changes only what was edited and keeps comments', () => {
    const d = connect(example(), 'email', 'quote')
    const text = writeDescriptionYaml(exampleText, d)
    expect(text).toContain('# replace with your chat ID')
    expect(text).toContain('# flagd streaming connection produces noise errors.')
    expect(text).toContain(LAYOUT_ANNOTATION)
    const reparsed = parseDescriptionYaml(text)
    expect(reparsed.ok && reparsed.desc).toEqual(d)
    // Untouched services are byte-identical.
    const block = (t: string, name: string) => t.slice(t.indexOf(`- name: ${name}\n`), t.indexOf('\n\n', t.indexOf(`- name: ${name}\n`)))
    expect(block(text, 'checkout')).toBe(block(exampleText, 'checkout'))
  })

  it('keeps flow style of lists', () => {
    const d = connect(example(), 'shipping', 'email')
    const text = writeDescriptionYaml(exampleText, d)
    expect(text).toContain('dependsOn: [quote, email]')
  })
})

describe('invalid YAML', () => {
  it('reports the error with a line', () => {
    const p = parseDescriptionYaml('metadata:\n  name: x\nspec: [\n')
    expect(p.ok).toBe(false)
    if (!p.ok) expect(p.line).toBeGreaterThan(0)
  })

  it('rejects documents that are not descriptions', () => {
    expect(parseDescriptionYaml('just text').ok).toBe(false)
    expect(parseDescriptionYaml('metadata: {name: x}\nspec: {}').ok).toBe(false)
  })

  it('keeps the last valid picture and blocks canvas edits until fixed', () => {
    let draft = initDraft(exampleText)
    const before = draft.desc
    draft = setText(draft, exampleText.replace('spec:', 'spec: ['))
    expect(draft.error).not.toBeNull()
    expect(draft.desc).toBe(before)
    expect(edit(draft, (d) => connect(d, 'email', 'quote'))).toBe(draft)
    draft = setText(draft, exampleText)
    expect(draft.error).toBeNull()
    const edited = edit(draft, (d) => connect(d, 'email', 'quote'))
    expect(isDirty(edited)).toBe(true)
    expect(parseDescriptionYaml(edited.text).ok).toBe(true)
  })
})

describe('rules in plain words', () => {
  afterEach(() => setLang('en', false))

  it('reads every rule the library would generate', () => {
    const rules = deriveRules(example())
    const byId = Object.fromEntries(rules.map((r) => [r.id, r]))
    expect(byId['checkout/errors'].sentence).toBe('Errors above 5% for 1 min')
    expect(byId['checkout/errors'].severity).toBe('critical')
    expect(byId['checkout/errorsPerOp']).toBeDefined()
    expect(byId['checkout/opErrors/oteldemo.CheckoutService/PlaceOrder'].threshold).toBe(1)
    expect(byId['frontend/latency'].sentence).toBe('Latency above 1.5 s for 5% of requests for 2 min')
    expect(byId['ad/memory'].threshold).toBe(400)
    expect(byId['ad/restarts'].sentence).toBe('More than 3 container restarts per hour')
    expect(byId['currency/errors'].severity).toBe('warning')
    expect(byId['orders/queueLag'].threshold).toBe(100)
    expect(byId['payment/slo'].threshold).toBe(99.5)
    expect(byId['valkey-cart/errors']).toBeUndefined()
  })

  it('reads the same rules in Russian after a switch', () => {
    setLang('ru', false)
    const byId = Object.fromEntries(deriveRules(example()).map((r) => [r.id, r]))
    expect(byId['checkout/errors'].sentence).toBe('Ошибок больше 5% - в течение 1 мин')
    expect(byId['frontend/latency'].sentence).toBe('Ответ дольше 1.5 с у 5% запросов - в течение 2 мин')
    expect(byId['ad/restarts'].sentence).toBe('Больше 3 перезапусков контейнера за час')
    expect(byId['email/oomKilled'].sentence).toBe('Контейнер убит из-за нехватки памяти (OOMKilled) - сразу')
  })

  it('counts the rules of the example (the rules list shows "N of N rules enabled")', () => {
    const all = deriveRules(example())
    const d = addRule(example(), 'currency', { kind: 'slo', threshold: 99.5 })
    expect([all.length, all.filter((r) => r.enabled).length, deriveRules(d).length, deriveRules(d).filter((r) => r.enabled).length]).toEqual([162, 162, 163, 163])
  })

  it('has the rules that need no threshold, and turns them off with their own setting', () => {
    const byId = Object.fromEntries(deriveRules(example()).map((r) => [r.id, r]))
    expect(byId['cart/errorsPerOp'].sentence).toBe('Errors above 5% in any operation for 1 min')
    expect(byId['shipping/latencyPerOp'].sentence).toBe('Latency above 1 s for 5% of requests in any operation for 30 s')
    expect(byId['recommendation/latencyAboveNormal'].sentence).toBe('Responses 5 times slower than normal and above 100 ms for 1 min')
    expect(byId['ad/cpuAboveNormal'].sentence).toBe('CPU 3 times above normal and above 0.5 cores for 2 min')
    expect(byId['ad/memoryLimit']).toBeUndefined() // ad has an explicit memory threshold
    expect(byId['email/memoryLimit'].sentence).toBe('Pod memory above 95% of the limit and still growing for 2 min')
    expect(byId['email/oomKilled'].sentence).toBe('Container killed for running out of memory (OOMKilled), immediately')

    let d = setRule(example(), 'cart/errorsPerOp', { enabled: false })
    expect(d.spec.services.find((s) => s.name === 'cart')?.errorRate).toEqual({ perOperation: false })
    expect(deriveRules(d).find((r) => r.id === 'cart/errorsPerOp')?.enabled).toBe(false)
    d = setRule(d, 'cart/errorsPerOp', { enabled: true })
    expect(d.spec.services.find((s) => s.name === 'cart')?.errorRate).toBeUndefined()

    d = setRule(example(), 'email/oomKilled', { enabled: false })
    expect(d.spec.services.find((s) => s.name === 'email')?.saturation).toEqual({ oomKilledEnabled: false })
    d = setRule(d, 'email/memoryLimit', { threshold: 80 })
    d = setRule(d, 'email/cpuAboveNormal', { enabled: false })
    expect(d.spec.services.find((s) => s.name === 'email')?.saturation).toEqual({ oomKilledEnabled: false, memoryLimitShare: 0.8, cpuAboveNormalFactor: 0 })
    d = setRule(d, 'email/latencyAboveNormal', { threshold: 10 })
    d = setRule(d, 'email/latencyPerOp', { enabled: false })
    expect(d.spec.services.find((s) => s.name === 'email')?.latency).toEqual({ aboveNormalFactor: 10, perOperation: false })
    // An explicit threshold replaces the rules against the limit and the normal.
    d = addRule(example(), 'email', { kind: 'cpu', threshold: 2 })
    const kinds = deriveRules(d).filter((r) => r.service === 'email').map((r) => r.kind)
    expect(kinds).toContain('cpu')
    expect(kinds).not.toContain('cpuAboveNormal')
    expect(kinds).not.toContain('cpuLimit')
  })

  it('writes a changed threshold, duration and severity on the service', () => {
    let d = setRule(example(), 'cart/errors', { threshold: 2, for: '2m', severity: 'warning' })
    expect(d.spec.services.find((s) => s.name === 'cart')?.errorRate).toEqual({ threshold: 0.02, for: '2m', severity: 'warning' })
    expect(deriveRules(d).find((r) => r.id === 'cart/errors')?.sentence).toBe('Errors above 2% for 2 min')
    d = setRule(d, 'cart/errors', { severity: null })
    expect(d.spec.services.find((s) => s.name === 'cart')?.errorRate?.severity).toBeUndefined()
    expect(d.spec.defaults).toEqual(example().spec.defaults)
  })

  it('turns rules off and on without losing thresholds', () => {
    let d = setRule(example(), 'ad/restarts', { enabled: false })
    expect(d.spec.services.find((s) => s.name === 'ad')?.saturation).toEqual({ memory: '400Mi', restartsEnabled: false })
    d = setRule(d, 'ad/restarts', { enabled: true })
    expect(d.spec.services.find((s) => s.name === 'ad')?.saturation).toEqual({ memory: '400Mi' })
    d = setRule(d, 'frontend/latency', { enabled: false })
    expect(d.spec.services.find((s) => s.name === 'frontend')?.latency).toEqual({ thresholdMs: 1500, enabled: false })
  })

  it('adds and removes rules, including custom ones', () => {
    let d = addRule(example(), 'quote', { kind: 'memory', threshold: 256 })
    expect(d.spec.services.find((s) => s.name === 'quote')?.saturation).toEqual({ memory: '256Mi' })
    d = addRule(d, 'quote', { kind: 'custom', custom: { name: 'rule-1', title: 'No quotes', condition: { metric: 'quotes_total', function: 'rate', operator: 'lt', threshold: 0.1 } } })
    const custom = deriveRules(d).find((r) => r.id === 'quote/custom/rule-1')
    expect(custom?.alert).toBe('KamertonCustomRule1')
    expect(custom?.sentence).toBe('No quotes: quotes_total below 0.1 for 2 min')
    d = addRule(d, 'valkey-cart', { kind: 'errors' })
    expect(deriveRules(d).some((r) => r.id === 'valkey-cart/errors')).toBe(true)
    d = removeRule(d, 'quote/custom/rule-1')
    expect(d.spec.services.find((s) => s.name === 'quote')?.customRules).toBeUndefined()
    d = removeRule(d, 'quote/memory')
    expect(d.spec.services.find((s) => s.name === 'quote')?.saturation).toBeUndefined()
  })

  it('maps live alert states to rules', () => {
    const r = deriveRules(example()).find((x) => x.id === 'checkout/opErrors/oteldemo.CheckoutService/PlaceOrder')!
    expect(ruleState(r, undefined)).toBe('unknown')
    expect(ruleState(r, [{ alert: 'KamertonOperationErrorRateHigh', service: 'checkout', state: 'firing' }])).toBe('ok')
    expect(ruleState(r, [{ alert: 'KamertonOperationErrorRateHigh', service: 'checkout', operation: 'oteldemo.CheckoutService/PlaceOrder', state: 'firing' }])).toBe('firing')
  })
})

describe('what goes where', () => {
  it('reads receivers and their routing', () => {
    const dests = deriveDestinations(example())
    expect(dests).toEqual([
      { receiver: 'oncall-telegram', kind: 'telegram', chatID: -1001234567890, emails: undefined, severities: ['critical'], services: [], custom: false },
      { receiver: 'team-email', kind: 'email', chatID: undefined, emails: ['oncall@example.com'], severities: ['critical', 'warning'], services: [], custom: false },
    ])
    expect(receiversFor(dests, 'cart', 'warning').map((x) => x.receiver)).toEqual(['team-email'])
  })

  it('limits a receiver to some services and adds Telegram chats like the core does', () => {
    let d = setDestination(example(), 'oncall-telegram', { severities: ['critical', 'warning'], services: ['checkout'] })
    expect(d.spec.notifications?.routing?.critical).toEqual(['team-email'])
    expect(d.spec.notifications?.routing?.routes).toEqual([{ receiver: 'oncall-telegram', severities: ['critical', 'warning'], services: ['checkout'] }])
    d = addTelegramDestination(d, -100555, { name: 'kamerton-telegram', key: 'bot-token' }, { severities: ['warning'], services: [] })
    expect(d.spec.notifications?.receivers?.at(-1)).toEqual({ name: 'telegram-n100555', telegram: { chatID: -100555, botTokenSecret: { name: 'kamerton-telegram', key: 'bot-token' } } })
    expect(d.spec.notifications?.routing?.warning).toEqual(['team-email', 'telegram-n100555'])
    d = removeDestination(d, 'telegram-n100555')
    expect(d.spec.notifications?.routing?.warning).toEqual(['team-email'])
  })
})

describe('half-typed YAML', () => {
  it('does not accept a service type that is not one of the known ones', () => {
    const p = parseDescriptionYaml(exampleText.replace('type: grpc', 'type: grpc ]'))
    expect(p.ok).toBe(false)
  })
})

describe('adding without a drop point', () => {
  it('keeps the current arrangement and puts the new service under it', () => {
    const d = example()
    const before = positionsOf(d)
    const after = positionsOf(addService(d, { name: 'flagd', type: 'http' }))
    for (const [k, p] of Object.entries(before)) expect(after[k]).toEqual(p)
    expect(after.flagd.y).toBeGreaterThan(Math.max(...Object.values(before).map((p) => p.y)))
  })
})

describe('team notification text', () => {
  it('checks placeholders and markup like the core', async () => {
    const { checkMessageText } = await import('./message')
    expect(checkMessageText('Қате көп: {value}, шек {threshold}')).toBe('')
    expect(checkMessageText('{limit}')).toContain('Unknown placeholder {limit}')
    expect(checkMessageText('a } b')).toContain('Curly braces')
    expect(checkMessageText('<b>x</b>')).toContain('Markup <b>')
    expect(checkMessageText('if < 5')).toBe('')
  })

  it('stores the text on the policy the rule comes from and removes it when emptied', async () => {
    const { getMessage, setMessage } = await import('./message')
    const d0 = example()
    const rule = deriveRules(d0).find((r) => r.id === 'ad/memory')!
    const d1 = setMessage(d0, rule, { title: 'Жад таусылып жатыр', action: ' ' })
    expect(d1.spec.services.find((s) => s.name === 'ad')?.saturation).toEqual({ memory: '400Mi', memoryMessage: { title: 'Жад таусылып жатыр' } })
    expect(getMessage(d1, rule)).toEqual({ title: 'Жад таусылып жатыр' })
    const d2 = setMessage(d1, rule, {})
    expect(d2.spec.services.find((s) => s.name === 'ad')?.saturation).toEqual({ memory: '400Mi' })
    const op = deriveRules(d0).find((r) => r.kind === 'opErrors' && r.service === 'payment')!
    const d3 = setMessage(d0, op, { text: 'Charges are failing: {value}' })
    expect(d3.spec.services.find((s) => s.name === 'payment')?.operations?.[0].errorRate?.message).toEqual({ text: 'Charges are failing: {value}' })
    expect(parseDescriptionYaml(writeDescriptionYaml(exampleText, d3)).ok).toBe(true)
  })
})
