// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// The editing state of one system: the YAML text (the stored truth, edited
// directly in YAML mode) and the last description that parsed. While the
// text does not parse, the canvas and rule editors keep showing the last
// valid description and do not allow changes, so a typo in YAML mode never
// wipes the picture and the picture never overwrites the text being fixed.

import { parseDescriptionYaml, writeDescriptionYaml } from './yaml'
import type { Description } from './types'

export interface Draft {
  /** Text as stored on the server. */
  saved: string
  /** Current text. */
  text: string
  /** Last description that parsed (null until the first one does). */
  desc: Description | null
  /** Why the current text does not parse; null when it does. */
  error: { message: string; line?: number } | null
}

export function initDraft(text: string): Draft {
  const p = parseDescriptionYaml(text)
  return { saved: text, text, desc: p.ok ? p.desc : null, error: p.ok ? null : { message: p.error, line: p.line } }
}

/** New text from YAML mode. */
export function setText(d: Draft, text: string): Draft {
  if (text === d.text) return d
  const p = parseDescriptionYaml(text)
  return p.ok ? { ...d, text, desc: p.desc, error: null } : { ...d, text, error: { message: p.error, line: p.line } }
}

/** A change made on the canvas or in a rule editor. Ignored while the text does not parse. */
export function edit(d: Draft, fn: (desc: Description) => Description): Draft {
  if (d.error || !d.desc) return d
  const next = fn(d.desc)
  if (next === d.desc) return d
  return { ...d, text: writeDescriptionYaml(d.text, next), desc: next }
}

export const isDirty = (d: Draft) => d.text !== d.saved

/** After a successful save. */
export const markSaved = (d: Draft): Draft => ({ ...d, saved: d.text })
