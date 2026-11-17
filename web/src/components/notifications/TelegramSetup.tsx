// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Telegram setup: bot token (validated by the core with getMe and stored in a
// Kubernetes Secret), chats discovered by the core, test messages, and which
// systems send to each chat. What goes where is chosen per system (canvas,
// "Where to send alerts").
// The token never comes back from the API; this screen only shows the bot's
// public name.

import { useState } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { Bot, Copy, ExternalLink, Hash, Loader2, MoreHorizontal, RefreshCw, Send, Trash2 } from 'lucide-react'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { cn } from 'cn'
import { t, useT } from '@/i18n'
import { useConfirm } from '@/components/ConfirmDialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  useAddTelegramChat,
  useDeleteTelegramChat,
  useDeleteTelegramToken,
  useSetTelegramToken,
  useTelegram,
  useTestTelegramChat,
  type TelegramChat,
  type TelegramStatus,
  type TelegramTestResult,
} from '@/lib/api'
import { chatStatus, chatTypeIcon, chatTypeLabel, pollingState } from './chat-type'
import { AddTile, ChannelCard, CopyButton, IconTile, NoUsesHint, PasswordInput, SecretLocation, StatusDot, Step, TestResultLine, UseChips } from './parts'

const tokenSchema = z.object({
  token: z
    .string()
    .trim()
    .regex(/^\d{5,20}:[A-Za-z0-9_-]{30,64}$/, { error: () => t('tg.tokenFormat') }),
})

function TokenForm({ onDone, onCancel, submitLabel }: { onDone?: () => void; onCancel?: () => void; submitLabel: string }) {
  const setToken = useSetTelegramToken()
  const { t: tr } = useT()
  const { register, handleSubmit, formState, reset } = useForm<z.infer<typeof tokenSchema>>({
    resolver: zodResolver(tokenSchema),
    defaultValues: { token: '' },
  })
  return (
    <form
      className="space-y-3"
      autoComplete="off"
      onSubmit={handleSubmit((v) =>
        setToken.mutate(v.token, {
          // Clear the field whatever the outcome: the token must not linger in the page.
          onSettled: () => reset({ token: '' }),
          onSuccess: () => onDone?.(),
        }),
      )}
    >
      <div className="space-y-1.5">
        <Label htmlFor="bot-token">{tr('tg.token')}</Label>
        <PasswordInput
          id="bot-token"
          autoComplete="off"
          spellCheck={false}
          placeholder="123456789:AAH…"
          wrapperClassName="max-w-md"
          className="font-mono"
          aria-invalid={!!formState.errors.token}
          {...register('token')}
        />
        {formState.errors.token && <p className="text-xs text-destructive">{formState.errors.token.message}</p>}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={setToken.isPending}>
          {setToken.isPending ? <Loader2 className="animate-spin" /> : <Bot />} {submitLabel}
        </Button>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel}>
            {tr('common.cancel')}
          </Button>
        )}
      </div>
      {setToken.isError && (
        <Alert variant="destructive">
          <AlertTitle>{tr('tg.tokenNotSaved')}</AlertTitle>
          <AlertDescription>{setToken.error.message}</AlertDescription>
        </Alert>
      )}
    </form>
  )
}

/** Guided flow: create a bot with BotFather and paste its token. */
function BotSetup({ t: token, replacing, onDone, onCancel }: { t: TelegramStatus['token']; replacing: boolean; onDone: () => void; onCancel?: () => void }) {
  const { t: tr } = useT()
  return (
    <div className="space-y-5">
      {!replacing && <p className="text-sm text-muted-foreground">{tr('tg.setupIntro')}</p>}
      <ol className="space-y-5">
        <Step n={1}>
          <p>{tr('tg.step1')}</p>
          <Button asChild variant="outline">
            <a href="https://t.me/BotFather" target="_blank" rel="noreferrer">
              <Send /> {tr('tg.openBotFather')} <ExternalLink className="text-muted-foreground" />
            </a>
          </Button>
        </Step>
        <Step n={2}>
          <p className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
            {tr('tg.step2a')}
            <span className="inline-flex items-center gap-1 rounded-md border bg-muted/60 py-0.5 pr-1 pl-1.5">
              <code className="font-mono text-[13px]">/newbot</code>
              <CopyButton value="/newbot" label={tr('tg.copyNewbot')} className="p-0.5" />
            </span>
            {tr('tg.step2b')}
          </p>
        </Step>
        <Step n={3}>
          <p>{tr('tg.step3')}</p>
          <TokenForm submitLabel={replacing ? tr('tg.checkReplace') : tr('tg.checkSave')} onDone={onDone} onCancel={onCancel} />
        </Step>
      </ol>
      <SecretLocation what={tr('tg.tokenWord')} secret={token} />
    </div>
  )
}

/** The connected bot: avatar, name, @username, status and a quiet menu. */
function BotSummary({ t, onReplace }: { t: TelegramStatus['token']; onReplace: () => void }) {
  const del = useDeleteTelegramToken()
  const [confirmDialog, confirm] = useConfirm()
  const { t: tr } = useT()
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3 rounded-xl border bg-muted/30 p-3">
        <IconTile icon={Bot} tint="telegram" size="lg" round />
        <div className="min-w-0 flex-1">
          {t.bot ? (
            <>
              <div className="truncate text-sm font-medium">{t.bot.firstName}</div>
              <a className="text-xs text-muted-foreground underline-offset-2 hover:underline" href={`https://t.me/${encodeURIComponent(t.bot.username)}`} target="_blank" rel="noreferrer">
                @{t.bot.username}
              </a>
            </>
          ) : (
            <div className="text-sm text-muted-foreground">{tr('tg.botUnchecked')}</div>
          )}
        </div>
        <StatusDot tone={t.problem ? 'bad' : 'ok'} className="hidden sm:inline-flex">
          {t.problem ? tr('tg.hasProblem') : tr('tg.connected')}
        </StatusDot>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" aria-label={tr('tg.botActions')} disabled={del.isPending}>
              {del.isPending ? <Loader2 className="animate-spin" /> : <MoreHorizontal />}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-48">
            <DropdownMenuItem onSelect={onReplace}>
              <RefreshCw /> {tr('tg.replaceToken')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              onSelect={async () => {
                const ok = await confirm({
                  title: tr('tg.deleteToken.title'),
                  description: tr('tg.deleteToken.text'),
                })
                if (ok) del.mutate()
              }}
            >
              <Trash2 /> {tr('tg.deleteToken')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {t.problem && (
        <Alert variant="destructive">
          <AlertTitle>{tr('tg.tokenProblem')}</AlertTitle>
          <AlertDescription>{t.problem}</AlertDescription>
        </Alert>
      )}
      {del.isError && (
        <Alert variant="destructive">
          <AlertTitle>{tr('common.deleteFailed')}</AlertTitle>
          <AlertDescription>{del.error.message}</AlertDescription>
        </Alert>
      )}
      {confirmDialog}
    </div>
  )
}

function PollingDot({ status }: { status: TelegramStatus }) {
  useT()
  const s = pollingState(status.polling)
  return <StatusDot tone={s.tone}>{s.label}</StatusDot>
}

const addChatSchema = z.object({
  chatID: z.string().trim().regex(/^-?\d{1,20}$/, { error: () => t('tg.chatIdFormat') }),
})

function AddChatForm() {
  const add = useAddTelegramChat()
  const { t: tr } = useT()
  const { register, handleSubmit, formState, reset } = useForm<z.infer<typeof addChatSchema>>({
    resolver: zodResolver(addChatSchema),
    defaultValues: { chatID: '' },
  })
  return (
    <form className="space-y-1.5" onSubmit={handleSubmit((v) => add.mutate(v.chatID, { onSuccess: () => reset({ chatID: '' }) }))}>
      <Label htmlFor="chat-id" className="text-xs">
        {tr('tg.chatId')}
      </Label>
      <div className="flex gap-2">
        <Input id="chat-id" className="min-w-0 flex-1 font-mono" placeholder="-1001234567890" aria-invalid={!!formState.errors.chatID} {...register('chatID')} />
        <Button type="submit" variant="outline" disabled={add.isPending}>
          {add.isPending && <Loader2 className="animate-spin" />} {tr('tg.checkAdd')}
        </Button>
      </div>
      {formState.errors.chatID && <p className="text-xs text-destructive">{formState.errors.chatID.message}</p>}
      {add.isError && <p className="text-xs text-destructive">{add.error.message}</p>}
    </form>
  )
}

/** The manual way, tucked away: check a chat by its ID. */
function ManualAdd() {
  const [open, setOpen] = useState(false)
  const { t: tr } = useT()
  return open ? (
    <AddChatForm />
  ) : (
    <Button variant="link" size="sm" className="h-auto self-start px-0 text-muted-foreground" onClick={() => setOpen(true)}>
      <Hash /> {tr('tg.manualAdd')}
    </Button>
  )
}

function AddHowTo({ status }: { status: TelegramStatus }) {
  const bot = status.token.bot?.username
  const { rich } = useT()
  return (
    <p className="text-sm text-muted-foreground">
      {rich('tg.howTo', { bot: bot ? <span className="font-medium text-foreground">@{bot}</span> : t('tg.theBot') })}
    </p>
  )
}

function ChatCard({
  chat: c,
  test,
  onTest,
  onDelete,
  deleting,
}: {
  chat: TelegramChat
  test?: TelegramTestResult | 'pending'
  onTest: () => void
  onDelete: () => void
  deleting: boolean
}) {
  const { t: tr } = useT()
  const st = chatStatus(c)
  const title = c.title || tr('tg.untitled')
  return (
    <article className={cn('flex flex-col gap-3 rounded-xl border bg-card p-4', !c.available && c.verified && 'bg-muted/30')} aria-label={title}>
      <div className="flex items-start gap-3">
        <IconTile icon={chatTypeIcon(c.type)} tint={c.available || !c.verified ? 'telegram' : 'neutral'} />
        <div className="min-w-0 flex-1">
          <div className={cn('truncate text-sm font-medium', !c.title && 'text-muted-foreground')}>{title}</div>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5">
            {c.type && <span className="text-xs text-muted-foreground">{chatTypeLabel(c.type)}</span>}
            <StatusDot tone={st.tone}>{st.label}</StatusDot>
          </div>
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" className="-mt-1 -mr-1 text-muted-foreground" aria-label={tr('tg.chatActions', { title })} disabled={deleting}>
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-56">
            <DropdownMenuLabel className="font-normal text-muted-foreground">
              {tr('tg.chatId')} <code className="text-foreground">{c.id}</code>
            </DropdownMenuLabel>
            <DropdownMenuItem onSelect={() => void navigator.clipboard?.writeText(String(c.id))}>
              <Copy /> {tr('tg.copyId')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={onDelete}>
              <Trash2 /> {tr('tg.removeChat')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {(c.problem || c.migratedFrom) && (
        <div className="space-y-0.5 text-xs text-muted-foreground">
          {c.problem && <p>{c.problem}</p>}
          {c.migratedFrom ? <p>{tr('tg.migratedFrom', { id: c.migratedFrom })}</p> : null}
        </div>
      )}
      <div className="flex-1 space-y-1.5">
        <div className="text-xs font-medium text-muted-foreground">{tr('tg.usedBy')}</div>
        <UseChips uses={c.usedIn} empty={<NoUsesHint />} />
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t pt-3">
        <Button variant="outline" size="sm" disabled={test === 'pending'} onClick={onTest}>
          {test === 'pending' ? <Loader2 className="animate-spin" /> : <Send />} {tr('common.test')}
        </Button>
        <TestResultLine result={test === 'pending' ? undefined : test} />
      </div>
    </article>
  )
}

function ChatsSection({ status }: { status: TelegramStatus }) {
  const [tests, setTests] = useState<Record<number, TelegramTestResult | 'pending'>>({})
  const test = useTestTelegramChat()
  const del = useDeleteTelegramChat()
  const [confirmDialog, confirm] = useConfirm()
  const { t: tr } = useT()

  const runTest = (id: number) => {
    setTests((t) => ({ ...t, [id]: 'pending' }))
    test.mutate(id, {
      onSuccess: (res) => setTests((t) => ({ ...t, [id]: res })),
      onError: (err) => setTests((t) => ({ ...t, [id]: { ok: false, error: err.message } })),
    })
  }
  const remove = async (c: TelegramChat) => {
    const ok = await confirm({
      title: tr('tg.removeChat.title', { name: c.title || String(c.id) }),
      description: tr('tg.removeChat.text'),
      confirmLabel: tr('service.remove.confirm'),
    })
    if (ok) del.mutate(c.id)
  }

  return (
    <section className="space-y-3" aria-label={tr('tg.chats')}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">{tr('tg.chats')}</h3>
        {status.chats.length > 0 && <PollingDot status={status} />}
      </div>
      {status.polling.lastError && (
        <Alert variant="destructive">
          <AlertTitle>{status.polling.conflict ? tr('tg.conflict') : tr('tg.pollFailed')}</AlertTitle>
          <AlertDescription>
            {status.polling.lastError}
            {status.polling.conflict && ` ${tr('tg.conflictHint')}`}
          </AlertDescription>
        </Alert>
      )}

      {status.chats.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-6 py-8 text-center">
          <IconTile icon={Send} tint="telegram" size="lg" round />
          <div className="space-y-1">
            <div className="text-sm font-medium">{tr('tg.addBot')}</div>
            <div className="mx-auto max-w-md">
              <AddHowTo status={status} />
            </div>
          </div>
          <PollingDot status={status} />
          <div className="flex w-full max-w-sm flex-col items-center">
            <ManualAdd />
          </div>
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {status.chats.map((c) => (
            <ChatCard key={c.id} chat={c} test={tests[c.id]} onTest={() => runTest(c.id)} onDelete={() => void remove(c)} deleting={del.isPending} />
          ))}
          <AddTile title={tr('tg.addChat')}>
            <AddHowTo status={status} />
            <div className="mt-auto flex flex-col gap-2">
              <PollingDot status={status} />
              <ManualAdd />
            </div>
          </AddTile>
        </div>
      )}
      {del.isError && <p className="text-xs text-destructive">{del.error.message}</p>}
      <p className="text-xs text-muted-foreground">{tr('tg.perSystem')}</p>
      {confirmDialog}
    </section>
  )
}

function TelegramCard({ status }: { status: TelegramStatus }) {
  const [replacing, setReplacing] = useState(false)
  const { t: tr, tn } = useT()
  const t = status.token
  const connected = t.configured
  return (
    <ChannelCard
      icon={Send}
      tint="telegram"
      title="Telegram"
      testId="channel-telegram"
      status={
        connected ? (
          <StatusDot tone={t.problem ? 'bad' : 'ok'}>{t.problem ? tr('tg.status.problem') : `${tr('tg.status.ok')} · ${tn('count.chats', status.chats.length)}`}</StatusDot>
        ) : (
          <StatusDot tone="muted">{tr('tg.status.none')}</StatusDot>
        )
      }
    >
      {!connected || replacing ? (
        <BotSetup t={t} replacing={replacing} onDone={() => setReplacing(false)} onCancel={replacing ? () => setReplacing(false) : undefined} />
      ) : (
        <>
          <BotSummary t={t} onReplace={() => setReplacing(true)} />
          <ChatsSection status={status} />
        </>
      )}
    </ChannelCard>
  )
}

export default function TelegramSetup() {
  const tg = useTelegram()
  const { t: tr } = useT()
  if (tg.isLoading) return <Skeleton className="h-48 rounded-xl" />
  if (tg.isError)
    return (
      <Alert variant="destructive">
        <AlertTitle>{tr('tg.loadError')}</AlertTitle>
        <AlertDescription>{tg.error.message}</AlertDescription>
      </Alert>
    )
  const data = tg.data
  if (!data) return null
  if (!data.available)
    return (
      <ChannelCard icon={Send} tint="telegram" title="Telegram" testId="channel-telegram" status={<StatusDot tone="muted">{tr('channel.unavailable')}</StatusDot>}>
        <div className="space-y-1">
          <p className="text-sm font-medium">{tr('tg.unavailable')}</p>
          <p className="text-sm text-muted-foreground">{data.reason}</p>
        </div>
      </ChannelCard>
    )
  return <TelegramCard status={data.status} />
}
