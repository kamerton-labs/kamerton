// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Shared state of the system editor (canvas, rules, YAML): one draft,
// read and changed through this context so every view shows the same thing.

import { createContext, use, useEffect, useState } from 'react'
import type { EmailStatus, TelegramStatus } from '@/lib/api'
import type { Draft } from '@/lib/model/draft'
import type { Destination } from '@/lib/model/routing'
import type { AlertStateSample } from '@/lib/model/rules'
import type { Description } from '@/lib/model/types'

export interface EditorState {
  systemId: string
  draft: Draft
  /** Last valid description (what the canvas shows). */
  desc: Description
  /** Changes are not possible (YAML does not parse, or nowhere to save). */
  readOnly: boolean
  edit: (fn: (d: Description) => Description) => void
  /** Live alert states; undefined when Prometheus is not reachable. */
  states?: AlertStateSample[]
  destinations: Destination[]
  telegram?: TelegramStatus
  /** Email setup (mail server and recipients); undefined when unavailable. */
  email?: EmailStatus
  /** Server validation errors of the current draft, by JSON pointer. */
  issues: { path: string; message: string }[]
}

export const EditorContext = createContext<EditorState | null>(null)

export function useEditor(): EditorState {
  const v = use(EditorContext)
  if (!v) throw new Error('useEditor outside the system editor')
  return v
}

/** Debounced copy of a value. */
export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

/** Display name of a receiver: the Telegram chat title or the email recipient's name when known. */
export function destinationName(d: Destination, telegram?: TelegramStatus, email?: EmailStatus): string {
  if (d.kind === 'telegram') {
    const chat = telegram?.chats.find((c) => c.id === d.chatID)
    return chat?.title || `Telegram ${d.chatID}`
  }
  const rec = email?.recipients.find((r) => r.receiver === d.receiver)
  return rec?.name || d.emails?.join(', ') || d.receiver
}
