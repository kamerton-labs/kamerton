// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// "Where to send alerts" on the canvas. Routing is "severity × set of services ->
// channel"; drawn as edges it would be a line from almost every service to
// every channel, so it is a compact list docked on the canvas instead: each
// channel with what it receives as chips, changed in a small dialog.
// Selecting a service shows, in its panel, which channels get its alerts.

import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { AlertTriangle, Check, ChevronDown, Clock, Languages, Mail, Plus, Search, Send, Trash2 } from 'lucide-react'
import { cn } from 'cn'
import { chatTypeIcon } from '@/components/notifications/chat-type'
import { Chip, IconTile } from '@/components/notifications/parts'
import StackPicker from '@/components/stacks/StackPicker'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { LANGS, useT } from '@/i18n'
import type { EmailRecipient, TelegramChat } from '@/lib/api'
import { destinationName, useEditor } from '@/lib/editor'
import {
  addEmailDestination,
  addTelegramDestination,
  conditionChips,
  conditionSentence,
  removeDestination,
  setDestination,
  type Destination,
} from '@/lib/model/routing'
import { allTimeZones, browserTimeZone, getTimeZone, setTimeZone, zoneLabel } from '@/lib/model/timezone'
import { notificationLang, setNotificationLang } from '@/lib/model/message'
import type { Severity } from '@/lib/model/types'

/** What the dialog edits: an existing receiver, or a chat / recipient being added. */
type Target =
  | { mode: 'edit'; dest: Destination }
  | { mode: 'new-telegram'; chat: TelegramChat }
  | { mode: 'new-email'; recipient: EmailRecipient }

const severityTiles = [
  { sev: 'critical', title: 'routing.chip.critical', hint: 'dest.criticalHint' },
  { sev: 'warning', title: 'routing.chip.warning', hint: 'dest.warningHint' },
] as const

function SeverityTile({ sev, title, hint, on, onToggle, disabled }: { sev: Severity; title: string; hint: string; on: boolean; onToggle: () => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={on}
      disabled={disabled}
      onClick={onToggle}
      className={cn(
        'flex w-full items-start gap-2.5 rounded-lg border p-3 text-left transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50',
        on ? 'border-foreground/30 bg-muted/60' : 'hover:bg-muted/40',
      )}
    >
      <span
        aria-hidden
        className={cn('mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-[4px] border', on ? 'border-primary bg-primary text-primary-foreground' : 'border-input')}
      >
        {on && <Check className="size-3" />}
      </span>
      <span className="min-w-0">
        <span className="flex items-center gap-1.5 text-sm font-medium">
          <span aria-hidden className={cn('inline-block size-1.5 rounded-full', sev === 'critical' ? 'bg-firing' : 'bg-warn')} />
          {title}
        </span>
        <span className="mt-0.5 block text-xs text-muted-foreground">{hint}</span>
      </span>
    </button>
  )
}

function Segmented({ value, onChange, options, label }: { value: string; onChange: (v: string) => void; options: { value: string; label: string }[]; label: string }) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex w-full rounded-lg bg-muted p-0.5">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          onClick={() => onChange(o.value)}
          className={cn(
            'flex-1 rounded-md px-3 py-1 text-sm transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50',
            value === o.value ? 'bg-background font-medium shadow-sm dark:bg-input/60' : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

function ServiceChecklist({ selected, onChange }: { selected: string[]; onChange: (v: string[]) => void }) {
  const { desc } = useEditor()
  const { t } = useT()
  const [query, setQuery] = useState('')
  const all = desc.spec.services
  const q = query.trim().toLowerCase()
  const shown = q ? all.filter((s) => s.name.toLowerCase().includes(q)) : all
  const toggle = (name: string, on: boolean) => onChange(on ? [...new Set([...selected, name])] : selected.filter((x) => x !== name))
  return (
    <div className="space-y-2">
      <div className="relative">
        <Search aria-hidden className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t('dest.findService')} aria-label={t('dest.findService')} className="pl-8" />
      </div>
      <div className="flex items-center justify-between gap-2 text-xs">
        <span className="text-muted-foreground" aria-live="polite">
          {t('dest.selected', { n: selected.length, total: all.length })}
        </span>
        <span className="flex gap-1">
          <Button type="button" variant="ghost" size="xs" onClick={() => onChange([...new Set([...selected, ...shown.map((s) => s.name)])])}>
            {t('dest.selectAll')}
          </Button>
          <Button type="button" variant="ghost" size="xs" onClick={() => onChange(q ? selected.filter((x) => !shown.some((s) => s.name === x)) : [])}>
            {t('dest.clearAll')}
          </Button>
        </span>
      </div>
      <div className="max-h-52 space-y-0.5 overflow-y-auto rounded-lg border p-1">
        {shown.length === 0 && <p className="px-2 py-1.5 text-sm text-muted-foreground">{t('common.nothingFound')}</p>}
        {shown.map((s) => (
          <label key={s.name} className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1 text-sm hover:bg-muted">
            <Checkbox checked={selected.includes(s.name)} onCheckedChange={(on) => toggle(s.name, on === true)} />
            <span className="min-w-0 flex-1 truncate">{s.name}</span>
            {s.critical && (
              <span className="inline-flex items-center gap-1 text-[11px] text-muted-foreground" title={t('dest.criticalTitle')}>
                <span aria-hidden className="inline-block size-1.5 rounded-full bg-firing" />
                {t('node.critical')}
              </span>
            )}
          </label>
        ))}
      </div>
    </div>
  )
}

function DestinationDialog({ target, onClose }: { target: Target; onClose: () => void }) {
  const { desc, edit, telegram, email, readOnly } = useEditor()
  const { t } = useT()
  const dest = target.mode === 'edit' ? target.dest : undefined
  const initial = dest ?? { severities: ['critical', 'warning'] as Severity[], services: [] as string[] }
  const [sevs, setSevs] = useState<Severity[]>(initial.severities)
  const [only, setOnly] = useState(initial.services.length > 0)
  const [svcs, setSvcs] = useState<string[]>(initial.services)
  const [ack, setAck] = useState(false)
  const kind = target.mode === 'edit' ? target.dest.kind : target.mode === 'new-telegram' ? 'telegram' : 'email'
  const name =
    target.mode === 'edit' ? destinationName(target.dest, telegram, email) : target.mode === 'new-telegram' ? target.chat.title || String(target.chat.id) : target.recipient.name
  const sel = { severities: sevs, services: only ? svcs : [] }
  const nothing = sevs.length === 0
  const canSave = !readOnly && !(only && svcs.length === 0) && (!nothing || ack)

  const save = () => {
    if (target.mode === 'edit') edit((d) => setDestination(d, target.dest.receiver, sel))
    else if (target.mode === 'new-telegram' && telegram) {
      const secret = { name: telegram.token.secretName, key: telegram.token.secretKey }
      edit((d) => addTelegramDestination(d, target.chat.id, secret, sel))
    } else if (target.mode === 'new-email' && email) {
      edit((d) => addEmailDestination(d, target.recipient, email.server, sel))
    }
    onClose()
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2.5">
            <IconTile icon={kind === 'telegram' ? Send : Mail} tint={kind === 'telegram' ? 'telegram' : 'email'} size="sm" />
            <span className="min-w-0 truncate">{name}</span>
          </DialogTitle>
          <DialogDescription>{t('dest.dialogDescription', { system: desc.metadata.name })}</DialogDescription>
        </DialogHeader>
        <div className="space-y-5">
          <fieldset className="space-y-2">
            <legend className="mb-2 text-sm font-medium">{t('dest.which')}</legend>
            <div className="grid gap-2 sm:grid-cols-2">
              {severityTiles.map((tile) => (
                <SeverityTile
                  key={tile.sev}
                  sev={tile.sev}
                  title={t(tile.title)}
                  hint={t(tile.hint)}
                  on={sevs.includes(tile.sev)}
                  onToggle={() => {
                    setAck(false)
                    setSevs((cur) =>
                      cur.includes(tile.sev) ? cur.filter((x) => x !== tile.sev) : (['critical', 'warning'] as Severity[]).filter((x) => x === tile.sev || cur.includes(x)),
                    )
                  }}
                />
              ))}
            </div>
          </fieldset>
          <fieldset className="space-y-2">
            <legend className="mb-2 text-sm font-medium">{t('dest.whichServices')}</legend>
            <Segmented
              label={t('dest.whichServices')}
              value={only ? 'only' : 'all'}
              onChange={(v) => setOnly(v === 'only')}
              options={[
                { value: 'all', label: t('routing.allServices') },
                { value: 'only', label: t('dest.onlySelected') },
              ]}
            />
            {only && <ServiceChecklist selected={svcs} onChange={setSvcs} />}
          </fieldset>
          {dest?.custom && <p className="text-xs text-muted-foreground">{t('dest.custom')}</p>}
          <div
            role="status"
            className={cn('rounded-lg px-3 py-2 text-sm', nothing || (only && svcs.length === 0) ? 'bg-warn/10 text-foreground' : 'bg-muted/60')}
            data-testid="destination-summary"
          >
            {conditionSentence({ ...sel, only }, kind === 'telegram' ? 'chat' : 'email')}
          </div>
          {nothing && (
            <label className="flex items-start gap-2 text-sm">
              <Checkbox checked={ack} onCheckedChange={(v) => setAck(v === true)} className="mt-0.5" />
              <span>
                <span className="flex items-center gap-1.5 font-medium">
                  <AlertTriangle className="size-3.5 text-warn" /> {t('dest.nothing')}
                </span>
                <span className="text-xs text-muted-foreground">{target.mode === 'edit' ? t('dest.ackEdit') : t('dest.ackNew')}</span>
              </span>
            </label>
          )}
        </div>
        <DialogFooter className="sm:justify-between">
          {target.mode === 'edit' ? (
            <Button
              variant="ghost"
              className="text-muted-foreground hover:text-destructive"
              disabled={readOnly}
              onClick={() => {
                edit((d) => removeDestination(d, target.dest.receiver))
                onClose()
              }}
            >
              <Trash2 /> {t('dest.remove')}
            </Button>
          ) : (
            <Button variant="ghost" onClick={onClose}>
              {t('common.cancel')}
            </Button>
          )}
          <Button disabled={!canSave} onClick={save}>
            {target.mode === 'edit' ? t('common.done') : t('common.add')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** "Time in messages": the zone of the times in Telegram and email messages. */
function TimeZoneDialog({ onClose }: { onClose: () => void }) {
  const { desc, edit, readOnly } = useEditor()
  const { t, lang } = useT()
  const current = getTimeZone(desc)
  // First time: the browser's zone is proposed.
  const [chosen, setChosen] = useState(current || browserTimeZone())
  const [query, setQuery] = useState('')
  // eslint-disable-next-line react-hooks/exhaustive-deps -- labels follow the interface language
  const zones = useMemo(() => allTimeZones().map((z) => ({ zone: z, label: zoneLabel(z) })), [lang])
  const q = query.trim().toLowerCase()
  const shown = q ? zones.filter((z) => z.zone.toLowerCase().replace(/_/g, ' ').includes(q.replace(/_/g, ' ')) || z.label.toLowerCase().includes(q)) : zones
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Clock className="size-4" /> {t('dest.tz.title')}
          </DialogTitle>
          <DialogDescription>{t('dest.tz.description')}</DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Input autoFocus placeholder={t('dest.tz.placeholder')} value={query} onChange={(e) => setQuery(e.target.value)} aria-label={t('dest.tz.search')} />
          <div className="max-h-64 overflow-y-auto rounded-md border p-1" role="listbox" aria-label={t('dest.tz.list')}>
            {shown.length === 0 && <p className="px-2 py-1.5 text-sm text-muted-foreground">{t('common.nothingFound')}</p>}
            {shown.map((z) => (
              <button
                key={z.zone}
                type="button"
                role="option"
                aria-selected={z.zone === chosen}
                ref={z.zone === chosen && !q ? (el) => el?.scrollIntoView({ block: 'nearest' }) : undefined}
                onClick={() => setChosen(z.zone)}
                className={cn('flex w-full items-center justify-between gap-2 rounded px-2 py-1 text-left text-sm hover:bg-muted', z.zone === chosen && 'bg-muted font-medium')}
              >
                <span className="flex min-w-0 items-center gap-1.5">
                  <Check className={cn('size-3.5 shrink-0', z.zone === chosen ? 'opacity-100' : 'opacity-0')} />
                  <span className="truncate">{z.zone.replace(/_/g, ' ')}</span>
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">{z.label}</span>
              </button>
            ))}
          </div>
          {chosen === browserTimeZone() && <p className="text-xs text-muted-foreground">{t('dest.tz.browser')}</p>}
        </div>
        <DialogFooter className="sm:justify-between">
          <Button
            variant="ghost"
            className="text-muted-foreground"
            disabled={readOnly || !current}
            onClick={() => {
              edit((d) => setTimeZone(d, ''))
              onClose()
            }}
          >
            {t('dest.tz.reset')}
          </Button>
          <Button
            disabled={readOnly || !chosen}
            onClick={() => {
              edit((d) => setTimeZone(d, chosen === 'UTC' ? '' : chosen))
              onClose()
            }}
          >
            {t('common.done')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** Language of the notification messages (spec.notifications.language): English unless set to Russian. */
function MessageLanguage() {
  const { desc, edit, readOnly } = useEditor()
  const { t } = useT()
  const current = notificationLang(desc)
  return (
    <div className="flex items-center gap-2 px-1.5 py-1 text-xs text-muted-foreground" data-testid="message-language">
      <Languages className="size-3.5 shrink-0" />
      <span className="min-w-0 flex-1 truncate">{t('dest.msgLang')}</span>
      <div role="radiogroup" aria-label={t('dest.msgLang')} className="flex rounded-md bg-muted p-0.5">
        {LANGS.map((l) => (
          <button
            key={l}
            type="button"
            role="radio"
            aria-checked={current === l}
            disabled={readOnly}
            title={t(`dest.msgLang.${l}` as const)}
            onClick={() => current !== l && edit((d) => setNotificationLang(d, l))}
            className={cn('rounded px-1.5 py-0.5 font-medium uppercase disabled:opacity-50', current === l ? 'bg-background text-foreground shadow-sm' : 'hover:text-foreground')}
          >
            {l}
          </button>
        ))}
      </div>
    </div>
  )
}

function DestinationCard({ dest, name, onOpen }: { dest: Destination; name: string; onOpen: () => void }) {
  const nothing = dest.severities.length === 0
  const { t } = useT()
  return (
    <button
      type="button"
      onClick={onOpen}
      className={cn(
        'flex w-full rounded-lg border bg-background p-2 text-left transition-colors outline-none hover:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/50 dark:bg-input/20',
        nothing && 'border-warn/50 bg-warn/5',
      )}
      aria-label={t('dest.cardAria', { name })}
    >
      <span className="min-w-0 flex-1 space-y-1.5">
        <span className="flex items-center gap-2">
          <IconTile icon={dest.kind === 'telegram' ? Send : Mail} tint={nothing ? 'warn' : dest.kind === 'telegram' ? 'telegram' : 'email'} size="xs" />
          <span className="min-w-0 truncate text-sm font-medium">{name}</span>
        </span>
        <span className="flex flex-wrap gap-1">
          {conditionChips(dest).map((c) => (
            <Chip key={c.label} tone={c.tone}>
              {c.label}
            </Chip>
          ))}
        </span>
      </span>
    </button>
  )
}

export default function DestinationsPanel({ className }: { className?: string }) {
  const { desc, destinations, telegram, email, readOnly } = useEditor()
  const [open, setOpen] = useState(true)
  const [target, setTarget] = useState<Target | null>(null)
  const [zoneOpen, setZoneOpen] = useState(false)
  const { t, tn } = useT()
  const zone = getTimeZone(desc)
  const freeChats = (telegram?.chats ?? []).filter((c) => c.available && !destinations.some((d) => d.chatID === c.id))
  const emailReady = !!email?.server.configured
  const freeRecipients = emailReady ? (email?.recipients ?? []).filter((r) => !destinations.some((d) => d.receiver === r.receiver)) : []
  const anyChannel = !!telegram?.token.configured || emailReady
  // Opened on the next tick: the menu must finish closing (and returning focus) first.
  const pick = (next: Target) => setTimeout(() => setTarget(next), 0)
  // A destination that was just edited may have been removed meanwhile.
  const current = target?.mode === 'edit' ? destinations.find((d) => d.receiver === target.dest.receiver) : undefined

  return (
    <div className={cn('w-72 rounded-lg border bg-card', className)}>
      <button type="button" aria-expanded={open} className="flex w-full items-center justify-between px-3 py-2 text-left text-sm font-medium" onClick={() => setOpen((o) => !o)}>
        {t('dest.title')}
        <ChevronDown className={cn('size-4 text-muted-foreground transition-transform', !open && '-rotate-90')} />
      </button>
      {open && (
        <div className="space-y-1.5 border-t p-2">
          {destinations.length === 0 && <p className="px-1 py-1 text-xs text-muted-foreground">{t('dest.none')}</p>}
          {destinations.map((d) => (
            <DestinationCard key={d.receiver} dest={d} name={destinationName(d, telegram, email)} onOpen={() => setTarget({ mode: 'edit', dest: d })} />
          ))}
          {freeChats.length > 0 || freeRecipients.length > 0 ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="sm" className="w-full justify-start text-muted-foreground" disabled={readOnly}>
                  <Plus /> {t('dest.addReceiver')}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-64">
                {freeChats.length > 0 && (
                  <>
                    <DropdownMenuLabel className="text-xs text-muted-foreground">{t('dest.telegramChats')}</DropdownMenuLabel>
                    {freeChats.map((c) => {
                      const Icon = chatTypeIcon(c.type)
                      return (
                        <DropdownMenuItem key={c.id} onSelect={() => pick({ mode: 'new-telegram', chat: c })}>
                          <Icon /> <span className="truncate">{c.title || c.id}</span>
                        </DropdownMenuItem>
                      )
                    })}
                  </>
                )}
                {freeRecipients.length > 0 && (
                  <>
                    {freeChats.length > 0 && <DropdownMenuSeparator />}
                    <DropdownMenuLabel className="text-xs text-muted-foreground">Email</DropdownMenuLabel>
                    {freeRecipients.map((r) => (
                      <DropdownMenuItem key={r.id} onSelect={() => pick({ mode: 'new-email', recipient: r })}>
                        <Mail />
                        <span className="min-w-0">
                          <span className="block truncate">{r.name}</span>
                          <span className="block truncate text-xs text-muted-foreground">{r.to.length === 1 ? r.to[0] : tn('count.addresses', r.to.length)}</span>
                        </span>
                      </DropdownMenuItem>
                    ))}
                  </>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem asChild>
                  <Link to="/channels" className="text-xs text-muted-foreground">
                    {t('dest.otherInChannels')}
                  </Link>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : (
            <Link to="/channels" className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline">
              <Plus className="size-3.5" />
              {anyChannel ? t('dest.addInChannels') : t('dest.connectInChannels')}
            </Link>
          )}
          <button
            type="button"
            onClick={() => setZoneOpen(true)}
            className="mt-1 flex w-full items-center gap-2 border-t px-1.5 pt-2 pb-1 text-left text-xs text-muted-foreground hover:text-foreground"
            title={t('dest.tz.hint')}
          >
            <Clock className="size-3.5 shrink-0" />
            <span className="min-w-0 truncate">{t('dest.tz.row', { zone: zone ? `${zoneLabel(zone)} · ${zone.replace(/_/g, ' ')}` : 'UTC' })}</span>
          </button>
          <MessageLanguage />
          <StackPicker />
        </div>
      )}
      {zoneOpen && <TimeZoneDialog onClose={() => setZoneOpen(false)} />}
      {target?.mode === 'edit' && current && <DestinationDialog key={current.receiver} target={{ mode: 'edit', dest: current }} onClose={() => setTarget(null)} />}
      {target && target.mode !== 'edit' && <DestinationDialog key={target.mode} target={target} onClose={() => setTarget(null)} />}
    </div>
  )
}
