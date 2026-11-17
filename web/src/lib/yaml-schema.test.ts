// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from '@/i18n'
import { contextAt, hoverAt, pointerToRange, schemaAt, suggest, syntaxErrors, type SchemaNode } from './yaml-schema'

const schema = JSON.parse(readFileSync(new URL('../../../internal/schema/systemdescription.schema.json', import.meta.url), 'utf8')) as SchemaNode
const example = readFileSync(new URL('../../../testdata/otel-demo/system.yaml', import.meta.url), 'utf8')

/** Text with "|" marking the cursor. */
function at(src: string) {
  const pos = src.indexOf('|')
  return { doc: src.slice(0, pos) + src.slice(pos + 1), pos }
}

const base = `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata:
  name: shop
spec:
  services:
    - name: api
      type: http
`

describe('cursor context', () => {
  it('finds the mapping of a service item and its existing keys', () => {
    const { doc, pos } = at(base + '      cri|\n')
    const ctx = contextAt(doc, pos)
    expect(ctx).toMatchObject({ path: ['spec', 'services', '*'], kind: 'key', prefix: 'cri' })
    expect(ctx.siblings).toEqual(expect.arrayContaining(['name', 'type']))
  })

  it('knows a new list item and a nested policy', () => {
    expect(contextAt(...Object.values(at(base + '    - |\n')) as [string, number]).path).toEqual(['spec', 'services', '*'])
    expect(contextAt(...Object.values(at(base + '      errorRate:\n        |\n')) as [string, number]).path).toEqual(['spec', 'services', '*', 'errorRate'])
    expect(contextAt(...Object.values(at(base + '|')) as [string, number]).path).toEqual([])
    expect(contextAt(...Object.values(at(base + '  |')) as [string, number]).path).toEqual(['spec'])
  })

  it('works in a half-typed document that does not parse', () => {
    const { doc, pos } = at(base + '      dependsOn: [\n      lat|')
    expect(contextAt(doc, pos).path).toEqual(['spec', 'services', '*'])
  })
})

describe('schema-driven completion', () => {
  afterEach(() => setLang('en', false))

  it('offers the fields of a service that are not there yet, with the schema hints', () => {
    const { doc, pos } = at(base + '      |\n')
    const { items } = suggest(schema, doc, pos)
    const labels = items.map((i) => i.label)
    expect(labels).toContain('critical')
    expect(labels).toContain('customRules')
    expect(labels).not.toContain('name')
    expect(labels).not.toContain('type')
    expect(items.find((i) => i.label === 'critical')?.info).toContain('Critical services page on-call')
    expect(items.find((i) => i.label === 'critical')?.detail).toBe('true/false')
    expect(items.find((i) => i.label === 'errorRate')?.apply).toBe('errorRate:')
  })

  it('uses the Russian hints and type names in Russian', () => {
    setLang('ru', false)
    const { doc, pos } = at(base + '      |\n')
    const { items } = suggest(schema, doc, pos)
    expect(items.find((i) => i.label === 'critical')?.info).toContain('Важный сервис')
    expect(items.find((i) => i.label === 'critical')?.detail).toBe('да/нет')
    expect(hoverAt(schema, example, example.indexOf('critical: true') + 3)?.text).toContain('Важный сервис')
  })

  it('offers enum values with the field hint', () => {
    const { doc, pos } = at(base.replace('type: http', 'type: |'))
    const { ctx, items } = suggest(schema, doc, pos)
    expect(ctx.kind).toBe('value')
    expect(items.map((i) => i.label)).toEqual(['http', 'grpc', 'queue', 'database'])
    expect(items[0].info).toContain('Kind of service')
  })

  it('falls back to the schema description when there is no hint', () => {
    expect(schemaAt(schema, ['spec', 'services', '*', 'errorRate', 'threshold'])?.type).toBe('number')
    const { doc, pos } = at(base + '      errorRate:\n        |\n')
    const items = suggest(schema, doc, pos).items
    expect(items.find((i) => i.label === 'threshold')?.info).toContain('0.05')
  })

  it('shows a hover hint for the key under the pointer', () => {
    const pos = example.indexOf('critical: true') + 3
    expect(hoverAt(schema, example, pos)?.text).toContain('Critical services page on-call')
    expect(hoverAt(schema, example, example.indexOf('# System description'))).toBeNull()
  })
})

describe('issue paths to positions', () => {
  it('maps a pointer to the key or the list item it names', () => {
    const r = pointerToRange(example, '/spec/services/2/dependsOn/0')!
    expect(example.slice(r.from, r.to)).toBe('cart')
    const k = pointerToRange(example, '/spec/services/1/latency/thresholdMs')!
    expect(example.slice(k.from, k.to)).toBe('thresholdMs')
    const n = pointerToRange(example, '/spec/notifications/routing/critical/1')!
    expect(example.slice(n.from, n.to)).toBe('team-email')
  })

  it('points a missing field at its deepest existing parent', () => {
    const r = pointerToRange(example, '/spec/services/3/queue')!
    expect(example.slice(r.from, r.to)).toBe('name: payment')
    const s = pointerToRange(example, '/spec/services/12/queue/topicX')!
    expect(example.slice(s.from, s.to)).toBe('queue')
  })

  it('reports YAML syntax errors with positions', () => {
    const errs = syntaxErrors('a: [1, 2\nb: c')
    expect(errs.length).toBeGreaterThan(0)
    expect(errs[0].from).toBeGreaterThanOrEqual(0)
    expect(pointerToRange('a: [1, 2\nb: c', '/a')).toBeNull()
  })
})
