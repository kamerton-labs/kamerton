// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Email setup, in the same shape as Telegram: the mail server (the SMTP
// password is checked by the core and stored only in a Kubernetes Secret;
// it never comes back), named recipients shared by all systems, test
// emails. What goes where is chosen per system on the canvas.

import { useState, type KeyboardEvent } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { Loader2, Mail, MoreHorizontal, Pencil, Send, Server, Trash2, X } from 'lucide-react'
import { useForm, useWatch } from 'react-hook-form'
import { z } from 'zod'
import { cn } from 'cn'
import { useConfirm } from '@/components/ConfirmDialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  useAddEmailRecipient,
  useDeleteEmailRecipient,
  useDeleteEmailServer,
  useEmail,
  useSetEmailServer,
  useTestEmailRecipient,
  useUpdateEmailRecipient,
  type EmailRecipient,
  type EmailServer,
  type EmailStatus,
} from '@/lib/api'
import { t, useT } from '@/i18n'
import { presetFor, SMARTHOST_RE, smtpPresets, splitAddresses } from './email-form'
import { AddTile, ChannelCard, IconTile, NoUsesHint, PasswordInput, SecretLocation, StatusDot, TestResultLine, UseChips } from './parts'

// ---- Mail server ----

const serverSchema = z.object({
  smarthost: z.string().trim().regex(SMARTHOST_RE, { error: () => t('email.smarthostFormat') }),
  from: z.email({ error: () => t('email.fromFormat') }).trim(),
  username: z.string().trim().max(256),
  password: z.string().max(1024),
})
type ServerValues = z.infer<typeof serverSchema>

function ServerForm({ server, onDone, onCancel }: { server: EmailServer; onDone?: () => void; onCancel?: () => void }) {
  const save = useSetEmailServer()
  const { t } = useT()
  const editing = server.configured
  const { register, handleSubmit, formState, reset, setValue, control, getValues, setError } = useForm<ServerValues>({
    resolver: zodResolver(serverSchema),
    defaultValues: { smarthost: server.smarthost ?? '', from: server.from ?? '', username: server.username ?? '', password: '' },
  })
  const smarthost = useWatch({ control, name: 'smarthost' })
  const preset = presetFor(smarthost)
  // The stored password is kept when the login does not change.
  const canKeep = editing && server.hasPassword
  return (
    <form
      className="space-y-4"
      autoComplete="off"
      onSubmit={handleSubmit((v) => {
        const keep = canKeep && v.username === (server.username ?? '')
        if (v.username && !v.password && !keep) {
          setError('password', { message: t('email.passwordRequired') })
          return
        }
        save.mutate(v, {
          // Clear the password whatever the outcome: it must not linger in the page.
          onSettled: () => reset({ ...getValues(), password: '' }),
          onSuccess: () => onDone?.(),
        })
      })}
    >
      <div className="space-y-1.5">
        <Label htmlFor="smtp-host">{t('email.server')}</Label>
        <div className="flex flex-wrap gap-1.5" role="group" aria-label={t('email.presets')}>
          {smtpPresets().map((p) => (
            <button
              key={p.name}
              type="button"
              aria-pressed={smarthost === p.smarthost}
              onClick={() => setValue('smarthost', p.smarthost, { shouldValidate: true })}
              className={cn(
                'rounded-full border px-2.5 py-0.5 text-xs outline-none hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/50',
                smarthost === p.smarthost && 'border-foreground/40 bg-muted font-medium',
              )}
            >
              {p.name}
            </button>
          ))}
        </div>
        <Input id="smtp-host" className="max-w-md font-mono" placeholder="smtp.example.com:465" spellCheck={false} aria-invalid={!!formState.errors.smarthost} {...register('smarthost')} />
        {formState.errors.smarthost ? (
          <p className="text-xs text-destructive">{formState.errors.smarthost.message}</p>
        ) : (
          <p className="text-xs text-muted-foreground">{preset ? preset.hint : t('email.serverHint')}</p>
        )}
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor="smtp-from">{t('email.from')}</Label>
          <Input id="smtp-from" type="email" placeholder="alerts@example.com" aria-invalid={!!formState.errors.from} {...register('from')} />
          {formState.errors.from && <p className="text-xs text-destructive">{formState.errors.from.message}</p>}
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="smtp-user">{t('email.username')}</Label>
          <Input id="smtp-user" placeholder={t('email.usernamePlaceholder')} spellCheck={false} {...register('username')} />
        </div>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="smtp-password">{t('login.password')}</Label>
        <PasswordInput
          id="smtp-password"
          autoComplete="new-password"
          wrapperClassName="max-w-md"
          placeholder={canKeep ? t('email.passwordKeep') : t('email.passwordApp')}
          aria-invalid={!!formState.errors.password}
          {...register('password')}
        />
        {formState.errors.password && <p className="text-xs text-destructive">{formState.errors.password.message}</p>}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? <Loader2 className="animate-spin" /> : <Server />} {editing ? t('tg.checkSave') : t('email.checkConnect')}
        </Button>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
        )}
      </div>
      {save.isError && (
        <Alert variant="destructive">
          <AlertTitle>{t('email.notConnected')}</AlertTitle>
          <AlertDescription>{save.error.message}</AlertDescription>
        </Alert>
      )}
    </form>
  )
}

function ServerSummary({ server, onEdit }: { server: EmailServer; onEdit: () => void }) {
  const del = useDeleteEmailServer()
  const [confirmDialog, confirm] = useConfirm()
  const { t } = useT()
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3 rounded-xl border bg-muted/30 p-3">
        <IconTile icon={Mail} tint="email" size="lg" round />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{server.from}</div>
          <div className="truncate text-xs text-muted-foreground">
            {t('email.via')} <span className="font-mono">{server.smarthost}</span>
            {server.username ? ` · ${t('email.login', { user: server.username })}` : ` · ${t('email.noLogin')}`}
          </div>
        </div>
        <StatusDot tone={server.problem ? 'bad' : 'ok'} className="hidden sm:inline-flex">
          {server.problem ? t('tg.hasProblem') : t('email.connected')}
        </StatusDot>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" aria-label={t('email.serverActions')} disabled={del.isPending}>
              {del.isPending ? <Loader2 className="animate-spin" /> : <MoreHorizontal />}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-52">
            <DropdownMenuItem onSelect={onEdit}>
              <Pencil /> {t('email.editServer')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              onSelect={async () => {
                const ok = await confirm({
                  title: t('email.disconnect.title'),
                  description: t('email.disconnect.text'),
                  confirmLabel: t('email.disconnect.confirm'),
                })
                if (ok) del.mutate()
              }}
            >
              <Trash2 /> {t('email.disconnect')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {server.problem && (
        <Alert variant="destructive">
          <AlertTitle>{t('email.problem')}</AlertTitle>
          <AlertDescription>{server.problem}</AlertDescription>
        </Alert>
      )}
      {del.isError && (
        <Alert variant="destructive">
          <AlertTitle>{t('email.disconnectFailed')}</AlertTitle>
          <AlertDescription>{del.error.message}</AlertDescription>
        </Alert>
      )}
      {confirmDialog}
    </div>
  )
}

// ---- Recipients ----

/** Addresses as removable chips with a text field that turns typed or pasted text into chips. */
function AddressesInput({ id, value, onChange, invalid }: { id: string; value: string[]; onChange: (v: string[]) => void; invalid?: boolean }) {
  const [text, setText] = useState('')
  const [bad, setBad] = useState<string[]>([])
  const { t } = useT()
  const commit = (raw: string) => {
    const { added, invalid: wrong } = splitAddresses(raw, value)
    if (added.length) onChange([...value, ...added])
    setBad(wrong)
    setText(wrong.join(' '))
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',' || e.key === ';') {
      if (text.trim()) {
        e.preventDefault()
        commit(text)
      }
    } else if (e.key === 'Backspace' && !text && value.length) {
      onChange(value.slice(0, -1))
    }
  }
  return (
    <div className="space-y-1">
      <div
        className={cn(
          'flex min-h-8 flex-wrap items-center gap-1 rounded-lg border border-input px-1.5 py-1 focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/50 dark:bg-input/30',
          (invalid || bad.length > 0) && 'border-destructive',
        )}
      >
        {value.map((a) => (
          <span key={a} className="inline-flex max-w-full items-center gap-0.5 rounded-md bg-muted py-0.5 pr-0.5 pl-1.5 text-xs">
            <span className="truncate">{a}</span>
            <button type="button" aria-label={t('email.removeAddress', { address: a })} className="rounded p-0.5 text-muted-foreground hover:text-destructive" onClick={() => onChange(value.filter((x) => x !== a))}>
              <X className="size-3" />
            </button>
          </span>
        ))}
        <input
          id={id}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKey}
          onBlur={() => text.trim() && commit(text)}
          onPaste={(e) => {
            const pasted = e.clipboardData.getData('text')
            if (/[\s,;]/.test(pasted.trim())) {
              e.preventDefault()
              commit(text + ' ' + pasted)
            }
          }}
          placeholder={value.length ? '' : 'oncall@example.com'}
          className="h-6 min-w-32 flex-1 bg-transparent px-1 text-sm outline-none placeholder:text-muted-foreground"
          type="email"
          multiple
          spellCheck={false}
        />
      </div>
      {bad.length > 0 && <p className="text-xs text-destructive">{t('email.badAddresses', { list: bad.join(', ') })}</p>}
    </div>
  )
}

function RecipientForm({ recipient, onDone, onCancel, idPrefix }: { recipient?: EmailRecipient; onDone?: () => void; onCancel?: () => void; idPrefix: string }) {
  const add = useAddEmailRecipient()
  const update = useUpdateEmailRecipient()
  const m = recipient ? update : add
  const { t } = useT()
  const [name, setName] = useState(recipient?.name ?? '')
  const [to, setTo] = useState<string[]>(recipient?.to ?? [])
  const [tried, setTried] = useState(false)
  const nameBad = tried && !name.trim()
  const toBad = tried && to.length === 0
  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault()
        setTried(true)
        if (!name.trim() || to.length === 0) return
        const done = {
          onSuccess: () => {
            if (!recipient) {
              setName('')
              setTo([])
              setTried(false)
            }
            onDone?.()
          },
        }
        if (recipient) update.mutate({ id: recipient.id, name: name.trim(), to }, done)
        else add.mutate({ name: name.trim(), to }, done)
      }}
    >
      <div className="space-y-1.5">
        <Label htmlFor={`${idPrefix}-name`} className="text-xs">
          {t('email.recipientName')}
        </Label>
        <Input id={`${idPrefix}-name`} value={name} onChange={(e) => setName(e.target.value)} placeholder={t('email.recipientNamePlaceholder')} maxLength={64} aria-invalid={nameBad} />
        {nameBad && <p className="text-xs text-destructive">{t('email.recipientNameRequired')}</p>}
      </div>
      <div className="space-y-1.5">
        <Label htmlFor={`${idPrefix}-to`} className="text-xs">
          {t('email.addresses')}
        </Label>
        <AddressesInput id={`${idPrefix}-to`} value={to} onChange={setTo} invalid={toBad} />
        {toBad ? <p className="text-xs text-destructive">{t('email.addressesRequired')}</p> : <p className="text-xs text-muted-foreground">{t('email.addressesHint')}</p>}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" disabled={m.isPending}>
          {m.isPending && <Loader2 className="animate-spin" />} {recipient ? t('system.save') : t('email.addRecipient')}
        </Button>
        {onCancel && (
          <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
        )}
      </div>
      {m.isError && <p className="text-xs text-destructive">{m.error.message}</p>}
    </form>
  )
}

function RecipientCard({ recipient: r, onDelete, deleting }: { recipient: EmailRecipient; onDelete: () => void; deleting: boolean }) {
  const [editing, setEditing] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; error?: string } | 'pending'>()
  const test = useTestEmailRecipient()
  const { t, tn } = useT()
  const runTest = () => {
    setResult('pending')
    test.mutate(r.id, { onSuccess: (res) => setResult(res), onError: (err) => setResult({ ok: false, error: err.message }) })
  }
  return (
    <article className="flex flex-col gap-3 rounded-xl border bg-card p-4" aria-label={r.name}>
      <div className="flex items-start gap-3">
        <IconTile icon={Mail} tint="email" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{r.name}</div>
          <div className="text-xs text-muted-foreground">{tn('count.addresses', r.to.length)}</div>
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" className="-mt-1 -mr-1 text-muted-foreground" aria-label={t('email.recipientActions', { name: r.name })} disabled={deleting}>
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-48">
            <DropdownMenuItem onSelect={() => setEditing(true)}>
              <Pencil /> {t('common.edit')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={onDelete}>
              <Trash2 /> {t('common.delete')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {editing ? (
        <RecipientForm recipient={r} idPrefix={`rcpt-${r.id}`} onDone={() => setEditing(false)} onCancel={() => setEditing(false)} />
      ) : (
        <>
          <ul className="flex flex-wrap gap-1" aria-label={t('email.addresses')}>
            {r.to.map((a) => (
              <li key={a} className="max-w-full truncate rounded-md bg-muted px-1.5 py-0.5 text-xs">
                {a}
              </li>
            ))}
          </ul>
          <div className="flex-1 space-y-1.5">
            <div className="text-xs font-medium text-muted-foreground">{t('tg.usedBy')}</div>
            <UseChips uses={r.usedIn} empty={<NoUsesHint />} />
          </div>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t pt-3">
            <Button variant="outline" size="sm" disabled={result === 'pending'} onClick={runTest}>
              {result === 'pending' ? <Loader2 className="animate-spin" /> : <Send />} {t('email.testEmail')}
            </Button>
            <TestResultLine result={result === 'pending' ? undefined : result} />
          </div>
        </>
      )}
    </article>
  )
}

function AddRecipientTile() {
  const [open, setOpen] = useState(false)
  const { t } = useT()
  return (
    <AddTile title={t('email.addRecipient')}>
      {open ? (
        <RecipientForm idPrefix="rcpt-new" onDone={() => setOpen(false)} onCancel={() => setOpen(false)} />
      ) : (
        <>
          <p className="text-sm text-muted-foreground">{t('email.recipientHint')}</p>
          <Button variant="outline" size="sm" className="mt-auto self-start" onClick={() => setOpen(true)}>
            {t('common.add')}
          </Button>
        </>
      )}
    </AddTile>
  )
}

function RecipientsSection({ status }: { status: EmailStatus }) {
  const del = useDeleteEmailRecipient()
  const [confirmDialog, confirm] = useConfirm()
  const { t } = useT()
  const remove = async (r: EmailRecipient) => {
    const ok = await confirm({
      title: t('email.deleteRecipient.title', { name: r.name }),
      description: t('tg.removeChat.text'),
    })
    if (ok) del.mutate(r.id)
  }
  return (
    <section className="space-y-3" aria-label={t('email.recipients')}>
      <h3 className="text-sm font-semibold">{t('email.recipients')}</h3>
      {status.recipients.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-6 py-8 text-center">
          <IconTile icon={Mail} tint="email" size="lg" round />
          <div className="space-y-1">
            <div className="text-sm font-medium">{t('email.firstRecipient')}</div>
            <p className="mx-auto max-w-md text-sm text-muted-foreground">{t('email.firstRecipientHint')}</p>
          </div>
          <div className="w-full max-w-sm text-left">
            <RecipientForm idPrefix="rcpt-first" />
          </div>
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {status.recipients.map((r) => (
            <RecipientCard key={r.id} recipient={r} onDelete={() => void remove(r)} deleting={del.isPending} />
          ))}
          <AddRecipientTile />
        </div>
      )}
      {del.isError && <p className="text-xs text-destructive">{del.error.message}</p>}
      <p className="text-xs text-muted-foreground">{t('email.perSystem')}</p>
      {confirmDialog}
    </section>
  )
}

function EmailCard({ status }: { status: EmailStatus }) {
  const [editing, setEditing] = useState(false)
  const { t, tn } = useT()
  const s = status.server
  return (
    <ChannelCard
      icon={Mail}
      tint="email"
      title="Email"
      testId="channel-email"
      status={
        s.configured ? (
          <StatusDot tone={s.problem ? 'bad' : 'ok'}>
            {s.problem ? t('email.status.problem') : `${t('email.status.ok')} · ${tn('count.recipients', status.recipients.length)}`}
          </StatusDot>
        ) : (
          <StatusDot tone="muted">{t('stacks.state.notConnected')}</StatusDot>
        )
      }
    >
      {!s.configured || editing ? (
        <div className="space-y-5">
          {!s.configured && <p className="text-sm text-muted-foreground">{t('email.intro')}</p>}
          <ServerForm server={s} onDone={() => setEditing(false)} onCancel={editing ? () => setEditing(false) : undefined} />
          <SecretLocation what={t('email.passwordWord')} secret={s} />
        </div>
      ) : (
        <>
          <ServerSummary server={s} onEdit={() => setEditing(true)} />
          <RecipientsSection status={status} />
        </>
      )}
    </ChannelCard>
  )
}

export default function EmailSetup() {
  const em = useEmail()
  const { t } = useT()
  if (em.isLoading) return <Skeleton className="h-40 rounded-xl" />
  if (em.isError)
    return (
      <Alert variant="destructive">
        <AlertTitle>{t('email.loadError')}</AlertTitle>
        <AlertDescription>{em.error.message}</AlertDescription>
      </Alert>
    )
  const data = em.data
  if (!data) return null
  if (!data.available)
    return (
      <ChannelCard icon={Mail} tint="email" title="Email" testId="channel-email" status={<StatusDot tone="muted">{t('channel.unavailable')}</StatusDot>}>
        <div className="space-y-1">
          <p className="text-sm font-medium">{t('email.unavailable')}</p>
          <p className="text-sm text-muted-foreground">{data.reason}</p>
          <p className="text-xs text-muted-foreground">{t('email.unavailableHint')}</p>
        </div>
      </ChannelCard>
    )
  return <EmailCard status={data.status} />
}
