// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Settings of the selected service in plain words: what it is, whether it
// is important, what it depends on, its rules, and where its alerts go.
// Technical details (telemetry name, pods) are behind "Advanced".

import { useState } from 'react'
import { Mail, Plus, Send, Trash2, X } from 'lucide-react'
import { Field } from '@/components/common'
import { useConfirm } from '@/components/ConfirmDialog'
import RuleCard from '@/components/rules/RuleCard'
import AddRuleDialog from '@/components/rules/AddRuleDialog'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { useT } from '@/i18n'
import { destinationName, useEditor } from '@/lib/editor'
import { disconnect, removeService, renameService, updateService } from '@/lib/model/canvas'
import { addableKinds, addRule, deriveRules, kindLabel, type RuleKind } from '@/lib/model/rules'
import { receiversFor } from '@/lib/model/routing'
import { NAME_RE, SERVICE_TYPES, serviceTypeLabel, type Service, type ServiceType } from '@/lib/model/types'

interface Props {
  name: string
  onClose: () => void
  onRenamed: (name: string) => void
}

function NameField({ service, onRenamed }: { service: Service; onRenamed: (n: string) => void }) {
  const { desc, edit, readOnly } = useEditor()
  const { t } = useT()
  const [text, setText] = useState(service.name)
  const [prev, setPrev] = useState(service.name)
  if (prev !== service.name) {
    setPrev(service.name)
    setText(service.name)
  }
  const taken = text !== service.name && desc.spec.services.some((s) => s.name === text)
  const bad = !NAME_RE.test(text) || text.length > 63
  const commit = () => {
    if (text === service.name) return
    if (bad || taken) {
      setText(service.name)
      return
    }
    edit((d) => renameService(d, service.name, text))
    onRenamed(text)
  }
  return (
    <Field label={t('create.name')} htmlFor="svc-name" error={taken ? t('service.nameTaken') : bad ? t('service.nameFormat') : undefined}>
      <Input id="svc-name" value={text} disabled={readOnly} onChange={(e) => setText(e.target.value)} onBlur={commit} onKeyDown={(e) => e.key === 'Enter' && commit()} className="font-medium" />
    </Field>
  )
}

function TextSetting({ label, hint, value, onCommit, placeholder, mono }: { label: string; hint?: string; value: string; onCommit: (v: string) => void; placeholder?: string; mono?: boolean }) {
  const { readOnly } = useEditor()
  const [text, setText] = useState(value)
  const [prev, setPrev] = useState(value)
  if (prev !== value) {
    setPrev(value)
    setText(value)
  }
  return (
    <Field label={label} hint={hint}>
      <Input
        value={text}
        placeholder={placeholder}
        disabled={readOnly}
        className={mono ? 'font-mono text-xs' : undefined}
        onChange={(e) => setText(e.target.value)}
        onBlur={() => text !== value && onCommit(text)}
        onKeyDown={(e) => e.key === 'Enter' && text !== value && onCommit(text)}
      />
    </Field>
  )
}

export default function ServicePanel({ name, onClose, onRenamed }: Props) {
  const { desc, edit, readOnly, destinations, telegram, email } = useEditor()
  const [adding, setAdding] = useState<RuleKind | null>(null)
  const [confirmDialog, confirm] = useConfirm()
  const { t } = useT()
  const s = desc.spec.services.find((x) => x.name === name)
  if (!s) return null
  const rules = deriveRules(desc).filter((r) => r.service === name)
  const set = (fn: (x: Service) => void) => edit((d) => updateService(d, name, fn))
  const addable = addableKinds(desc, name)
  const callers = desc.spec.services.filter((x) => x.dependsOn?.includes(name)).map((x) => x.name)
  const route = (sev: 'critical' | 'warning') => receiversFor(destinations, name, sev)

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between gap-2 border-b px-4 py-3">
        <div className="min-w-0">
          <div className="text-xs text-muted-foreground">{serviceTypeLabel(s.type)}</div>
          <h2 className="truncate text-base font-semibold">{s.name}</h2>
        </div>
        <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label={t('common.close')}>
          <X />
        </Button>
      </div>
      <div className="flex-1 space-y-5 overflow-y-auto px-4 py-4">
        <section className="space-y-3">
          <NameField service={s} onRenamed={onRenamed} />
          <Field label={t('service.type')}>
            <Select
              value={s.type}
              disabled={readOnly}
              onValueChange={(ty) =>
                set((x) => {
                  x.type = ty as ServiceType
                  if (ty === 'queue' && !x.queue) x.queue = { topic: x.name, maxLag: 1000 }
                })
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SERVICE_TYPES.map((ty) => (
                  <SelectItem key={ty} value={ty}>
                    {serviceTypeLabel(ty)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <label className="flex items-start justify-between gap-3 rounded-lg border p-2.5">
            <span>
              <span className="block text-sm font-medium">{t('service.critical')}</span>
              <span className="block text-xs text-muted-foreground">{t('service.criticalHint')}</span>
            </span>
            <Switch checked={!!s.critical} disabled={readOnly} onCheckedChange={(on) => set((x) => (on ? (x.critical = true) : delete x.critical))} />
          </label>
          <TextSetting
            label={t('service.description')}
            value={s.description ?? ''}
            placeholder={t('service.descriptionPlaceholder')}
            onCommit={(v) => set((x) => (v.trim() ? (x.description = v.trim()) : delete x.description))}
          />
        </section>

        <section className="space-y-2">
          <h3 className="text-sm font-medium">{t('service.dependsOn')}</h3>
          {s.dependsOn?.length ? (
            <div className="flex flex-wrap gap-1.5">
              {s.dependsOn.map((d) => (
                <span key={d} className="inline-flex items-center gap-1 rounded-md border bg-muted/50 py-0.5 pr-1 pl-2 text-xs">
                  {d}
                  {!readOnly && (
                    <button type="button" aria-label={t('service.removeDep', { name: d })} className="rounded p-0.5 text-muted-foreground hover:text-destructive" onClick={() => edit((x) => disconnect(x, name, d))}>
                      <X className="size-3" />
                    </button>
                  )}
                </span>
              ))}
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">{t('service.noDeps')}</p>
          )}
          {callers.length > 0 && <p className="text-xs text-muted-foreground">{t('service.callers', { list: callers.join(', ') })}</p>}
        </section>

        <section className="space-y-2">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-medium">{t('system.view.rules')}</h3>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm" disabled={readOnly}>
                  <Plus /> {t('common.add')}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-64">
                {addable.map((k) => (
                  <DropdownMenuItem
                    key={k}
                    onSelect={() => {
                      if (k === 'errorsPerOp' || k === 'memory' || k === 'cpu' || k === 'slo') edit((d) => addRule(d, name, { kind: k }))
                      else setAdding(k)
                    }}
                  >
                    {kindLabel(k)}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
          {rules.length === 0 && <p className="text-xs text-muted-foreground">{t('service.noRules')}</p>}
          <div className="space-y-2">
            {rules.map((r) => (
              <RuleCard key={r.id} rule={r} />
            ))}
          </div>
        </section>

        <section className="space-y-2">
          <h3 className="text-sm font-medium">{t('service.routes')}</h3>
          {(['critical', 'warning'] as const).map((sev) => {
            const to = route(sev)
            return (
              <div key={sev} className="flex items-start gap-2 text-xs">
                <span className="flex w-28 shrink-0 items-center gap-1.5 pt-0.5 text-muted-foreground">
                  <span aria-hidden className={sev === 'critical' ? 'size-1.5 rounded-full bg-firing' : 'size-1.5 rounded-full bg-warn'} />
                  {sev === 'critical' ? t('routing.chip.critical') : t('routing.chip.warning')}
                </span>
                {to.length === 0 ? (
                  <span className="pt-0.5 text-warn">{t('service.nowhere')}</span>
                ) : (
                  <span className="flex min-w-0 flex-wrap gap-1">
                    {to.map((d) => (
                      <span key={d.receiver} className="inline-flex max-w-full items-center gap-1 truncate rounded-md border bg-muted/50 px-1.5 py-0.5">
                        {d.kind === 'telegram' ? <Send className="size-3 shrink-0 text-muted-foreground" /> : <Mail className="size-3 shrink-0 text-muted-foreground" />}
                        <span className="truncate">{destinationName(d, telegram, email)}</span>
                      </span>
                    ))}
                  </span>
                )}
              </div>
            )
          })}
        </section>

        <details className="group rounded-lg border px-3 py-2">
          <summary className="cursor-pointer text-sm text-muted-foreground">{t('service.advanced')}</summary>
          <div className="mt-3 space-y-3">
            <TextSetting label={t('service.owner')} value={s.owner ?? ''} placeholder={t('service.ownerPlaceholder')} onCommit={(v) => set((x) => (v.trim() ? (x.owner = v.trim()) : delete x.owner))} />
            <TextSetting
              label={t('service.telemetryName')}
              hint={t('service.telemetryNameHint')}
              value={s.telemetryName ?? ''}
              placeholder={s.name}
              mono
              onCommit={(v) => set((x) => (v.trim() ? (x.telemetryName = v.trim()) : delete x.telemetryName))}
            />
            <TextSetting
              label={t('service.namespace')}
              hint={desc.spec.defaults?.namespace ? t('service.namespaceDefault', { ns: desc.spec.defaults.namespace }) : t('service.namespaceAny')}
              value={s.workload?.namespace ?? ''}
              mono
              onCommit={(v) =>
                set((x) => {
                  x.workload = { ...(x.workload ?? {}), namespace: v.trim() || undefined }
                  if (!x.workload.namespace) delete x.workload.namespace
                  if (!Object.keys(x.workload).length) delete x.workload
                })
              }
            />
            <TextSetting
              label={t('service.pods')}
              hint={t('service.podsHint', { prefix: `${s.name}-` })}
              value={s.workload?.podRegex ?? ''}
              mono
              onCommit={(v) =>
                set((x) => {
                  x.workload = { ...(x.workload ?? {}), podRegex: v.trim() || undefined }
                  if (!x.workload.podRegex) delete x.workload.podRegex
                  if (!Object.keys(x.workload).length) delete x.workload
                })
              }
            />
            {s.type === 'queue' && s.queue && (
              <TextSetting label={t('service.topic')} value={s.queue.topic} mono onCommit={(v) => v.trim() && set((x) => (x.queue!.topic = v.trim()))} />
            )}
            <TextSetting label={t('service.runbookURL')} hint={t('service.runbookURLHint')} value={s.runbookURL ?? ''} onCommit={(v) => set((x) => (v.trim() ? (x.runbookURL = v.trim()) : delete x.runbookURL))} />
          </div>
        </details>
      </div>
      <div className="border-t px-4 py-2.5">
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground hover:text-destructive"
          disabled={readOnly}
          onClick={async () => {
            const ok = await confirm({
              title: t('service.remove.title', { name }),
              description: t('service.remove.text'),
              confirmLabel: t('service.remove.confirm'),
            })
            if (ok) {
              edit((d) => removeService(d, name))
              onClose()
            }
          }}
        >
          <Trash2 /> {t('service.remove')}
        </Button>
      </div>
      {confirmDialog}
      <AddRuleDialog open={adding !== null} onOpenChange={(o) => !o && setAdding(null)} service={name} initialKind={adding ?? undefined} />
    </div>
  )
}
