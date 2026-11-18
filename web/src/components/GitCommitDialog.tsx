// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useT } from '@/i18n'
import type { GitChange, RuleLine } from '@/lib/api'

const title = (c: GitChange) => (c.kind === 'delete' ? `Remove system ${c.system}` : c.kind === 'change' ? `Change system ${c.system}` : `Add system ${c.system}`)

/** The saved changes, what they do to the rules, and the message they are pushed with. */
export default function GitCommitDialog({
  changes,
  target,
  pending,
  error,
  onConfirm,
  onClose,
}: {
  changes: GitChange[]
  /** "pull request into main" or "push to main". */
  target: string
  pending: boolean
  error?: string
  onConfirm: (message: string) => void
  onClose: () => void
}) {
  const { t } = useT()
  const fallback = changes.length === 1 ? title(changes[0]) : `Change ${changes.length} systems`
  const [message, setMessage] = useState(fallback)
  return (
    <Dialog open onOpenChange={(v) => !v && !pending && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl" data-testid="git-commit-dialog">
        <DialogHeader>
          <DialogTitle>Commit</DialogTitle>
          <DialogDescription>{target}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 text-sm">
          <ul className="space-y-3" data-testid="git-commit-diff">
            {changes.map((c) => (
              <li key={c.system}>
                <ChangeSummary c={c} />
              </li>
            ))}
          </ul>
          <div className="space-y-1.5">
            <Label htmlFor="git-msg">{t('gitc.message')}</Label>
            <Input id="git-msg" value={message} onChange={(e) => setMessage(e.target.value)} maxLength={120} data-testid="git-commit-message" />
          </div>
          {error && <p className="text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => onConfirm(message.trim() || fallback)} disabled={pending} data-testid="git-commit-confirm">
            {pending && <Loader2 className="animate-spin" />}
            Push
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

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
