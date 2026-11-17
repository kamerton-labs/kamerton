// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Highlight styles use concrete colors: a HighlightStyle compiles to class
// rules once, so var() would not follow the theme. Each clears 4.5:1 on its
// background.

import { HighlightStyle } from '@codemirror/language'
import { EditorView } from '@codemirror/view'
import { tags as t } from '@lezer/highlight'

export const darkHighlight = HighlightStyle.define([
  { tag: t.keyword, color: '#c792ea' },
  { tag: [t.propertyName, t.definition(t.propertyName)], color: '#82aaff' },
  { tag: [t.string, t.special(t.string)], color: '#c3e88d' },
  { tag: [t.number, t.bool, t.null], color: '#f78c6c' },
  { tag: t.comment, color: '#8d97a3', fontStyle: 'italic' },
  { tag: [t.punctuation, t.separator, t.meta], color: '#89ddff' },
])

export const lightHighlight = HighlightStyle.define([
  { tag: t.keyword, color: '#7c3aa8' },
  { tag: [t.propertyName, t.definition(t.propertyName)], color: '#1f66b0' },
  { tag: [t.string, t.special(t.string)], color: '#0f7457' },
  { tag: [t.number, t.bool, t.null], color: '#a3491a' },
  { tag: t.comment, color: '#5d655c', fontStyle: 'italic' },
  { tag: [t.punctuation, t.separator, t.meta], color: '#0e6a72' },
])

export const editorTheme = EditorView.theme({
  '&': { backgroundColor: 'transparent', fontSize: '13px', height: '100%' },
  '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', lineHeight: '1.5' },
  '.cm-content': { padding: '8px 0' },
  '.cm-gutters': { backgroundColor: 'transparent', border: 'none', color: 'var(--muted-foreground)' },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'color-mix(in oklab, currentColor 6%, transparent)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-cursor': { borderLeftColor: 'var(--foreground)' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection': { backgroundColor: 'color-mix(in oklab, var(--ring) 35%, transparent)' },
  '.cm-tooltip': {
    backgroundColor: 'var(--popover)',
    color: 'var(--popover-foreground)',
    border: '1px solid var(--border)',
    borderRadius: '6px',
  },
  '.cm-tooltip .cm-tooltip-arrow:before': { borderTopColor: 'var(--border)', borderBottomColor: 'var(--border)' },
  '.cm-tooltip .cm-tooltip-arrow:after': { borderTopColor: 'var(--popover)', borderBottomColor: 'var(--popover)' },
  '.cm-tooltip-autocomplete ul li[aria-selected]': { backgroundColor: 'var(--accent)', color: 'var(--accent-foreground)' },
  '.cm-completionInfo': { maxWidth: '360px', padding: '6px 8px', whiteSpace: 'normal', fontFamily: 'var(--font-sans)' },
  '.cm-completionDetail': { marginLeft: '8px', color: 'var(--muted-foreground)', fontStyle: 'normal' },
  '.cm-hint': { padding: '6px 8px', maxWidth: '360px', fontSize: '12px', fontFamily: 'var(--font-sans)' },
  '.cm-hint b': { display: 'block', marginBottom: '2px', fontFamily: 'ui-monospace, monospace' },
  '.cm-diagnostic': { fontFamily: 'var(--font-sans)' },
  '.cm-panels': { backgroundColor: 'var(--card)', color: 'var(--foreground)', borderColor: 'var(--border)' },
  '.cm-searchMatch': { backgroundColor: 'color-mix(in oklab, var(--warn) 35%, transparent)' },
})
