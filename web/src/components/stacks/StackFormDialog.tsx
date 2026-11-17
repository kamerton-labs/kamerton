// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Add a set of programs (name and addresses, typed or taken from found
// programs) or rename one. The advanced part holds the extra labels for
// PrometheusRule and AlertmanagerConfig objects: which Prometheus Operator
// instance picks up the rules of systems using the set.

import { useState } from 'react'
import { ChevronDown, Loader2 } from 'lucide-react'
import { cn } from 'cn'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useCreateStack, useUpdateStack, type ProgramKind, type StackSet } from '@/lib/api'
import { useT } from '@/i18n'
import { KINDS, labelsToText, programInfo, stackName, textToLabels, type StackDraft } from '@/lib/stacks'

/** Create (no set) or edit (set given: name and labels; addresses are changed on the cards). */
export default function StackFormDialog({ set, draft, onClose }: { set?: StackSet; draft?: StackDraft; onClose: () => void }) {
  const editing = !!set
  const { t, rich } = useT()
  const [name, setName] = useState(set ? stackName(set.id, set.name) : (draft?.name ?? ''))
  const [urls, setUrls] = useState<Partial<Record<ProgramKind, string>>>(draft?.urls ?? {})
  const [ruleText, setRuleText] = useState(labelsToText(set?.ruleLabels ?? draft?.ruleLabels))
  const [amcText, setAmcText] = useState(labelsToText(set?.alertmanagerConfigLabels ?? draft?.alertmanagerConfigLabels))
  const hasLabels = !!(ruleText || amcText)
  const [advanced, setAdvanced] = useState(hasLabels)
  const create = useCreateStack()
  const update = useUpdateStack()
  const m = editing ? update : create
  const rule = textToLabels(ruleText)
  const amc = textToLabels(amcText)
  const labelError = rule.error ?? amc.error

  const submit = () => {
    if (labelError) return
    const body = { name: name.trim(), ruleLabels: rule.labels, alertmanagerConfigLabels: amc.labels }
    if (set) update.mutate({ id: set.id, ...body }, { onSuccess: onClose })
    else
      create.mutate({ ...body, urls: Object.fromEntries(Object.entries(urls).filter(([, v]) => v?.trim())) }, { onSuccess: onClose })
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-lg" data-testid="stack-form">
        <DialogHeader>
          <DialogTitle>{editing ? t('stackBlock.title', { name: stackName(set.id, set.name) }) : t('stackForm.newTitle')}</DialogTitle>
          <DialogDescription>{editing ? t('stackForm.editDescription') : t('stackForm.newDescription')}</DialogDescription>
        </DialogHeader>
        <form
          id="stack-form"
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="stack-name">{t('email.recipientName')}</Label>
            <Input id="stack-name" data-testid="stack-name-input" autoFocus placeholder={t('stackForm.namePlaceholder')} value={name} onChange={(e) => setName(e.target.value)} maxLength={60} />
          </div>
          {!editing && (
            <div className="space-y-2">
              {KINDS.map((k) => (
                <div key={k} className="grid gap-1 sm:grid-cols-[10rem_1fr] sm:items-center sm:gap-3">
                  <Label htmlFor={`stack-url-${k}`} className="text-sm">
                    {programInfo[k].title}
                  </Label>
                  <Input
                    id={`stack-url-${k}`}
                    data-testid={`stack-url-${k}`}
                    className="font-mono text-xs"
                    spellCheck={false}
                    autoComplete="off"
                    placeholder={programInfo[k].example}
                    value={urls[k] ?? ''}
                    onChange={(e) => setUrls((u) => ({ ...u, [k]: e.target.value }))}
                  />
                </div>
              ))}
            </div>
          )}
          <div className="rounded-lg border">
            <button
              type="button"
              aria-expanded={advanced}
              data-testid="stack-advanced"
              onClick={() => setAdvanced((a) => !a)}
              className="flex w-full items-center justify-between px-3 py-2 text-left text-sm font-medium"
            >
              {t('stackForm.advanced')}
              <ChevronDown className={cn('size-4 text-muted-foreground transition-transform', !advanced && '-rotate-90')} />
            </button>
            {advanced && (
              <div className="space-y-3 border-t p-3">
                <p className="text-xs text-muted-foreground">
                  {rich('stackForm.labelsHint', { format: <code>{t('stackForm.labelFormat')}</code> })}
                </p>
                {draft?.labelsNote && <p className="text-xs text-ok">{draft.labelsNote}</p>}
                <div className="space-y-1.5">
                  <Label htmlFor="stack-rule-labels">{t('stackForm.ruleLabels')}</Label>
                  <Textarea id="stack-rule-labels" data-testid="stack-rule-labels" className="font-mono text-xs" rows={2} placeholder="release=kube-prometheus-stack" value={ruleText} onChange={(e) => setRuleText(e.target.value)} />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="stack-amc-labels">{t('stackForm.routeLabels')}</Label>
                  <Textarea id="stack-amc-labels" data-testid="stack-amc-labels" className="font-mono text-xs" rows={2} placeholder="alertmanagerConfig=team-a" value={amcText} onChange={(e) => setAmcText(e.target.value)} />
                </div>
                {labelError && <p className="text-xs text-destructive">{labelError}</p>}
              </div>
            )}
          </div>
          {m.isError && <p className="text-sm text-destructive">{t('system.saveError', { error: m.error.message })}</p>}
        </form>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="stack-form" data-testid="stack-save" disabled={!name.trim() || m.isPending || !!labelError}>
            {m.isPending && <Loader2 className="animate-spin" />} {editing ? t('system.save') : t('status.addSet')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
