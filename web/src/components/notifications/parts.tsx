// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Small pieces shared by the channel cards (Telegram, email) and the
// "where to send" panel of the canvas.

import { useState, type ComponentProps, type ReactNode } from 'react'
import { Link } from 'react-router'
import { cn } from 'cn'
import { Check, ChevronDown, Copy, Eye, EyeOff, Plus, type LucideIcon } from 'lucide-react'
import { useT } from '@/i18n'
import { Input } from '@/components/ui/input'
import type { ChannelUse } from '@/lib/api'
import { channelUseLabel, type ChipTone } from '@/lib/model/routing'

export type Tint = 'telegram' | 'email' | 'neutral' | 'ok' | 'warn'

const tintClass: Record<Tint, string> = {
  telegram: 'bg-sky-500/10 text-sky-600 dark:bg-sky-400/15 dark:text-sky-300',
  email: 'bg-violet-500/10 text-violet-600 dark:bg-violet-400/15 dark:text-violet-300',
  neutral: 'bg-muted text-muted-foreground',
  ok: 'bg-ok/10 text-ok',
  warn: 'bg-warn/15 text-warn',
}

/** An icon in a tinted rounded square. */
export function IconTile({ icon: Icon, tint = 'neutral', size = 'md', round, className }: { icon: LucideIcon; tint?: Tint; size?: 'xs' | 'sm' | 'md' | 'lg'; round?: boolean; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        'inline-flex shrink-0 items-center justify-center',
        round ? 'rounded-full' : 'rounded-lg',
        size === 'xs' ? 'size-5 rounded-md [&>svg]:size-3' : size === 'sm' ? 'size-7 [&>svg]:size-3.5' : size === 'lg' ? 'size-11 [&>svg]:size-5' : 'size-9 [&>svg]:size-4',
        tintClass[tint],
        className,
      )}
    >
      <Icon />
    </span>
  )
}

export type DotTone = 'ok' | 'warn' | 'bad' | 'muted' | 'busy'

const dotTone: Record<DotTone, string> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-destructive',
  muted: 'bg-muted-foreground/40',
  busy: 'bg-ok animate-pulse',
}

/** A small coloured dot with a few words. */
export function StatusDot({ tone, children, className }: { tone: DotTone; children: ReactNode; className?: string }) {
  return (
    <span className={cn('inline-flex items-center gap-1.5 text-xs text-muted-foreground', className)}>
      <span aria-hidden className={cn('inline-block size-1.5 shrink-0 rounded-full', dotTone[tone])} />
      {children}
    </span>
  )
}

const chipTone: Record<ChipTone, string> = {
  critical: 'border-firing/30 bg-firing/10 text-firing',
  warning: 'border-warn/40 bg-warn/10 text-foreground',
  neutral: 'border-border bg-muted/60 text-foreground',
  muted: 'border-warn/40 bg-warn/10 text-foreground',
}

/** A condition chip: "Critical", "All services". */
export function Chip({ tone = 'neutral', children, className }: { tone?: ChipTone; children: ReactNode; className?: string }) {
  return <span className={cn('inline-flex h-5 max-w-full items-center truncate rounded-md border px-1.5 text-[11px] leading-none font-medium', chipTone[tone], className)}>{children}</span>
}

/** Password field with a show/hide toggle. Hidden by default. */
export function PasswordInput({ className, wrapperClassName, ...props }: Omit<ComponentProps<'input'>, 'type'> & { wrapperClassName?: string }) {
  const [shown, setShown] = useState(false)
  const { t } = useT()
  return (
    <div className={cn('relative', wrapperClassName)}>
      <Input {...props} type={shown ? 'text' : 'password'} className={cn('pr-9', className)} />
      <button
        type="button"
        onClick={() => setShown((s) => !s)}
        aria-label={shown ? t('parts.hidePassword') : t('parts.showPassword')}
        title={shown ? t('parts.hidePassword') : t('parts.showPassword')}
        aria-pressed={shown}
        className="absolute inset-y-0 right-0 flex w-9 items-center justify-center rounded-r-lg text-muted-foreground outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50"
      >
        {shown ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  )
}

/** "Where … is stored", on demand: the Kubernetes Secret that holds a channel's secret. */
export function SecretLocation({ what, secret }: { what: string; secret: { namespace: string; secretName: string; secretKey: string } }) {
  const { t, rich } = useT()
  return (
    <details className="group text-xs text-muted-foreground">
      <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
        <ChevronDown className="size-3.5 transition-transform group-open:rotate-180" />
        {t('parts.whereStored', { what })}
      </summary>
      <p className="mt-1.5 pl-4.5">
        {rich('parts.secretText', {
          secret: (
            <code className="rounded bg-muted px-1">
              {secret.namespace}/{secret.secretName}
            </code>
          ),
          key: <code className="rounded bg-muted px-1">{secret.secretKey}</code>,
        })}
      </p>
    </details>
  )
}

/** Copies a value; shows a check for a moment. */
export function CopyButton({ value, label, className, children }: { value: string; label: string; className?: string; children?: ReactNode }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={() => {
        void navigator.clipboard?.writeText(value).then(() => {
          setDone(true)
          setTimeout(() => setDone(false), 1500)
        })
      }}
      className={cn('inline-flex items-center gap-1 rounded-md text-muted-foreground outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50', className)}
    >
      {children}
      {done ? <Check className="size-3.5 text-ok" /> : <Copy className="size-3.5" />}
    </button>
  )
}

/** Systems that write to a channel, as chips linking to the system. */
export function UseChips({ uses, empty }: { uses: ChannelUse[]; empty?: ReactNode }) {
  useT()
  if (uses.length === 0) return <>{empty}</>
  return (
    <ul className="flex flex-wrap gap-1.5">
      {uses.map((u) => (
        <li key={u.system} className="max-w-full">
          <Link
            to={`/systems/${encodeURIComponent(u.system)}`}
            className="inline-flex max-w-full items-center gap-1.5 truncate rounded-full border bg-background px-2 py-0.5 text-xs outline-none hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/50"
          >
            <span className="truncate font-medium">{u.system}</span>
            <span className="truncate text-muted-foreground">{channelUseLabel(u)}</span>
          </Link>
        </li>
      ))}
    </ul>
  )
}

/** Hint when no system writes to a channel yet. */
export function NoUsesHint() {
  const { t } = useT()
  return (
    <p className="text-xs text-muted-foreground">
      {t('parts.noUses')}{' '}
      <Link to="/" className="font-medium text-foreground underline-offset-2 hover:underline">
        {t('parts.pickSystem')}
      </Link>
    </p>
  )
}

/** Inline result of a test message. */
export function TestResultLine({ result }: { result?: { ok: boolean; error?: string } }) {
  const { t } = useT()
  if (!result) return null
  return (
    <p role="status" className={cn('text-xs', result.ok ? 'text-ok' : 'text-destructive')}>
      {result.ok ? t('parts.testSent') : t('parts.testFailed', { error: result.error ?? '' })}
    </p>
  )
}

/** The dashed "add" tile at the end of a grid. */
export function AddTile({ title, children, className }: { title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex min-h-40 flex-col gap-3 rounded-xl border border-dashed p-4', className)}>
      <div className="flex items-center gap-2.5">
        <IconTile icon={Plus} size="sm" />
        <div className="text-sm font-medium">{title}</div>
      </div>
      {children}
    </div>
  )
}

/** Numbered step of a guided setup. */
export function Step({ n, children, done }: { n: number; children: ReactNode; done?: boolean }) {
  return (
    <li className="flex gap-3">
      <span
        aria-hidden
        className={cn(
          'mt-0.5 inline-flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold',
          done ? 'bg-ok/15 text-ok' : 'bg-primary text-primary-foreground',
        )}
      >
        {done ? <Check className="size-3.5" /> : n}
      </span>
      <div className="min-w-0 flex-1 space-y-2 pt-0.5 text-sm">{children}</div>
    </li>
  )
}

/** One channel on the Channels page: icon, name, status, then its content. */
export function ChannelCard({
  icon,
  tint,
  title,
  status,
  badge,
  children,
  testId,
}: {
  icon: LucideIcon
  tint: Tint
  title: string
  status?: ReactNode
  badge?: ReactNode
  children: ReactNode
  testId?: string
}) {
  return (
    <section aria-label={title} data-testid={testId} className="overflow-hidden rounded-xl bg-card text-card-foreground ring-1 ring-foreground/10">
      <header className="flex items-center gap-3 px-5 py-4">
        <IconTile icon={icon} tint={tint} />
        <div className="min-w-0 flex-1">
          <h2 className="flex flex-wrap items-center gap-2 text-base leading-tight font-semibold">
            {title}
            {badge}
          </h2>
          {status && <div className="mt-0.5">{status}</div>}
        </div>
      </header>
      <div className="space-y-6 border-t px-5 py-5">{children}</div>
    </section>
  )
}
