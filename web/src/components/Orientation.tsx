// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// "What is set up": the real state of each step a person needs, with a link
// to fix it. Collapses to one line once everything is in place.

import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { CheckCircle2, ChevronDown, CircleAlert, CircleDashed } from 'lucide-react'
import { cn } from 'cn'
import { useDiscovery, useStacks, useSystems, useTelegram } from '@/lib/api'
import { useT } from '@/i18n'
import { coreHealth } from '@/lib/stacks'

type StepState = 'done' | 'todo' | 'problem' | 'loading'

interface Step {
  title: string
  state: StepState
  text: string
  link?: { to: string; label: string }
  onClick?: () => void
}

const icon: Record<StepState, ReactNode> = {
  done: <CheckCircle2 className="size-5 text-ok" />,
  todo: <CircleDashed className="size-5 text-muted-foreground" />,
  problem: <CircleAlert className="size-5 text-warn" />,
  loading: <CircleDashed className="size-5 animate-spin text-muted-foreground" />,
}

export default function Orientation({ onAddSystem }: { onAddSystem: () => void }) {
  const stacks = useStacks()
  const disc = useDiscovery()
  const systems = useSystems()
  const tg = useTelegram(undefined, 30_000)
  const [expanded, setExpanded] = useState(false)
  const { t, tn } = useT()

  const steps: Step[] = []

  if (stacks.isLoading) steps.push({ title: t('orient.stack'), state: 'loading', text: t('orient.checking') })
  else {
    const health = coreHealth(stacks.data?.sets)
    steps.push({
      title: t('orient.stack'),
      state: health.state === 'ok' ? 'done' : 'problem',
      text: health.state === 'ok' ? t('orient.stackOk') : (health.problem ?? t('status.checkError')),
      link: health.state === 'ok' ? undefined : { to: '/status', label: t('orient.details') },
    })
  }

  if (disc.isLoading) steps.push({ title: t('orient.services'), state: 'loading', text: t('orient.searching') })
  else if (!disc.data?.configured || disc.isError) steps.push({ title: t('orient.services'), state: 'todo', text: t('orient.servicesNoProm') })
  else {
    const n = disc.data.services.length
    steps.push({
      title: t('orient.services'),
      state: n > 0 ? 'done' : 'problem',
      text: n > 0 ? tn('orient.servicesSending', n) : t('orient.servicesNone'),
    })
  }

  const list = systems.data?.systems ?? []
  const rules = list.reduce((n, s) => n + s.rules, 0)
  if (systems.isLoading) steps.push({ title: t('orient.rules'), state: 'loading', text: '…' })
  else
    steps.push({
      title: t('orient.rules'),
      state: list.length === 0 ? 'todo' : list.some((s) => !s.valid) ? 'problem' : 'done',
      text:
        list.length === 0
          ? t('orient.rulesNone')
          : `${tn('count.systems', list.length)}, ${tn('count.rules', rules)}${list.some((s) => !s.valid) ? t('orient.rulesInvalid') : ''}`,
      onClick: list.length === 0 ? onAddSystem : undefined,
    })

  const receivers = list.reduce((n, s) => n + s.receivers, 0)
  const tgStatus = tg.data?.available ? tg.data.status : undefined
  const routedChats = tgStatus?.chats.filter((c) => c.usedIn.length > 0).length ?? 0
  steps.push({
    title: t('orient.channel'),
    state: receivers > 0 || routedChats > 0 ? 'done' : 'todo',
    text:
      routedChats > 0
        ? tn('orient.chats', routedChats)
        : receivers > 0
          ? tn('orient.receivers', receivers)
          : t('orient.channelNone'),
    link: { to: '/channels', label: receivers > 0 || routedChats > 0 ? t('orient.open') : t('orient.connectTelegram') },
  })

  const allDone = steps.every((s) => s.state === 'done')
  if (allDone && !expanded)
    return (
      <button type="button" aria-expanded={false} onClick={() => setExpanded(true)} className="flex w-full items-center gap-2 rounded-lg border bg-card px-3 py-2 text-left text-sm">
        <CheckCircle2 className="size-4 text-ok" />
        <span>{t('orient.allDone')}</span>
        <ChevronDown className="ml-auto size-4 text-muted-foreground" />
      </button>
    )

  return (
    <section className="rounded-lg border bg-card">
      <div className="flex items-center justify-between px-4 pt-3">
        <h2 className="text-sm font-medium">{allDone ? t('orient.titleDone') : t('orient.title')}</h2>
        {allDone && (
          <button type="button" onClick={() => setExpanded(false)} className="text-xs text-muted-foreground hover:text-foreground">
            {t('orient.collapse')}
          </button>
        )}
      </div>
      <ol className="grid gap-px p-2 sm:grid-cols-2 xl:grid-cols-4">
        {steps.map((s, i) => (
          <li key={s.title} className={cn('flex gap-3 rounded-md p-2', s.state !== 'done' && s.state !== 'loading' && 'bg-muted/40')}>
            <div className="pt-0.5">{icon[s.state]}</div>
            <div className="min-w-0 space-y-0.5">
              <div className="text-sm font-medium">
                <span className="text-muted-foreground">{i + 1}.</span> {s.title}
              </div>
              <p className="text-xs text-muted-foreground">{s.text}</p>
              {s.link && (
                <Link to={s.link.to} className="text-xs font-medium underline-offset-2 hover:underline">
                  {s.link.label} →
                </Link>
              )}
              {s.onClick && (
                <button type="button" onClick={s.onClick} className="text-xs font-medium underline-offset-2 hover:underline">
                  {t('systems.add')} →
                </button>
              )}
            </div>
          </li>
        ))}
      </ol>
    </section>
  )
}
