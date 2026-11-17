// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Megaphone, User, Users, type LucideIcon } from 'lucide-react'
import { t } from '@/i18n'
import type { TelegramChat, TelegramStatus } from '@/lib/api'
import type { DotTone } from './parts'

const CHAT_TYPES = ['private', 'group', 'supergroup', 'channel'] as const

/** Kind of a Telegram chat in words; an unknown kind is shown as it is. */
export const chatTypeLabel = (type: string): string =>
  (CHAT_TYPES as readonly string[]).includes(type) ? t(`chat.type.${type as (typeof CHAT_TYPES)[number]}` as const) : type

export function chatTypeIcon(type: string): LucideIcon {
  if (type === 'private') return User
  if (type === 'channel') return Megaphone
  return Users
}

export function pollingState(p: TelegramStatus['polling']): { tone: DotTone; label: string } {
  if (p.conflict) return { tone: 'bad', label: t('chat.polling.conflict') }
  if (p.lastError) return { tone: 'bad', label: t('chat.polling.error') }
  if (p.active) return { tone: 'busy', label: t('chat.polling.active') }
  return { tone: 'muted', label: t('chat.polling.idle') }
}

export function chatStatus(c: TelegramChat): { tone: DotTone; label: string } {
  if (!c.verified) return { tone: 'muted', label: t('chat.status.fromDescription') }
  if (c.available) return { tone: 'ok', label: t('chat.status.ok') }
  return { tone: 'bad', label: t('chat.status.unavailable') }
}
