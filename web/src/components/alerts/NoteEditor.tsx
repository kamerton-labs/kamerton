// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useT } from '@/i18n'
import { useSaveIncidentNote, type IncidentNote } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { NOTE_CAUSE_MAX, NOTE_FIX_MAX, noteCounter, noteLength, notePayload } from '@/lib/incidents'

/** "updated 2 days ago, from Telegram"; who wrote it is not recorded. */
export function NoteMeta({ note }: { note?: IncidentNote }) {
  const { t } = useT()
  if (!note?.updatedAt) return null
  const ago = timeAgo(note.updatedAt)
  return (
    <span className="text-xs text-muted-foreground" data-testid="note-meta">
      {note.source === 'telegram' ? t('note.updatedTelegram', { ago }) : t('note.updated', { ago })}
    </span>
  )
}

/** The note as the core stored it, shown after a save. */
export function SavedNote({ note, onEdit }: { note?: IncidentNote; onEdit: () => void }) {
  const { t } = useT()
  const fix = note?.fix?.trim()
  const cause = note?.cause?.trim()
  return (
    <div className="space-y-1 text-sm" data-testid="note-saved">
      {fix && (
        <p className="font-medium [overflow-wrap:anywhere] whitespace-pre-line">
          <span className="text-muted-foreground">{t('note.fix')}: </span>
          {fix}
        </p>
      )}
      {cause && (
        <p className="[overflow-wrap:anywhere] whitespace-pre-line">
          <span className="text-muted-foreground">{t('note.cause')}: </span>
          {cause}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-xs text-muted-foreground">{t('note.saved')}</span>
        <Button type="button" variant="ghost" size="xs" onClick={onEdit} data-testid="note-edit">
          {t('common.edit')}
        </Button>
      </div>
    </div>
  )
}

function Field({ id, label, placeholder, value, max, onChange, testid }: { id: string; label: string; placeholder: string; value: string; max: number; onChange: (v: string) => void; testid: string }) {
  const len = noteLength(value)
  const counter = noteCounter(len, max)
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-2">
        <Label htmlFor={id} className="text-xs">
          {label}
        </Label>
        {counter && (
          <span className={len >= max ? 'text-xs text-destructive tabular-nums' : 'text-xs text-muted-foreground tabular-nums'} data-testid={`${testid}-counter`}>
            {counter}
          </span>
        )}
      </div>
      <Textarea id={id} rows={2} maxLength={max} value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} data-testid={testid} />
    </div>
  )
}

/** "Cause" and "What helped" of a recorded incident; saved only on the button. */
export default function NoteEditor({ id, note }: { id: string; note?: IncidentNote }) {
  const { t } = useT()
  const save = useSaveIncidentNote()
  const [cause, setCause] = useState(note?.cause ?? '')
  const [fix, setFix] = useState(note?.fix ?? '')
  const [stored, setStored] = useState<IncidentNote | undefined>(note)
  const [editing, setEditing] = useState(true)
  const next = notePayload(cause, fix)
  const changed = next.cause !== (stored?.cause ?? '').trim() || next.fix !== (stored?.fix ?? '').trim()
  const domId = `note-${id.replace(/[^\w-]/g, '_')}`
  if (!editing) return <SavedNote note={stored} onEdit={() => setEditing(true)} />
  return (
    <form
      className="space-y-3"
      data-testid="note-editor"
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate(
          { id, ...next },
          {
            onSuccess: (rec) => {
              const n = rec?.note
              setStored(n)
              setCause(n?.cause ?? '')
              setFix(n?.fix ?? '')
              if (n) setEditing(false)
            },
          },
        )
      }}
    >
      <Field id={`${domId}-cause`} label={t('note.cause')} placeholder={t('note.causePlaceholder')} value={cause} max={NOTE_CAUSE_MAX} onChange={setCause} testid="note-cause" />
      <Field id={`${domId}-fix`} label={t('note.fix')} placeholder={t('note.fixPlaceholder')} value={fix} max={NOTE_FIX_MAX} onChange={setFix} testid="note-fix" />
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Button type="submit" size="sm" disabled={save.isPending || !changed} data-testid="note-save">
          {save.isPending && <Loader2 className="animate-spin" />} {t('system.save')}
        </Button>
        <NoteMeta note={stored} />
      </div>
      {save.isError && (
        <p className="text-xs text-destructive" data-testid="note-error">
          {t('common.failed', { error: save.error.message })}
        </p>
      )}
    </form>
  )
}
