// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it } from 'vitest'
import { setLang, tl, type MsgKey } from '@/i18n'
import { defaultMessage, getMessage, promDuration, setMessage } from './message'
import { addRule, addableKinds, deriveRules, generatedCount, removeRule, setRule, SYSTEM_RULES } from './rules'
import type { Description, Service } from './types'
import { parseDescriptionYaml } from './yaml'

const exampleText = readFileSync(new URL('../../../../testdata/otel-demo/system.yaml', import.meta.url), 'utf8')

function example(): Description {
  const p = parseDescriptionYaml(exampleText)
  if (!p.ok) throw new Error(p.error)
  return p.desc
}

function shop(services: Service[], spec: Partial<Description['spec']> = {}): Description {
  return { apiVersion: 'kamerton.dev/v1alpha1', kind: 'SystemDescription', metadata: { name: 'shop' }, spec: { services, ...spec } }
}

const find = (d: Description, id: string) => deriveRules(d).find((r) => r.id === id)

afterEach(() => setLang('en', false))

describe('rule count', () => {
  it('equals what the core generates for the demo description', () => {
    const rules = deriveRules(example())
    // `kamerton generate -f testdata/otel-demo/system.yaml` makes 195 rules.
    expect(generatedCount(rules)).toBe(195)
    expect(rules.filter((r) => r.kind === 'noReadyPods' && r.enabled)).toHaveLength(16)
    expect(rules.filter((r) => r.kind === 'noData' && r.enabled)).toHaveLength(14)
    expect(rules.filter((r) => r.kind === 'systemNoData' && r.enabled)).toHaveLength(1)
    // The SLO card stands for the fast and the slow burn-rate rules.
    expect(rules.filter((r) => r.enabled).length).toBe(193)
  })
})

describe('system no-data rule', () => {
  it('is the first rule, belongs to no service and is always critical', () => {
    const [first] = deriveRules(example())
    expect(first).toMatchObject({ id: `${SYSTEM_RULES}/systemNoData`, service: '', system: true, severity: 'critical', severityFixed: true, for: '3m', alert: 'KamertonTelemetryMissing' })
    expect(first.sentence).toBe('No span metrics from any service for over 3 min at an hour that normally has traffic: other alerts of the system cannot fire')
  })

  it('turns off, edits after and keeps its message in spec.noData', () => {
    let d = setRule(example(), `${SYSTEM_RULES}/systemNoData`, { enabled: false })
    expect(d.spec.noData).toEqual({ enabled: false })
    expect(find(d, `${SYSTEM_RULES}/systemNoData`)?.enabled).toBe(false)
    d = setRule(d, `${SYSTEM_RULES}/systemNoData`, { enabled: true, for: '5m' })
    expect(d.spec.noData).toEqual({ after: '5m' })
    const rule = find(d, `${SYSTEM_RULES}/systemNoData`)!
    d = setMessage(d, rule, { action: 'Call the platform team' })
    expect(getMessage(d, rule)).toEqual({ action: 'Call the platform team' })
    expect(removeRule(d, rule.id).spec.noData?.enabled).toBe(false)
  })

  it('is not generated when span metrics are turned off', () => {
    const d = shop([{ name: 'api', type: 'http' }, { name: 'web', type: 'http' }], { telemetry: { spans: { callsMetric: '-' } } })
    expect(deriveRules(d).some((r) => r.kind === 'systemNoData' || r.kind === 'noData')).toBe(false)
  })
})

describe('per-service no-data rule', () => {
  it('is on for http and grpc, needs another service and follows the requests-stopped severity', () => {
    const d = shop([
      { name: 'api', type: 'http', critical: true },
      { name: 'web', type: 'grpc' },
      { name: 'db', type: 'database' },
    ])
    expect(find(d, 'api/noData')).toMatchObject({ enabled: true, severity: 'critical', severityInherited: true, severityDefault: 'critical', for: '3m' })
    // No traffic rule for a non-critical service: warning.
    expect(find(d, 'web/noData')).toMatchObject({ enabled: true, severity: 'warning' })
    expect(find(d, 'db/noData')).toBeUndefined()
    expect(deriveRules(shop([{ name: 'api', type: 'http' }])).some((r) => r.kind === 'noData')).toBe(false)
    // Two services with the same name in telemetry are one source of data.
    expect(deriveRules(shop([{ name: 'api', type: 'http' }, { name: 'api-v2', type: 'http', telemetryName: 'api' }])).some((r) => r.kind === 'noData')).toBe(false)
  })

  it('says on the requests-stopped card that it reports a complete stop while no-data stays silent', () => {
    const d = shop([{ name: 'api', type: 'http', critical: true }, { name: 'web', type: 'http' }])
    expect(find(d, 'api/traffic')?.note).toBe('A complete stop is reported here even after the series vanish; the "No data" rule stays silent meanwhile.')
    expect(find(setRule(d, 'api/noData', { enabled: false }), 'api/traffic')?.note).toBeUndefined()
  })

  it('is added to a queue only on request and removed from it again', () => {
    const d = shop([{ name: 'api', type: 'http' }, { name: 'orders', type: 'queue' }], { defaults: { noData: { severity: 'critical' } } })
    expect(addableKinds(d, 'orders')).toContain('noData')
    expect(addableKinds(d, 'api')).not.toContain('noData')
    let n = addRule(d, 'orders', { kind: 'noData', for: '10m' })
    expect(n.spec.services[1].noData).toEqual({ after: '10m' })
    expect(find(n, 'orders/noData')).toMatchObject({ enabled: true, severity: 'critical', removable: true })
    n = setRule(n, 'orders/noData', { for: '3m' })
    n = setRule(n, 'orders/noData', { enabled: false })
    expect(find(n, 'orders/noData')?.enabled).toBe(false)
    expect(removeRule(n, 'orders/noData').spec.services[1].noData).toBeUndefined()
  })

  it('edits after and severity on the service and turns off through it', () => {
    let d = setRule(example(), 'cart/noData', { for: '10m', severity: 'warning' })
    expect(d.spec.services.find((s) => s.name === 'cart')?.noData).toEqual({ after: '10m', severity: 'warning' })
    d = setRule(d, 'cart/noData', { enabled: false })
    expect(find(d, 'cart/noData')?.enabled).toBe(false)
  })

  it('previews the core default text with the window in Prometheus notation', () => {
    const rule = find(setRule(example(), 'cart/noData', { for: '180s' }), 'cart/noData')!
    expect(promDuration('180s')).toBe('3m')
    expect(promDuration('90m')).toBe('1h30m')
    expect(defaultMessage(rule, 'en')?.text).toContain('it normally has traffic at this hour but has sent no span metrics for 3m, while other services still do')
    expect(defaultMessage(rule, 'ru')?.action).toContain('экспортёр OpenTelemetry')
  })
})

describe('no-ready-pods rule', () => {
  it('is on for every service with a 30s pending period', () => {
    const r = find(example(), 'orders/noReadyPods')!
    expect(r).toMatchObject({ enabled: true, for: '30s', severity: 'warning', alert: 'KamertonNoReadyPods' })
    expect(r.sentence).toBe('No pod of the service is ready, or all pods are gone within 1 h of the last ready one for 30 s')
    expect(find(example(), 'payment/noReadyPods')?.severity).toBe('critical')
  })

  it('edits the pending period and is not generated without the readiness metric', () => {
    const d = setRule(example(), 'cart/noReadyPods', { for: '1m', enabled: false })
    expect(d.spec.services.find((s) => s.name === 'cart')?.noReadyPods).toEqual({ for: '1m', enabled: false })
    const off = example()
    off.spec.telemetry = { ...off.spec.telemetry, containers: { readyMetric: '-' } }
    expect(deriveRules(off).some((r) => r.kind === 'noReadyPods')).toBe(false)
  })

  it('follows defaults', () => {
    const d = example()
    d.spec.defaults = { ...d.spec.defaults, noReadyPods: { enabled: false } }
    expect(deriveRules(d).filter((r) => r.kind === 'noReadyPods' && r.enabled)).toHaveLength(0)
    expect(setRule(d, 'cart/noReadyPods', { enabled: true }).spec.services.find((s) => s.name === 'cart')?.noReadyPods).toEqual({ enabled: true })
  })
})

// Go string literals joined across "..." + "..." lines, with \" unescaped.
const goText = (rel: string) =>
  readFileSync(new URL(`../../../../internal/${rel}`, import.meta.url), 'utf8')
    .replace(/"\s*\+\s*\n\s*"/g, '')
    .replace(/\\"/g, '"')

describe('texts mirrored from the core', () => {
  const messages = goText('generator/rulemessage.go')
  const hints = goText('generator/runbook.go')

  it('match the default no-data messages of internal/generator/rulemessage.go', () => {
    for (const lang of ['en', 'ru'] as const)
      for (const key of ['msg.default.noData.text', 'msg.default.systemNoData.text'] as const satisfies MsgKey[]) expect(messages).toContain(`text: "${tl(lang, key)}"`)
  })

  it('match the no-data runbook advice of internal/generator/runbook.go', () => {
    for (const lang of ['en', 'ru'] as const)
      for (const key of ['runbook.adviceAlert.telemetryMissing', 'runbook.adviceAlert.serviceTelemetryMissing'] as const satisfies MsgKey[])
        expect(hints).toContain(`: "${tl(lang, key).replace(/\n/g, ' ')}"`)
  })

  it('say that no data counts only at hours with traffic, and that requests stopped covers a complete stop', () => {
    expect(tl('en', 'rule.hint.noData')).toContain('normally has traffic at this hour')
    expect(tl('en', 'rule.hint.traffic')).toContain('also after the series vanish')
    expect(tl('ru', 'rule.note.trafficNoData')).toContain('«Нет данных» в это время молчит')
    expect(tl('en', 'rule.note.trafficNoData')).not.toContain('{after}')
  })
})
