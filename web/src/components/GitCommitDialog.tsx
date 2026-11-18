// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useEffect, useState } from 'react'
import { GitBranch, GitPullRequest, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useT } from '@/i18n'
import { gitPreview, type GitPreview, type RuleLine } from '@/lib/api'

export type GitAction = 'save' | 'move' | 'delete'

/**
 * What a person sees before a change goes to the repository: where it goes, which rules it
 * changes, and the commit message, which they can word themselves.
 */
export default function GitCommitDialog({
  action,
  system,
  yaml,
  mode,
  file,
  pending,
  error,
  onConfirm,
  onClose,
}: {
  action: GitAction
  system: string
  /** The description to write; not used for a delete. */
  yaml: string
  mode: 'pullRequest' | 'commit'
  /** The file as known before the preview answers. */
  file?: string
  pending: boolean
  error?: string
  onConfirm: (message: string) => void
  onClose: () => void
}) {
  const { t, tn } = useT()
  const [preview, setPreview] = useState<GitPreview | null>(null)
  const [previewError, setPreviewError] = useState('')
  const fallback = t(action === 'delete' ? 'gitc.title.delete' : action === 'move' ? 'gitc.title.move' : 'gitc.title.save', { system })
  const [title, setTitle] = useState(fallback)

  useEffect(() => {
    if (action === 'delete') return
    let live = true
    gitPreview(yaml)
      .then((p) => live && setPreview(p))
      .catch((e: unknown) => live && setPreviewError(e instanceof Error ? e.message : String(e)))
    return () => {
      live = false
    }
  }, [action, yaml])

  const target = preview?.file ?? file
  const d = preview?.diff
  const none = d && !d.new && d.changed.length + d.added.length + d.removed.length === 0
  const Icon = mode === 'pullRequest' ? GitPullRequest : GitBranch
  return (
    <Dialog open onOpenChange={(v) => !v && !pending && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl" data-testid="git-commit-dialog">
        <DialogHeader>
          <DialogTitle>Commit</DialogTitle>
          <DialogDescription className="sr-only">{t(mode === 'pullRequest' ? 'gitc.where.propose' : 'gitc.where.commit', { branch: '', repo: '' })}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 text-sm">
          <div className="flex items-start gap-2 rounded-md border bg-muted/30 p-3" data-testid="git-commit-where">
            <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <div className="min-w-0 space-y-0.5">
              <div className="font-mono text-xs [overflow-wrap:anywhere]">{target ?? '…'}</div>
              <div className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
                {preview
                  ? t(mode === 'pullRequest' ? 'gitc.where.propose' : 'gitc.where.commit', { branch: preview.branch, repo: preview.repoURL ?? preview.repo })
                  : action === 'delete'
                    ? t('gitc.where.delete')
                    : t('gitc.loading')}
              </div>
            </div>
          </div>

          {action === 'delete' ? (
            <p data-testid="git-commit-diff">{t('gitc.delete.text', { system })}</p>
          ) : previewError ? (
            <p className="text-destructive">{t('gitc.previewError', { error: previewError })}</p>
          ) : !d ? (
            <p className="flex items-center gap-2 text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" /> {t('gitc.loading')}
            </p>
          ) : (
            <div className="space-y-2" data-testid="git-commit-diff">
              <p className="font-medium">
                {d.new
                  ? tn('gitc.diff.new', d.total)
                  : preview?.move
                    ? tn('gitc.diff.move', d.total)
                    : none
                      ? t('gitc.diff.none')
                      : t('gitc.diff.summary', { changed: d.changed.length, added: d.added.length, removed: d.removed.length, total: d.total })}
              </p>
              {!d.new && !preview?.move && (
                <>
                  <Lines title={t('gitc.diff.changed')} lines={d.changed} sep=": " />
                  <Lines title={t('gitc.diff.added')} lines={d.added} sep=" " />
                  <Lines title={t('gitc.diff.removed')} lines={d.removed} sep=" " />
                </>
              )}
            </div>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="git-msg">{t('gitc.message')}</Label>
            <Input id="git-msg" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={120} data-testid="git-commit-message" />
          </div>
          {error && <p className="text-destructive">{error}</p>}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={() => onConfirm(title.trim() || fallback)}
            disabled={pending || (action !== 'delete' && !preview && !previewError)}
            variant={action === 'delete' ? 'destructive' : 'default'}
            data-testid="git-commit-confirm"
          >
            {pending && <Loader2 className="animate-spin" />}
            Push
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Lines({ title, lines, sep }: { title: string; lines: RuleLine[]; sep: string }) {
  const { t } = useT()
  const [all, setAll] = useState(false)
  if (lines.length === 0) return null
  const shown = all ? lines : lines.slice(0, 6)
  return (
    <div>
      <div className="text-xs text-muted-foreground">{title}</div>
      <ul className="mt-0.5 space-y-0.5">
        {shown.map((l) => (
          <li key={l.rule} className="[overflow-wrap:anywhere]">
            <span className="font-medium">{l.rule}</span>
            {l.what && (
              <span className="text-muted-foreground">
                {sep}
                {l.what}
              </span>
            )}
          </li>
        ))}
      </ul>
      {lines.length > shown.length && (
        <button type="button" className="mt-1 text-xs text-muted-foreground underline underline-offset-2" onClick={() => setAll(true)}>
          {t('gitc.diff.more', { n: lines.length - shown.length })}
        </button>
      )}
    </div>
  )
}
