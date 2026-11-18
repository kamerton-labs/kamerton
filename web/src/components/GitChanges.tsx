// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState } from 'react'
import { useT } from '@/i18n'
import type { GitChange, RuleLine } from '@/lib/api'

const one = (c: GitChange) => (c.kind === 'delete' ? `Remove system ${c.system}` : c.kind === 'change' ? `Change system ${c.system}` : `Add system ${c.system}`)

/** The commit message used when the person leaves the field empty. */
// eslint-disable-next-line react-refresh/only-export-components
export const defaultMessage = (changes: GitChange[]) => (changes.length === 1 ? one(changes[0]) : `Change ${changes.length} systems`)

/** One saved change: the system, and in words what happens to its rules. */
export function ChangeSummary({ c }: { c: GitChange }) {
  const { t, tn } = useT()
  const d = c.diff
  const none = d && !d.new && d.changed.length + d.added.length + d.removed.length === 0
  return (
    <div>
      <div className="flex flex-wrap items-baseline gap-x-2">
        <span className="font-medium">{c.system}</span>
        <span className="text-xs text-muted-foreground">
          {c.kind === 'delete'
            ? t('gitc.kind.delete')
            : c.error
              ? c.error
              : !d
                ? ''
                : c.kind === 'move'
                  ? tn('gitc.kind.move', d.total)
                  : d.new
                    ? tn('gitc.kind.new', d.total)
                    : none
                      ? t('gitc.diff.none')
                      : t('gitc.diff.summary', { changed: d.changed.length, added: d.added.length, removed: d.removed.length })}
        </span>
      </div>
      {d && c.kind === 'change' && (
        <>
          <Lines lines={d.changed} sep=": " />
          <Lines lines={d.added} sep=" " prefix="+ " />
          <Lines lines={d.removed} sep=" " prefix="− " />
        </>
      )}
    </div>
  )
}

function Lines({ lines, sep, prefix = '' }: { lines: RuleLine[]; sep: string; prefix?: string }) {
  const { t } = useT()
  const [all, setAll] = useState(false)
  if (lines.length === 0) return null
  const shown = all ? lines : lines.slice(0, 4)
  return (
    <ul className="mt-0.5 space-y-0.5 text-xs">
      {shown.map((l) => (
        <li key={l.rule} className="[overflow-wrap:anywhere]">
          {prefix}
          {l.rule}
          {l.what && (
            <span className="text-muted-foreground">
              {sep}
              {l.what}
            </span>
          )}
        </li>
      ))}
      {lines.length > shown.length && (
        <li>
          <button type="button" className="text-muted-foreground underline underline-offset-2" onClick={() => setAll(true)}>
            {t('gitc.diff.more', { n: lines.length - shown.length })}
          </button>
        </li>
      )}
    </ul>
  )
}
