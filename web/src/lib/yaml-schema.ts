// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// What the YAML editor knows about the description, from its JSON Schema:
// which keys are valid at the cursor, which values an enum allows, and the
// hint for a field. Plus mapping the core's JSON-pointer issue paths to
// positions in the text. Pure functions, unit-tested; the CodeMirror wiring
// lives in components/editor/YamlEditor.tsx.
//
// The cursor position is resolved by indentation rather than by parsing, so
// completion works in a half-typed document that does not parse yet.

import { isMap, isPair, isScalar, isSeq, parseDocument, type Node as YamlNode } from 'yaml'
import { getLang, t } from '@/i18n'
import { hintRu } from '@/i18n/schema-ru'

export interface SchemaNode {
  type?: string | string[]
  description?: string
  properties?: Record<string, SchemaNode>
  items?: SchemaNode
  additionalProperties?: SchemaNode | boolean
  enum?: unknown[]
  required?: string[]
}

interface LineInfo {
  indent: number
  dash: boolean
  /** Column where the key (or the item's content) starts. */
  keyCol: number
  key?: string
  /** Text after "key:" (trimmed). */
  rest?: string
  blank: boolean
}

const LINE_RE = /^(\s*)(-\s+)?(?:(["']?)([^"'#:\s][^:#]*?)\3\s*:(?:\s|$)(.*))?/

function lineInfo(text: string): LineInfo {
  const trimmed = text.trim()
  if (!trimmed || trimmed.startsWith('#')) return { indent: 0, dash: false, keyCol: 0, blank: true }
  const m = LINE_RE.exec(text)!
  const indent = m[1].length
  const dash = !!m[2]
  return { indent, dash, keyCol: indent + (m[2]?.length ?? 0), key: m[4], rest: m[5]?.trim(), blank: false }
}

export interface CursorContext {
  /** Path of the mapping that holds the key at the cursor ('*' for array items). */
  path: string[]
  /** 'key' when typing a key, 'value' when typing the value of `key`. */
  kind: 'key' | 'value'
  key?: string
  /** The word typed so far. */
  prefix: string
  /** Keys already present in that mapping. */
  siblings: string[]
}

/** Where the cursor is, in terms of the document structure. */
export function contextAt(doc: string, pos: number): CursorContext {
  const lines = doc.split('\n')
  let offset = 0
  let row = 0
  while (row < lines.length - 1 && offset + lines[row].length < pos) {
    offset += lines[row].length + 1
    row++
  }
  const line = lines[row]
  const col = pos - offset
  const before = line.slice(0, col)
  const valueMatch = /^(\s*)(-\s+)?(["']?)([^"'#:\s][^:#]*?)\3\s*:\s+(.*)$/.exec(before)
  const keyMatch = /^(\s*)(-\s+)?([\w./-]*)$/.exec(before)
  const path: string[] = []
  let need: number
  let kind: 'key' | 'value' = 'key'
  let key: string | undefined
  let prefix: string
  let dash: boolean
  let indent: number
  if (valueMatch) {
    kind = 'value'
    key = valueMatch[4]
    prefix = valueMatch[5]
    indent = valueMatch[1].length
    dash = !!valueMatch[2]
    need = indent + (valueMatch[2]?.length ?? 0)
  } else {
    indent = keyMatch ? keyMatch[1].length : before.length - before.trimStart().length
    dash = !!keyMatch?.[2]
    prefix = keyMatch?.[3] ?? ''
    need = indent + (keyMatch?.[2]?.length ?? 0)
  }
  if (dash) {
    path.unshift('*')
    need = indent
  }
  const itemCol = dash ? indent + (valueMatch?.[2] ?? keyMatch?.[2] ?? '').length : need
  const siblings: string[] = []
  let inSameMapping = true
  for (let j = row - 1; j >= 0; j--) {
    const li = lineInfo(lines[j])
    if (li.blank) continue
    if (li.dash && li.indent < need) {
      if (inSameMapping && li.key && li.keyCol === itemCol && !dash) siblings.push(li.key)
      path.unshift('*')
      need = li.indent
      inSameMapping = false
      continue
    }
    if (li.key && li.keyCol < need) {
      path.unshift(li.key)
      need = li.keyCol
      inSameMapping = false
      continue
    }
    if (inSameMapping && li.key && li.keyCol === itemCol) siblings.push(li.key)
  }
  // Keys below the cursor in the same mapping.
  for (let j = row + 1; j < lines.length; j++) {
    const li = lineInfo(lines[j])
    if (li.blank) continue
    if (li.keyCol < itemCol || (li.dash && li.indent < itemCol + 1 && li.keyCol <= itemCol)) break
    if (li.key && li.keyCol === itemCol && !li.dash) siblings.push(li.key)
  }
  return { path, kind, key, prefix, siblings }
}

/** The schema node at a path ('*' for array items; unknown map keys use additionalProperties). */
export function schemaAt(root: SchemaNode, path: string[]): SchemaNode | undefined {
  let n: SchemaNode | undefined = root
  for (const seg of path) {
    if (!n) return undefined
    if (seg === '*' || /^\d+$/.test(seg)) n = n.items
    else n = n.properties?.[seg] ?? (typeof n.additionalProperties === 'object' ? n.additionalProperties : undefined)
  }
  return n
}

export interface Suggestion {
  label: string
  /** Short type, e.g. "string", "list". */
  detail: string
  info: string
  kind: 'property' | 'value'
  /** Text to insert. */
  apply: string
}

const SCHEMA_TYPES = ['string', 'integer', 'number', 'boolean', 'array', 'object'] as const

const typeLabel = (n: SchemaNode): string => {
  const ty = Array.isArray(n.type) ? n.type[0] : n.type
  if (n.enum) return n.enum.map(String).join(' | ')
  return ty && (SCHEMA_TYPES as readonly string[]).includes(ty) ? t(`schema.type.${ty as (typeof SCHEMA_TYPES)[number]}` as const) : ''
}

/** Hint for a field: in Russian the Russian hint when known; otherwise the schema's own (English) description. */
export function hintFor(root: SchemaNode, path: string[]): string {
  return (getLang() === 'ru' ? hintRu(path) : undefined) ?? schemaAt(root, path)?.description ?? ''
}

/** Completion items at the cursor. */
export function suggest(root: SchemaNode, doc: string, pos: number): { ctx: CursorContext; items: Suggestion[] } {
  const ctx = contextAt(doc, pos)
  if (ctx.kind === 'value') {
    const n = schemaAt(root, [...ctx.path, ctx.key!])
    const values = n?.enum ?? (n && (Array.isArray(n.type) ? n.type.includes('boolean') : n.type === 'boolean') ? [true, false] : [])
    return {
      ctx,
      items: values.map((v) => ({ label: String(v), detail: '', info: hintFor(root, [...ctx.path, ctx.key!]), kind: 'value', apply: String(v) })),
    }
  }
  const n = schemaAt(root, ctx.path)
  const props = n?.properties ?? {}
  const items = Object.entries(props)
    .filter(([k]) => !ctx.siblings.includes(k))
    .map(([k, child]) => {
      const container = child.type === 'object' || child.type === 'array'
      return {
        label: k,
        detail: typeLabel(child),
        info: hintFor(root, [...ctx.path, k]),
        kind: 'property' as const,
        apply: container ? `${k}:` : `${k}: `,
      }
    })
    .sort((a, b) => Number(!(n?.required ?? []).includes(a.label)) - Number(!(n?.required ?? []).includes(b.label)) || a.label.localeCompare(b.label))
  return { ctx, items }
}

/** The key under the cursor and its hint, for hover tooltips. */
export function hoverAt(root: SchemaNode, doc: string, pos: number): { from: number; to: number; title: string; text: string } | null {
  const lineStart = doc.lastIndexOf('\n', pos - 1) + 1
  const lineEnd = doc.indexOf('\n', pos) === -1 ? doc.length : doc.indexOf('\n', pos)
  const line = doc.slice(lineStart, lineEnd)
  const m = /^(\s*)(-\s+)?(["']?)([^"'#:\s][^:#]*?)\3\s*:/.exec(line)
  if (!m) return null
  const from = lineStart + m[1].length + (m[2]?.length ?? 0) + m[3].length
  const to = from + m[4].length
  if (pos < from || pos > to) return null
  const ctx = contextAt(doc, lineStart + m[1].length + (m[2]?.length ?? 0))
  const path = [...ctx.path, m[4]]
  const text = hintFor(root, path)
  if (!text) return null
  const n = schemaAt(root, path)
  return { from, to, title: `${m[4]}${n ? ` · ${typeLabel(n)}` : ''}`, text }
}

// ---- Issue paths -> text positions ----

function unescape(seg: string) {
  return seg.replace(/~1/g, '/').replace(/~0/g, '~')
}

/**
 * Position of a JSON pointer (/spec/services/2/dependsOn/0) in YAML text: the
 * key of the field, or the item for list entries. A path that does not exist
 * (a missing required field) points at its deepest existing parent. Returns
 * null when the text does not parse.
 */
export function pointerToRange(text: string, pointer: string): { from: number; to: number } | null {
  const doc = parseDocument(text, { keepSourceTokens: false })
  if (doc.errors.length > 0) return null
  const segs = pointer.split('/').slice(1).map(unescape).filter((s, i, a) => !(s === '' && i === a.length - 1))
  let node: unknown = doc.contents
  let best: { from: number; to: number } = { from: 0, to: Math.min(text.indexOf('\n') === -1 ? text.length : text.indexOf('\n'), text.length) }
  for (const seg of segs) {
    if (isMap(node)) {
      const pair = node.items.find((p) => isPair(p) && isScalar(p.key) && String(p.key.value) === seg)
      if (!pair || !isScalar(pair.key) || !pair.key.range) break
      best = { from: pair.key.range[0], to: pair.key.range[1] }
      node = pair.value
    } else if (isSeq(node)) {
      const item = node.items[Number(seg)] as YamlNode | undefined
      if (!item || !item.range) break
      const firstLineEnd = text.indexOf('\n', item.range[0])
      best = { from: item.range[0], to: Math.min(item.range[1], firstLineEnd === -1 ? item.range[1] : firstLineEnd) }
      node = item
    } else break
  }
  return best
}

/** YAML syntax errors with positions (shown at once, before the core is asked). */
export function syntaxErrors(text: string): { from: number; to: number; message: string }[] {
  const doc = parseDocument(text)
  return doc.errors.map((e) => ({
    from: e.pos[0],
    to: Math.max(e.pos[1], e.pos[0] + 1),
    message: e.message.split('\n')[0],
  }))
}
