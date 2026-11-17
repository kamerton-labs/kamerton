// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { MutationObserver, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLang } from '@/i18n'
import { noiseFeedbackOptions, type NoiseResponse, type NoiseRule } from '@/lib/api'
import { EditorContext, type EditorState } from '@/lib/editor'
import { applySuggestion, noiseFor, noisyRules, suggestionPatch, suggestionView } from '@/lib/model/noise'
import { addRule, deriveRules, setRule, type RuleItem } from '@/lib/model/rules'
import type { Description } from '@/lib/model/types'
import { parseDescriptionYaml } from '@/lib/model/yaml'
import exampleText from '../../../../testdata/otel-demo/system.yaml?raw'
import { RuleEditor } from './RuleCard'
import RulesView from './RulesView'

const SYSTEM = 'otel-demo'

function example(): Description {
  const p = parseDescriptionYaml(exampleText)
  if (!p.ok) throw new Error(p.error)
  return p.desc
}

const card = (d: Description, id: string): RuleItem => deriveRules(d).find((r) => r.id === id)!

// Shapes pinned to the core: internal/server/noise_test.go (report, feedback answer),
// internal/noise/analyze_test.go (suggestions) and internal/generator/tunable.go (paths, formats).
// The report's service is "api" there; here it is a service of the otel-demo fixture.
const errorsNoisy: NoiseRule = {
  alert: 'KamertonErrorRateHigh',
  service: 'cart',
  operation: '',
  fired: 6,
  short: 6,
  longestSeconds: 60,
  lastFiredAt: '2026-11-17T09:00:00Z',
  feedback: { useful: 1, noise: 5 },
  noisy: true,
  suggestion: { field: 'threshold', current: '0.05', suggested: '0.063', unit: '', wouldFire: 1, wouldMiss: 0, wouldShort: 0, path: 'errorRate.threshold', format: 'share' },
}

const forSuggestion = { field: 'for', current: '1m', suggested: '2m', unit: '', wouldFire: 1, wouldMiss: 0, wouldShort: 0, path: 'errorRate.for', format: 'duration' }

const latencyCalm: NoiseRule = { alert: 'KamertonLatencyHigh', service: 'cart', operation: '', fired: 2, short: 0, longestSeconds: 600, feedback: { useful: 0, noise: 0 }, noisy: false }

const noise = (rules: NoiseRule[], over: Partial<NoiseResponse> = {}): NoiseResponse => ({ window: '7d', available: true, reason: '', shortUnder: '5m', longFrom: '10m', rules, ...over })

const PLACE_ORDER = 'oteldemo.CheckoutService/PlaceOrder'

interface Opts {
  data?: NoiseResponse
  url?: string
  dirty?: boolean
}

function render(desc: Description, el: ReactElement, { data, url = '/', dirty = false }: Opts = {}) {
  const qc = new QueryClient()
  if (data) qc.setQueryData(['system', SYSTEM, 'noise', '7d'], data)
  const state: EditorState = {
    systemId: SYSTEM,
    draft: { text: exampleText, saved: dirty ? '' : exampleText, error: null, desc } as EditorState['draft'],
    desc,
    readOnly: false,
    edit: () => {},
    destinations: [],
    issues: [],
  }
  return renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[url]}>
        <EditorContext value={state}>{el}</EditorContext>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const count = (html: string, s: string) => html.split(s).length - 1

afterEach(() => {
  setLang('en', false)
  vi.unstubAllGlobals()
})

describe('rules list', () => {
  it('marks noisy rules, offers the filter with the count and a summary', () => {
    const html = render(example(), <RulesView />, { data: noise([errorsNoisy, latencyCalm]) })
    expect(count(html, 'data-testid="rule-noisy"')).toBe(1)
    const row = html.slice(html.indexOf('data-testid="rule-noisy"') - 400, html.indexOf('data-testid="rule-noisy"'))
    expect(row).toContain('>cart<')
    expect(row).toContain('>Errors<span title=')
    expect(html).toMatch(/data-testid="rules-filter-noisy"[^>]*>Noisy <span[^>]*>1<\/span>/)
    expect(html).toContain('1 rule is noisy this week')
    expect(html).toMatch(/data-testid="rules-noisy-summary" href="\/\?noisy=1"/)
  })

  it('looks as today when nothing is noisy, when nothing was measured and when the core cannot measure', () => {
    for (const data of [undefined, noise([latencyCalm]), noise([], { available: false, reason: 'Prometheus is not connected' })]) {
      const html = render(example(), <RulesView />, { data })
      expect(html).not.toContain('rule-noisy')
      expect(html).not.toContain('rules-filter-noisy')
      expect(html).not.toContain('Prometheus is not connected')
    }
  })

  it('shows only noisy rules with the filter on', () => {
    const html = render(example(), <RulesView />, { data: noise([errorsNoisy]), url: '/?noisy=1' })
    expect(count(html, 'data-testid="rule-noisy"')).toBe(1)
    expect(html).not.toContain('frontend-proxy')
    expect(html).not.toContain('data-testid="rule-system"')
    expect(html).not.toContain('rules-noisy-summary')
    expect(html).toMatch(/aria-pressed="true"[^>]*data-testid="rules-filter-noisy"/)
    expect(html).toContain('>Reset<')
  })

  it('follows the core flag: a rule that is off still counts, another operation does not', () => {
    const rules = deriveRules(example())
    expect(noisyRules(rules, [{ ...errorsNoisy, operation: 'GetCart' }])).toHaveLength(0)
    expect(noisyRules(rules.map((r) => (r.id === 'cart/errors' ? { ...r, enabled: false } : r)), [errorsNoisy])).toHaveLength(1)
    expect(noisyRules(rules, [{ ...errorsNoisy, noisy: false }])).toHaveLength(0)
    expect(noisyRules(rules, [errorsNoisy]).map((r) => r.id)).toEqual(['cart/errors'])
  })

  it('explains "noisy" on the marker', () => {
    expect(render(example(), <RulesView />, { data: noise([errorsNoisy]) })).toContain(
      'title="Fired at least 5 times with most cleared within 5 min, or people marked it as noise more often than useful"',
    )
  })
})

describe('noise block in the card', () => {
  it('shows the counts, a share threshold in percent with Apply, and the marks', () => {
    const d = example()
    const html = render(d, <RuleEditor rule={card(d, 'cart/errors')} />, { data: noise([errorsNoisy]) })
    expect(html).toContain('data-testid="rule-noise"')
    expect(html).toContain('Fired 6 times in 7 days, 6 cleared within 5 min')
    expect(html).toContain('Threshold 6.3% instead of 5%: would have fired 1 time and missed none that lasted 10 min or more.')
    expect(html).toContain('data-testid="rule-noise-apply"')
    expect(html).toContain('marked as noise 5 times · marked as useful 1 time')
    expect(html).toContain('data-testid="noise-feedback-useful"')
    expect(html.indexOf('data-testid="rule-noise"')).toBeGreaterThan(html.indexOf('Backtest'))
  })

  it('shows the counts without a suggestion and stays away from quiet rules', () => {
    const d = example()
    const plain = render(d, <RuleEditor rule={card(d, 'cart/latency')} />, { data: noise([{ ...latencyCalm, feedback: { useful: 2, noise: 0 } }]) })
    expect(plain).toContain('Fired 2 times in 7 days')
    expect(plain).not.toContain('cleared within')
    expect(plain).not.toContain('rule-noise-suggestion')
    expect(plain).not.toContain('rule-noise-apply')
    expect(plain).toContain('marked as useful 2 times')
    expect(render(d, <RuleEditor rule={card(d, 'cart/memoryLimit')} />, { data: noise([errorsNoisy]) })).not.toContain('rule-noise')
  })

  it('shows a rule known only by its verdicts as not fired', () => {
    const d = example()
    const html = render(d, <RuleEditor rule={card(d, 'cart/latency')} />, { data: noise([{ ...latencyCalm, fired: 0, feedback: { useful: 0, noise: 1 } }]) })
    expect(html).toContain('Did not fire in 7 days.')
    expect(html).toContain('marked as noise 1 time')
  })

  it('shows raw numbers without Apply for an unknown format or a field the card does not have', () => {
    const d = example()
    const unknown = render(d, <RuleEditor rule={card(d, 'cart/errors')} />, {
      data: noise([{ ...errorsNoisy, suggestion: { ...errorsNoisy.suggestion, format: 'ratio' } }]),
    })
    expect(unknown).toContain('Threshold 0.063 instead of 0.05:')
    expect(unknown).not.toContain('rule-noise-apply')
    const elsewhere = render(d, <RuleEditor rule={card(d, 'cart/latencyPerOp')} />, {
      data: noise([{ alert: 'KamertonOperationLatencyHigh', service: 'cart', operation: '', fired: 7, noisy: true, suggestion: { ...forSuggestion, path: 'latency.for' } }]),
    })
    expect(elsewhere).toContain('Holds for at least 2 min instead of 1 min')
    expect(elsewhere).not.toContain('rule-noise-apply')
  })

  it('says the reason once when the core cannot measure, without an error', () => {
    const d = example()
    const html = render(d, <RuleEditor rule={card(d, 'cart/errors')} />, { data: noise([errorsNoisy], { available: false, reason: 'Prometheus did not answer: connection refused' }) })
    expect(count(html, 'Prometheus did not answer: connection refused')).toBe(1)
    expect(html).toContain('data-testid="rule-noise-unavailable"')
    expect(html).not.toContain('data-testid="rule-noise"')
    expect(html).not.toContain('text-destructive')
  })

  it('renders in Russian', () => {
    setLang('ru', false)
    const d = example()
    const html = render(d, <RuleEditor rule={card(d, 'cart/errors')} />, { data: noise([{ ...errorsNoisy, fired: 40, short: 37 }]) })
    expect(html).toContain('Как часто срабатывает')
    expect(html).toContain('Сработало 40 раз за 7 дней, из них 37 прошли сами меньше чем за 5 мин')
    expect(html).toContain('Порог 6.3% вместо 5%: сработало бы 1 раз, не пропустило бы ни одного срабатывания от 10 мин и дольше.')
    expect(html).toContain('>Применить<')
    expect(html).toContain('>По делу<')
    expect(html).toContain('>Шум<')
    expect(html).toContain('отмечено как шум 5 раз · отмечено «по делу» 1 раз')
    expect(render(d, <RulesView />, { data: noise([errorsNoisy]) })).toContain('За неделю шумит 1 правило')
  })
})

describe('Apply', () => {
  const cart = (d: Description) => d.spec.services.find((s) => s.name === 'cart')!

  it('writes a share threshold where the manual edit does: the service, not defaults', () => {
    const d = example()
    expect(cart(d).errorRate).toBeUndefined()
    const next = applySuggestion(d, card(d, 'cart/errors'), errorsNoisy.suggestion)
    expect(card(next, 'cart/errors').threshold).toBe(6.3)
    expect(cart(next).errorRate?.threshold).toBe(0.063)
    expect(next.spec.defaults?.errorRate?.threshold).toBe(0.05)
    expect(next).toEqual(setRule(d, 'cart/errors', { threshold: 6.3 }))
    const html = render(next, <RuleEditor rule={card(next, 'cart/errors')} />, { data: noise([errorsNoisy]), dirty: true })
    expect(html).toContain('Set in the card. Save to make it work.')
    expect(html).not.toContain('rule-noise-apply')
  })

  it('writes a "for" duration into an error-rate card and the shared saturation "for" of a memory card', () => {
    const d = example()
    const errors = applySuggestion(d, card(d, 'cart/errors'), forSuggestion)
    expect(card(errors, 'cart/errors').for).toBe('2m')
    expect(cart(errors).errorRate?.for).toBe('2m')
    const memory = applySuggestion(d, card(d, 'cart/memoryLimit'), { ...forSuggestion, current: '2m', suggested: '10m', path: 'saturation.for' })
    expect(card(memory, 'cart/memoryLimit').for).toBe('10m')
    expect(cart(memory).saturation?.for).toBe('10m')
    const html = render(errors, <RuleEditor rule={card(errors, 'cart/errors')} />, { data: noise([{ ...errorsNoisy, suggestion: forSuggestion }]), dirty: true })
    expect(html).toContain('Holds for at least 2 min instead of 1 min: would have fired 1 time and missed none that lasted 10 min or more.')
    expect(html).toContain('data-testid="rule-noise-applied"')
  })

  it('writes a memory quantity in MiB', () => {
    const d = addRule(example(), 'cart', { kind: 'memory' })
    const s = { field: 'threshold', current: '512Mi', suggested: '768Mi', unit: '', wouldFire: 2, wouldMiss: 0, path: 'saturation.memory', format: 'quantity' }
    expect(suggestionView(card(d, 'cart/memory'), s)).toMatchObject({ current: '512 MiB', suggested: '768 MiB', patch: { threshold: 768 } })
    expect(cart(applySuggestion(d, card(d, 'cart/memory'), s)).saturation?.memory).toBe('768Mi')
  })

  it('writes a millisecond threshold of an operation rule into that operation', () => {
    const d = addRule(example(), 'checkout', { kind: 'opLatency', operation: PLACE_ORDER, threshold: 300 })
    const rule = card(d, `checkout/opLatency/${PLACE_ORDER}`)
    const s = { field: 'threshold', current: '300', suggested: '450', unit: 'ms', wouldFire: 1, wouldMiss: 0, path: `operations[${PLACE_ORDER}].latency.thresholdMs`, format: 'number' }
    expect(suggestionView(rule, s)).toMatchObject({ current: '300 ms', suggested: '450 ms' })
    const next = applySuggestion(d, rule, s)
    const op = next.spec.services.find((x) => x.name === 'checkout')!.operations!.find((o) => o.name === PLACE_ORDER)!
    expect(op.latency?.thresholdMs).toBe(450)
    expect(next.spec.services.find((x) => x.name === 'checkout')?.latency).toEqual(d.spec.services.find((x) => x.name === 'checkout')?.latency)
    expect(suggestionPatch(rule, { ...s, path: 'latency.thresholdMs' })).toBeNull()
  })

  it('writes a share threshold of an operation rule that has its own policy', () => {
    const d = example()
    const rule = card(d, `checkout/opErrors/${PLACE_ORDER}`)
    const s = { ...errorsNoisy.suggestion, current: '0.01', suggested: '0.013', path: `operations[${PLACE_ORDER}].errorRate.threshold` }
    const op = applySuggestion(d, rule, s).spec.services.find((x) => x.name === 'checkout')!.operations!.find((o) => o.name === PLACE_ORDER)!
    expect(op.errorRate?.threshold).toBe(0.013)
  })

  it('covers every path of tunable.go with the card that edits it', () => {
    const d = addRule(addRule(example(), 'cart', { kind: 'memory' }), 'cart', { kind: 'cpu' })
    const cases: [string, string, string, string, string | number][] = [
      ['cart/errorsPerOp', 'errorRate.threshold', 'share', '0.1', 10],
      ['cart/latency', 'latency.thresholdMs', 'number', '1300', 1300],
      ['cart/latencyPerOp', 'latency.thresholdMs', 'number', '1300', 1300],
      ['cart/latencyAboveNormal', 'latency.aboveNormalFactor', 'number', '7.5', 7.5],
      ['cart/traffic', 'trafficDrop.for', 'duration', '3m', '3m'],
      ['cart/noReadyPods', 'noReadyPods.for', 'duration', '1m', '1m'],
      ['cart/restarts', 'saturation.maxRestartsPerHour', 'integer', '5', 5],
      ['cart/memory', 'saturation.for', 'duration', '5m', '5m'],
      ['cart/cpu', 'saturation.cpuCores', 'number', '1.5', 1.5],
      ['cart/cpu', 'saturation.for', 'duration', '5m', '5m'],
    ]
    for (const [id, path, format, suggested, want] of cases) {
      const rule = card(d, id)
      expect(rule, id).toBeDefined()
      const field = format === 'duration' ? 'for' : 'threshold'
      expect(suggestionPatch(rule, { field, suggested, path, format }), `${id} ${path}`).toEqual({ [field]: want })
    }
    const sat = addRule(example(), 'cart', { kind: 'custom', custom: { name: 'free-disk', title: 'Little free disk', condition: { metric: 'node_free_bytes', operator: 'lt', threshold: 100 } } })
    expect(suggestionPatch(card(sat, 'cart/custom/free-disk'), { field: 'threshold', suggested: '80', path: 'customRules[free-disk].condition.threshold', format: 'number' })).toEqual({ threshold: 80 })
    expect(suggestionPatch(card(sat, 'cart/custom/free-disk'), { field: 'for', suggested: '3m', path: 'customRules[free-disk].for', format: 'duration' })).toEqual({ for: '3m' })
    const e = example()
    const builtin: [string, string, string, string, string | number][] = [
      ['cart/memoryLimit', 'saturation.memoryLimitShare', 'share', '0.97', 97],
      ['cart/cpuLimit', 'saturation.cpuLimitShare', 'share', '0.95', 95],
      ['cart/cpuAboveNormal', 'saturation.cpuAboveNormalFactor', 'number', '4.5', 4.5],
      ['orders/queueLag', 'queue.maxLag', 'number', '1300', 1300],
      ['orders/queueLag', 'queue.for', 'duration', '5m', '5m'],
      [`checkout/opErrors/${PLACE_ORDER}`, `operations[${PLACE_ORDER}].errorRate.for`, 'duration', '2m', '2m'],
    ]
    for (const [id, path, format, suggested, want] of builtin) {
      const field = format === 'duration' ? 'for' : 'threshold'
      expect(suggestionPatch(card(e, id), { field, suggested, path, format }), `${id} ${path}`).toEqual({ [field]: want })
    }
    expect(suggestionPatch(card(d, 'cart/restarts'), { field: 'threshold', suggested: '4.5', path: 'saturation.maxRestartsPerHour', format: 'integer' })).toBeNull()
  })

  it('adds up the two alerts of an SLO card', () => {
    const slo = { ...card(example(), 'cart/errors'), kind: 'slo' as const, alerts: ['KamertonSLOBurnRateFast', 'KamertonSLOBurnRateSlow'] }
    const n = noiseFor(slo, [
      { alert: 'KamertonSLOBurnRateFast', service: 'cart', fired: 3, short: 1, feedback: { noise: 1 } },
      { alert: 'KamertonSLOBurnRateSlow', service: 'cart', fired: 5, short: 4, noisy: true, feedback: { noise: 2 } },
    ])
    expect(n).toMatchObject({ alert: 'KamertonSLOBurnRateSlow', fired: 8, short: 5, noisy: true, feedback: { noise: 3, useful: 0 } })
  })
})

describe('feedback', () => {
  const key = ['system', SYSTEM, 'noise', '7d']
  const opRule: NoiseRule = { alert: 'KamertonOperationErrorRateHigh', service: 'checkout', operation: PLACE_ORDER, fired: 5, feedback: { useful: 0, noise: 0 } }

  function setup(respond: () => Promise<Response>) {
    const qc = new QueryClient()
    qc.setQueryData(key, noise([errorsNoisy, latencyCalm, opRule]))
    const fetch = vi.fn<(url: string, init?: RequestInit) => Promise<Response>>(() => respond())
    vi.stubGlobal('fetch', fetch)
    const m = new MutationObserver(qc, noiseFeedbackOptions(qc, SYSTEM))
    return { qc, fetch, m }
  }

  const fb = (qc: QueryClient, i = 0) => (qc.getQueryData(key) as NoiseResponse).rules?.[i].feedback
  const answer = (body: object) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

  it('posts the verdict, counts it at once and takes the totals the core answers', async () => {
    let done: (r: Response) => void = () => {}
    const { qc, fetch, m } = setup(() => new Promise((r) => (done = r)))
    const p = m.mutate({ alert: 'KamertonErrorRateHigh', service: 'cart', operation: '', verdict: 'noise' })
    await vi.waitFor(() => expect(fetch).toHaveBeenCalled())
    expect(fb(qc)).toEqual({ useful: 1, noise: 6 })
    expect(fb(qc, 1)).toEqual({ useful: 0, noise: 0 })
    const [url, init] = fetch.mock.calls[0]
    expect(url).toBe('/api/v1/systems/otel-demo/noise/feedback')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(String(init?.body))).toEqual({ alert: 'KamertonErrorRateHigh', service: 'cart', verdict: 'noise' })
    done(answer({ recorded: true, alert: 'KamertonErrorRateHigh', service: 'cart', operation: '', feedback: { useful: 2, noise: 9 } }))
    await p
    expect(fb(qc)).toEqual({ useful: 2, noise: 9 })
  })

  it('sends the operation of a rule written for one operation', async () => {
    const { qc, fetch, m } = setup(() =>
      Promise.resolve(answer({ recorded: true, alert: 'KamertonOperationErrorRateHigh', service: 'checkout', operation: PLACE_ORDER, feedback: { useful: 0, noise: 1 } })),
    )
    await m.mutate({ alert: 'KamertonOperationErrorRateHigh', service: 'checkout', operation: PLACE_ORDER, verdict: 'noise' })
    expect(JSON.parse(String(fetch.mock.calls[0][1]?.body))).toEqual({ alert: 'KamertonOperationErrorRateHigh', service: 'checkout', operation: PLACE_ORDER, verdict: 'noise' })
    expect(fb(qc, 2)).toEqual({ useful: 0, noise: 1 })
  })

  it('takes the vote back when the core refuses it', async () => {
    const { qc, m } = setup(() => Promise.resolve(new Response(JSON.stringify({ error: 'no such service in this system: nope', code: 'bad_request' }), { status: 400 })))
    await expect(m.mutate({ alert: 'KamertonErrorRateHigh', service: 'cart', verdict: 'useful' })).rejects.toThrow('no such service')
    expect(fb(qc)).toEqual({ useful: 1, noise: 5 })
  })
})
