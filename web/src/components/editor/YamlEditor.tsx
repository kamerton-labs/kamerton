// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// YAML editor (CodeMirror 6). Completions and hints come from the JSON Schema
// (lib/yaml-schema.ts); markers are local syntax errors and the core's issues.

import { useEffect, useRef } from 'react'
import { autocompletion, closeBrackets, completionKeymap, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { yaml } from '@codemirror/lang-yaml'
import { indentUnit, syntaxHighlighting } from '@codemirror/language'
import { lintGutter, lintKeymap, setDiagnostics, type Diagnostic } from '@codemirror/lint'
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search'
import { Compartment, EditorState } from '@codemirror/state'
import { EditorView, highlightActiveLine, highlightActiveLineGutter, hoverTooltip, keymap, lineNumbers } from '@codemirror/view'
import { t } from '@/i18n'
import { useTheme } from '@/lib/theme'
import { hoverAt, pointerToRange, suggest, syntaxErrors, type SchemaNode } from '@/lib/yaml-schema'
import { darkHighlight, editorTheme, lightHighlight } from './cm-theme'

export interface EditorIssue {
  path: string
  message: string
  severity?: 'error' | 'warning'
}

interface Props {
  value: string
  onChange: (value: string) => void
  schema?: Record<string, unknown>
  issues?: EditorIssue[]
  readOnly?: boolean
  className?: string
}

export default function YamlEditor({ value, onChange, schema, issues = [], readOnly = false, className }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const onChangeRef = useRef(onChange)
  const schemaRef = useRef<SchemaNode | undefined>(schema as SchemaNode | undefined)
  const themeComp = useRef(new Compartment())
  const roComp = useRef(new Compartment())
  const { theme } = useTheme()

  useEffect(() => {
    onChangeRef.current = onChange
    schemaRef.current = schema as SchemaNode | undefined
  }, [onChange, schema])

  // Create the editor once; theme and read-only are reconfigured in place.
  useEffect(() => {
    if (!host.current) return
    const complete = (ctx: CompletionContext): CompletionResult | null => {
      const s = schemaRef.current
      if (!s) return null
      const { ctx: c, items } = suggest(s, ctx.state.doc.toString(), ctx.pos)
      if (!items.length || (!ctx.explicit && c.prefix === '' && c.kind === 'key' && !/^\s*(-\s+)?$/.test(ctx.state.doc.lineAt(ctx.pos).text.slice(0, ctx.pos - ctx.state.doc.lineAt(ctx.pos).from))))
        return null
      return {
        from: ctx.pos - c.prefix.length,
        options: items.map((i) => ({
          label: i.label,
          detail: i.detail,
          info: i.info || undefined,
          type: i.kind === 'property' ? 'property' : 'enum',
          apply: i.apply,
        })),
        validFor: /^[\w./-]*$/,
      }
    }
    const hint = hoverTooltip((v, pos) => {
      const s = schemaRef.current
      if (!s) return null
      const h = hoverAt(s, v.state.doc.toString(), pos)
      if (!h) return null
      return {
        pos: h.from,
        end: h.to,
        create() {
          const dom = document.createElement('div')
          dom.className = 'cm-hint'
          const b = document.createElement('b')
          b.textContent = h.title
          dom.append(b, document.createTextNode(h.text))
          return { dom }
        },
      }
    })
    const state = EditorState.create({
      doc: value,
      extensions: [
        lineNumbers(),
        highlightActiveLine(),
        highlightActiveLineGutter(),
        history(),
        indentUnit.of('  '),
        EditorState.tabSize.of(2),
        yaml(),
        closeBrackets(),
        autocompletion({ override: [complete], activateOnTyping: true, icons: false }),
        lintGutter(),
        hint,
        search({ top: true }),
        highlightSelectionMatches(),
        keymap.of([indentWithTab, ...defaultKeymap, ...historyKeymap, ...completionKeymap, ...lintKeymap, ...searchKeymap]),
        editorTheme,
        EditorView.contentAttributes.of({ 'aria-label': t('yaml.aria') }),
        themeComp.current.of(syntaxHighlighting(lightHighlight)),
        roComp.current.of([EditorState.readOnly.of(false)]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) onChangeRef.current(u.state.doc.toString())
        }),
      ],
    })
    const v = new EditorView({ state, parent: host.current })
    view.current = v
    return () => {
      v.destroy()
      view.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- created once; value is synced below
  }, [])

  // External changes (canvas, rule editors) replace the text.
  useEffect(() => {
    const v = view.current
    if (v && value !== v.state.doc.toString()) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
  }, [value])

  useEffect(() => {
    view.current?.dispatch({ effects: themeComp.current.reconfigure(syntaxHighlighting(theme === 'dark' ? darkHighlight : lightHighlight)) })
  }, [theme])

  useEffect(() => {
    view.current?.dispatch({ effects: roComp.current.reconfigure([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]) })
  }, [readOnly])

  // Markers: syntax errors at once; the core's issues when the text parses.
  useEffect(() => {
    const v = view.current
    if (!v) return
    const text = v.state.doc.toString()
    const syntax = syntaxErrors(text)
    const diags: Diagnostic[] = syntax.length
      ? syntax.map((e) => ({ from: Math.min(e.from, text.length), to: Math.min(e.to, text.length), severity: 'error', message: t('yaml.syntax', { message: e.message }) }))
      : issues.flatMap((i) => {
          const r = pointerToRange(text, i.path)
          return r ? [{ from: r.from, to: Math.max(r.to, r.from), severity: i.severity === 'warning' ? 'warning' : 'error', message: i.message } as Diagnostic] : []
        })
    v.dispatch(setDiagnostics(v.state, diags))
  }, [issues, value])

  return <div ref={host} className={className} />
}
