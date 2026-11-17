// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// "Alert text": the team writes the notification of a rule in its own
// words and language. Placeholders come from a fixed list and are inserted
// with a click; the preview shows the message as it will arrive, in the
// language of the notification messages of the description (English unless
// it says Russian), the same words the core uses.

import { useRef, useState } from 'react'
import { ChevronDown } from 'lucide-react'
import { cn } from 'cn'
import { Field } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { tl, useT } from '@/i18n'
import { useEditor } from '@/lib/editor'
import { checkMessageText, defaultTitle, fill, getMessage, notificationLang, PLACEHOLDER_KEYS, placeholderLabel, previewValues, setMessage, thresholdText as thresholdIn } from '@/lib/model/message'
import type { RuleItem } from '@/lib/model/rules'
import { getTimeZone, offsetLabel, offsetMinutes } from '@/lib/model/timezone'
import type { NotificationMessage } from '@/lib/model/types'

type Key = 'title' | 'text' | 'action'

export default function MessageEditor({ rule }: { rule: RuleItem }) {
  const { desc, edit, readOnly, issues } = useEditor()
  const { t } = useT()
  const stored = getMessage(desc, rule) ?? {}
  const [open, setOpen] = useState(!!(stored.title || stored.text || stored.action))
  const [draft, setDraft] = useState<NotificationMessage>(stored)
  const [prevStored, setPrevStored] = useState(JSON.stringify(stored))
  if (JSON.stringify(stored) !== prevStored) {
    setPrevStored(JSON.stringify(stored))
    setDraft(stored)
  }
  const titleRef = useRef<HTMLInputElement>(null)
  const textRef = useRef<HTMLTextAreaElement>(null)
  const actionRef = useRef<HTMLTextAreaElement>(null)
  const [focus, setFocus] = useState<Key>('text')
  const errors = Object.fromEntries((['title', 'text', 'action'] as Key[]).map((k) => [k, checkMessageText(draft[k] ?? '')])) as Record<Key, string>
  const commit = (next: NotificationMessage) => {
    const bad = (['title', 'text', 'action'] as Key[]).some((k) => checkMessageText(next[k] ?? ''))
    if (!bad && JSON.stringify(next) !== JSON.stringify(stored)) edit((d) => setMessage(d, rule, next))
  }
  const serverErr = (k: Key) => issues.find((i) => i.path.endsWith(`essage/${k}`))?.message

  const insert = (key: string) => {
    const el = focus === 'title' ? titleRef.current : focus === 'text' ? textRef.current : actionRef.current
    const cur = draft[focus] ?? ''
    const at = el?.selectionStart ?? cur.length
    const end = el?.selectionEnd ?? at
    const next = { ...draft, [focus]: cur.slice(0, at) + `{${key}}` + cur.slice(end) }
    setDraft(next)
    commit(next)
    requestAnimationFrame(() => {
      el?.focus()
      el?.setSelectionRange(at + key.length + 2, at + key.length + 2)
    })
  }

  // The preview speaks the language of the messages, not of the interface.
  const ml = notificationLang(desc)
  const thresholdText = thresholdIn(rule, ml)
  const values = previewValues(rule, desc.metadata.name, thresholdText, offsetLabel(offsetMinutes(getTimeZone(desc))))
  const fallbackTitle = rule.kind === 'custom' ? (rule.custom?.title ?? '') : defaultTitle(rule.kind, ml)
  const title = draft.title?.trim() ? fill(draft.title, values) : fallbackTitle
  const shared = rule.kind === 'errors' || rule.kind === 'errorsPerOp'
  const sharedLatency = rule.kind === 'latency' || rule.kind === 'latencyPerOp' || rule.kind === 'latencyAboveNormal'
  const sharedCPU = rule.kind === 'cpu' || rule.kind === 'cpuLimit' || rule.kind === 'cpuAboveNormal'

  return (
    <div className="rounded-md border">
      <button type="button" aria-expanded={open} onClick={() => setOpen((o) => !o)} className="flex w-full items-center justify-between px-2.5 py-2 text-left text-sm">
        <span>
          {t('message.heading')}
          <span className="ml-2 text-xs text-muted-foreground">{stored.title || stored.text || stored.action ? t('message.own') : t('message.default')}</span>
        </span>
        <ChevronDown className={cn('size-4 text-muted-foreground transition-transform', open && 'rotate-180')} />
      </button>
      {open && (
        <div className="space-y-3 border-t p-2.5">
          <p className="text-xs text-muted-foreground">
            {t('message.intro')}
            {shared && ` ${t('message.sharedErrors')}`}
            {sharedLatency && ` ${t('message.sharedLatency')}`}
            {sharedCPU && ` ${t('message.sharedCpu')}`}
            {(rule.kind === 'memory' || rule.kind === 'memoryLimit') && ` ${t('message.sharedMemory')}`}
          </p>
          <Field label={t('message.title')} error={errors.title || serverErr('title')}>
            <Input
              ref={titleRef}
              value={draft.title ?? ''}
              disabled={readOnly}
              placeholder={fallbackTitle}
              onFocus={() => setFocus('title')}
              onChange={(e) => setDraft({ ...draft, title: e.target.value })}
              onBlur={() => commit(draft)}
            />
          </Field>
          <Field label={t('message.text')} error={errors.text || serverErr('text')}>
            <Textarea
              ref={textRef}
              rows={2}
              value={draft.text ?? ''}
              disabled={readOnly}
              placeholder={t('message.textPlaceholder')}
              onFocus={() => setFocus('text')}
              onChange={(e) => setDraft({ ...draft, text: e.target.value })}
              onBlur={() => commit(draft)}
            />
          </Field>
          <Field label={t('message.action')} error={errors.action || serverErr('action')}>
            <Textarea
              ref={actionRef}
              rows={2}
              value={draft.action ?? ''}
              disabled={readOnly}
              placeholder={t('message.actionPlaceholder')}
              onFocus={() => setFocus('action')}
              onChange={(e) => setDraft({ ...draft, action: e.target.value })}
              onBlur={() => commit(draft)}
            />
          </Field>
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-xs text-muted-foreground">{t('message.insert')}</span>
            {PLACEHOLDER_KEYS.map((key) => (
              <Button key={key} type="button" variant="outline" size="xs" disabled={readOnly} onMouseDown={(e) => e.preventDefault()} onClick={() => insert(key)} title={`{${key}}`}>
                {placeholderLabel(key)}
              </Button>
            ))}
          </div>
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground">{t('message.previewTitle')}</div>
            <div className="rounded-lg bg-muted/60 p-2.5 text-sm leading-relaxed whitespace-pre-wrap">
              <div>
                {rule.severity === 'critical' ? '🔴' : '🟡'} <b>{tl(ml, rule.severity === 'critical' ? 'msg.word.critical' : 'msg.word.warning')}</b> · {desc.metadata.name}
              </div>
              <div className="mt-1.5">
                {rule.severity === 'critical' ? '🔴' : '🟡'} <b>{rule.service}</b>: {title}
              </div>
              <div className="text-muted-foreground">
                {tl(ml, 'msg.word.now')} <b>…</b>
                {thresholdText && `, ${tl(ml, 'msg.word.threshold')} ${thresholdText}`} · {tl(ml, 'msg.word.since')} {values.since}
              </div>
              {draft.text?.trim() && <div>{fill(draft.text, values)}</div>}
              {draft.action?.trim() && <div>👉 {fill(draft.action, values)}</div>}
            </div>
            <p className="text-xs text-muted-foreground">
              {t('message.previewHint')}
            </p>
          </div>
        </div>
      )}
    </div>
  )
}
