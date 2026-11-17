// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// YAML text is the stored truth of a system. The canvas and the rule
// editors work on a parsed Description; every change is written back into
// the existing YAML document node by node, so comments, key order and
// formatting of untouched parts survive.

import { t } from '@/i18n'
import { isCollection, isMap, isSeq, parseDocument, type Document, type Node as YamlNode } from 'yaml'
import type { Description } from './types'

const TYPES = ['http', 'grpc', 'queue', 'database']

export type ParseResult =
  | { ok: true; desc: Description }
  | { ok: false; error: string; line?: number }

/** Parses the YAML text of a description. Never throws. */
export function parseDescriptionYaml(text: string): ParseResult {
  let doc: Document
  try {
    doc = parseDocument(text)
  } catch (e) {
    return { ok: false, error: String(e) }
  }
  if (doc.errors.length > 0) {
    const err = doc.errors[0]
    return { ok: false, error: err.message.split('\n')[0].replace(/[:.]+$/, ''), line: err.linePos?.[0]?.line }
  }
  const v = doc.toJS() as unknown
  if (!v || typeof v !== 'object' || Array.isArray(v)) return { ok: false, error: t('yaml.error.notDescription') }
  const d = v as Partial<Description>
  if (!d.metadata || typeof d.metadata !== 'object' || typeof d.metadata.name !== 'string') return { ok: false, error: t('yaml.error.noName') }
  if (!d.spec || typeof d.spec !== 'object' || !Array.isArray(d.spec.services)) return { ok: false, error: t('yaml.error.noServices') }
  for (const [i, s] of d.spec.services.entries()) {
    if (!s || typeof s !== 'object' || typeof s.name !== 'string') return { ok: false, error: t('yaml.error.serviceNoName', { n: i + 1 }) }
    if (!TYPES.includes(s.type)) return { ok: false, error: t('yaml.error.serviceType', { name: s.name, types: TYPES.join(', ') }) }
    if (s.dependsOn !== undefined && !(Array.isArray(s.dependsOn) && s.dependsOn.every((x) => typeof x === 'string')))
      return { ok: false, error: t('yaml.error.dependsOn', { name: s.name }) }
  }
  return { ok: true, desc: d as Description }
}

const isPlainObject = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)

export function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((x, i) => deepEqual(x, b[i]))
  if (isPlainObject(a) && isPlainObject(b)) {
    const ka = Object.keys(a).filter((k) => a[k] !== undefined)
    const kb = Object.keys(b).filter((k) => b[k] !== undefined)
    return ka.length === kb.length && ka.every((k) => deepEqual(a[k], b[k]))
  }
  return false
}

type Path = (string | number)[]

function createNode(doc: Document, value: unknown, like?: unknown): YamlNode {
  const node = doc.createNode(value) as YamlNode
  // Keep flow style ([a, b], {x: 1}) of the node being replaced.
  if (isCollection(like) && isCollection(node) && like.flow) node.flow = true
  return node
}

function sync(doc: Document, path: Path, prev: unknown, next: unknown) {
  if (deepEqual(prev, next)) return
  if (next === undefined) {
    doc.deleteIn(path)
    return
  }
  const node = path.length === 0 ? doc.contents : doc.getIn(path, true)
  if (isPlainObject(prev) && isPlainObject(next) && isMap(node)) {
    for (const k of Object.keys(prev)) {
      if (next[k] === undefined && prev[k] !== undefined) doc.deleteIn([...path, k])
    }
    for (const [k, v] of Object.entries(next)) {
      if (v === undefined) continue
      if (prev[k] === undefined) doc.setIn([...path, k], createNode(doc, v))
      else sync(doc, [...path, k], prev[k], v)
    }
    return
  }
  if (Array.isArray(prev) && Array.isArray(next) && isSeq(node)) {
    const named = (xs: unknown[]) => xs.every((x) => isPlainObject(x) && typeof x.name === 'string')
    let cur = prev
    if (named(prev) && named(next)) {
      // Remove items that are gone (by name), from the end so indexes hold.
      const keep = new Set(next.map((x) => (x as { name: string }).name))
      for (let i = prev.length - 1; i >= 0; i--) {
        if (!keep.has((prev[i] as { name: string }).name)) doc.deleteIn([...path, i])
      }
      cur = prev.filter((x) => keep.has((x as { name: string }).name))
    } else if (cur.length > next.length) {
      for (let i = cur.length - 1; i >= next.length; i--) doc.deleteIn([...path, i])
      cur = cur.slice(0, next.length)
    }
    for (let i = 0; i < next.length; i++) {
      if (i < cur.length) {
        const sameItem = !named([cur[i], next[i]]) || (cur[i] as { name: string }).name === (next[i] as { name: string }).name
        if (sameItem) sync(doc, [...path, i], cur[i], next[i])
        else doc.setIn([...path, i], createNode(doc, next[i]))
      } else {
        doc.addIn(path, createNode(doc, next[i]))
      }
    }
    return
  }
  if (path.length === 0) {
    doc.contents = createNode(doc, next) as typeof doc.contents
    return
  }
  // Scalars keep their node (comment, quoting) when replaced by a scalar.
  doc.setIn(path, isPlainObject(next) || Array.isArray(next) ? createNode(doc, next, node) : next)
}

/**
 * Writes `next` into the YAML text `text`, touching only what changed.
 * `text` must parse (callers keep editing disabled while it does not).
 */
export function writeDescriptionYaml(text: string, next: Description): string {
  const doc = parseDocument(text)
  if (doc.errors.length > 0) throw new Error('cannot update YAML with syntax errors')
  const prev = doc.toJS()
  const plain = JSON.parse(JSON.stringify(next))
  if (deepEqual(prev, plain)) return text
  sync(doc, [], prev, plain)
  return doc.toString({ lineWidth: 0, flowCollectionPadding: false })
}
