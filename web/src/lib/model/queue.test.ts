// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { hintRu } from '@/i18n/schema-ru'
import { defaultTitle, fixedThreshold, getMessage, notificationLang, setMessage, setNotificationLang, thresholdText } from './message'
import { addRule, addableKinds, deriveRules, removeRule, setRule } from './rules'
import type { Description } from './types'
import { parseDescriptionYaml, writeDescriptionYaml } from './yaml'

const exampleText = readFileSync(new URL('../../../../testdata/otel-demo/system.yaml', import.meta.url), 'utf8')

function example(): Description {
  const p = parseDescriptionYaml(exampleText)
  if (!p.ok) throw new Error(p.error)
  return p.desc
}

const stopped = (d: Description) => deriveRules(d).find((r) => r.id === 'orders/queueStopped')
const queue = (d: Description) => d.spec.services.find((s) => s.name === 'orders')!.queue!

describe('rule "nobody reads the queue"', () => {
  it('is on next to the queue lag rule, without a threshold (mirrors the generator)', () => {
    const d = example()
    const r = stopped(d)!
    expect(r).toMatchObject({
      kind: 'queueStopped',
      enabled: true,
      for: '',
      severity: 'warning',
      severityInherited: true,
      removable: false,
      alert: 'KamertonQueueConsumerStopped',
      alerts: ['KamertonQueueConsumerStopped'],
    })
    expect(r.threshold).toBeNaN()
    expect(r.sentence).toBe('Nobody reads queue orders: no consumers in the group for 1 min or the offset stands still for 3 min with unread messages for 1 min')
    expect(deriveRules(d).filter((x) => x.kind === 'queueStopped').map((x) => x.service)).toEqual(['orders'])
    expect(deriveRules(d)).toHaveLength(162)
  })

  it('takes the shorter pending period and the queue severity', () => {
    const d = example()
    queue(d).for = '30s'
    queue(d).severity = 'critical'
    expect(stopped(d)!.sentence.endsWith(' for 30 s')).toBe(true)
    expect(stopped(d)!).toMatchObject({ severity: 'critical', severityInherited: false })
  })

  it('is turned off and on with consumerStoppedEnabled; only false is written', () => {
    let d = setRule(example(), 'orders/queueStopped', { enabled: false })
    expect(queue(d).consumerStoppedEnabled).toBe(false)
    expect(stopped(d)!.enabled).toBe(false)
    expect(writeDescriptionYaml(exampleText, d)).toContain('consumerStoppedEnabled: false')
    expect(deriveRules(d).find((r) => r.id === 'orders/queueLag')!.enabled).toBe(true)
    d = setRule(d, 'orders/queueStopped', { enabled: true })
    expect(queue(d)).not.toHaveProperty('consumerStoppedEnabled')
    expect(stopped(d)!.enabled).toBe(true)
    // Removing a built-in rule turns it off; adding turns it back on.
    d = removeRule(d, 'orders/queueStopped')
    expect(stopped(d)!.enabled).toBe(false)
    d = addRule(d, 'orders', { kind: 'queueStopped' })
    expect(stopped(d)!.enabled).toBe(true)
    expect(queue(d)).not.toHaveProperty('consumerStoppedEnabled')
    // A threshold or a pending period cannot be set on it.
    d = setRule(d, 'orders/queueStopped', { threshold: 5, for: '10m' })
    expect(queue(d).maxLag).toBe(100)
    expect(queue(d).for).toBe('2m')
    expect(addableKinds(d, 'orders')).not.toContain('queueStopped')
  })

  it('has its own notification text', () => {
    const d = example()
    const r = stopped(d)!
    const next = setMessage(d, r, { title: 'Nobody reads {service}', text: '' })
    expect(queue(next).consumerStoppedMessage).toEqual({ title: 'Nobody reads {service}' })
    expect(queue(next).message).toBeUndefined()
    expect(getMessage(next, r)).toEqual({ title: 'Nobody reads {service}' })
    expect(queue(setMessage(next, r, {})).consumerStoppedMessage).toBeUndefined()
  })

  it('previews default texts in the language of the notifications (internal/rules/localize.go)', () => {
    const d = example()
    const r = stopped(d)!
    expect(defaultTitle('queueStopped', 'en')).toBe('Nobody reads the queue')
    expect(defaultTitle('queueStopped', 'ru')).toBe('Очередь перестали читать')
    expect(defaultTitle('custom', 'en')).toBe('')
    expect(fixedThreshold('queueStopped', 'en')).toBe('no consumers in the group for 1m or the group offset stands still for 3m with unread messages')
    expect(fixedThreshold('queueStopped', 'ru')).toContain('нет потребителей 1m')
    expect(thresholdText(r, 'en')).toBe(fixedThreshold('queueStopped', 'en'))
    const lag = deriveRules(d).find((x) => x.id === 'orders/queueLag')!
    expect(thresholdText(lag, 'en')).toBe('100 messages')
    expect(thresholdText(lag, 'ru')).toBe('100 сообщ.')
  })

  it('reads and writes the notification language; English is the default', () => {
    const d = example()
    expect(notificationLang(d)).toBe('en')
    const ru = setNotificationLang(d, 'ru')
    expect(ru.spec.notifications?.language).toBe('ru')
    expect(notificationLang(ru)).toBe('ru')
    const back = setNotificationLang(ru, 'en')
    expect(back.spec.notifications?.language).toBeUndefined()
    expect(writeDescriptionYaml(exampleText, ru)).toContain('language: ru')
  })

  it('has Russian hints for its fields', () => {
    expect(hintRu(['spec', 'services', '*', 'queue', 'consumerStoppedEnabled'])).toContain('false - выключить')
    expect(hintRu(['spec', 'services', '*', 'queue', 'consumerStoppedMessage'])).toBeTruthy()
    expect(hintRu(['spec', 'telemetry', 'queues', 'membersMetric'])).toContain('участников')
    expect(hintRu(['spec', 'telemetry', 'queues', 'offsetMetric'])).toContain('смещения')
  })
})
