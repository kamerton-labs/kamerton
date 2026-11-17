// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// No browser: static markup is enough to check structure, texts and safety attributes.

import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLang } from '@/i18n'
import type { EmailResponse, TelegramChat, TelegramResponse, TelegramStatus } from '@/lib/api'
import { chatStatus, pollingState } from './chat-type'

const state: { telegram?: TelegramResponse; email?: EmailResponse } = {}
const mutation = () => ({ mutate: vi.fn(), isPending: false, isError: false, error: null })

vi.mock('@/lib/api', () => ({
  useTelegram: () => ({ isLoading: false, isError: false, data: state.telegram }),
  useEmail: () => ({ isLoading: false, isError: false, data: state.email }),
  useSetTelegramToken: mutation,
  useDeleteTelegramToken: mutation,
  useAddTelegramChat: mutation,
  useDeleteTelegramChat: mutation,
  useTestTelegramChat: mutation,
  useSetEmailServer: mutation,
  useDeleteEmailServer: mutation,
  useAddEmailRecipient: mutation,
  useUpdateEmailRecipient: mutation,
  useDeleteEmailRecipient: mutation,
  useTestEmailRecipient: mutation,
}))

const { default: TelegramSetup } = await import('./TelegramSetup')
const { default: EmailSetup } = await import('./EmailSetup')

const render = (el: React.ReactElement) => renderToStaticMarkup(<MemoryRouter>{el}</MemoryRouter>)

function tgStatus(over: Partial<TelegramStatus> = {}): TelegramStatus {
  return {
    token: { configured: true, bot: { id: 1, username: 'shop_alerts_bot', firstName: 'Shop alerts', checkedAt: '' }, secretName: 'kamerton-telegram', secretKey: 'bot-token', namespace: 'kamerton' },
    polling: { active: true },
    chats: [],
    description: { loaded: true, mode: 'cluster', services: [], writable: true },
    ...over,
  }
}

function chat(over: Partial<TelegramChat>): TelegramChat {
  return {
    id: -100,
    title: 'Ops',
    type: 'supergroup',
    status: 'member',
    available: true,
    updatedAt: '',
    source: 'update',
    verified: true,
    routing: { severities: [], services: [] },
    usedIn: [],
    ...over,
  }
}

beforeEach(() => {
  state.telegram = undefined
  state.email = undefined
})
afterEach(() => setLang('en', false))

describe('Telegram card', () => {
  it('guides through BotFather when no bot is connected', () => {
    state.telegram = { available: true, status: tgStatus({ token: { configured: false, secretName: 'kamerton-telegram', secretKey: 'bot-token', namespace: 'kamerton' } }) }
    const html = render(<TelegramSetup />)
    expect(html).toContain('href="https://t.me/BotFather"')
    expect(html).toContain('target="_blank"')
    expect(html).toContain('Open @BotFather')
    expect(html).toContain('/newbot')
    expect(html).toMatch(/id="bot-token"[^>]*type="password"|type="password"[^>]*id="bot-token"/)
    expect(html).toContain('aria-label="Show password"')
    expect(html).toContain('Where the token is stored')
    expect(html).toContain('Check and save')
    expect(html).not.toContain('Chats')
  })

  it('guides through BotFather in Russian after the language is switched', () => {
    setLang('ru', false)
    state.telegram = { available: true, status: tgStatus({ token: { configured: false, secretName: 'kamerton-telegram', secretKey: 'bot-token', namespace: 'kamerton' } }) }
    const html = render(<TelegramSetup />)
    expect(html).toContain('Открыть @BotFather')
    expect(html).toContain('aria-label="Показать пароль"')
    expect(html).toContain('Где хранится токен')
    expect(html).toContain('Проверить и сохранить')
  })

  it('shows a compact connected bot, chat cards and the add tile', () => {
    state.telegram = {
      available: true,
      status: tgStatus({
        chats: [
          chat({ usedIn: [{ system: 'shop', severities: ['critical'], services: ['a', 'b', 'c'] }] }),
          chat({ id: -200, title: 'Gone', available: false, problem: 'the bot is not a member of this chat' }),
          chat({ id: -300, title: '', type: '', verified: false, source: 'description' }),
        ],
      }),
    }
    const html = render(<TelegramSetup />)
    expect(html).toContain('Shop alerts')
    expect(html).toContain('@shop_alerts_bot')
    expect(html).toContain('bot connected · 3 chats')
    expect(html).toContain('aria-label="Bot actions"')
    expect(html).not.toContain('type="password"')
    expect(html).toContain('href="/systems/shop"')
    expect(html).toContain('critical · 3 services')
    expect(html).toContain('No system sends here yet.')
    expect(html).toContain('bot is in the chat')
    expect(html).toContain('unavailable')
    expect(html).toContain('from the description')
    expect(html).toContain('Untitled')
    expect(html).toContain('Add chat')
    expect(html).toContain('Chat did not show up? Add it by chat ID')
    expect(html.split('aria-label="Actions for chat').length - 1).toBe(3)
    expect(html.split(' Test</button>').length - 1).toBe(3)
  })

  it('keeps token problems and polling errors visible', () => {
    state.telegram = {
      available: true,
      status: tgStatus({
        token: { configured: true, problem: 'Telegram rejected the token', secretName: 'kamerton-telegram', secretKey: 'bot-token', namespace: 'kamerton' },
        polling: { active: false, lastError: 'Conflict: terminated by other getUpdates request', conflict: true },
      }),
    }
    const html = render(<TelegramSetup />)
    expect(html).toContain('Token problem')
    expect(html).toContain('Telegram rejected the token')
    expect(html).toContain('Someone else is taking the bot updates')
    expect(html).toContain('Add the bot to a chat')
  })

  it('says when setup is unavailable', () => {
    state.telegram = { available: false, reason: 'no cluster' }
    const html = render(<TelegramSetup />)
    expect(html).toContain('Telegram setup is unavailable')
    expect(html).toContain('no cluster')
  })

  it('labels chat and polling states', () => {
    expect(chatStatus(chat({}))).toEqual({ tone: 'ok', label: 'bot is in the chat' })
    expect(chatStatus(chat({ available: false }))).toEqual({ tone: 'bad', label: 'unavailable' })
    expect(chatStatus(chat({ verified: false }))).toEqual({ tone: 'muted', label: 'from the description' })
    expect(pollingState({ active: true }).tone).toBe('busy')
    expect(pollingState({ active: true, lastError: 'x', conflict: true }).label).toBe('someone else is polling this bot')
  })
})

describe('Email card', () => {
  const server = { secretName: 'kamerton-smtp', secretKey: 'password', namespace: 'kamerton', hasPassword: false, configured: false }

  it('offers a short server form with a hidden password', () => {
    state.email = { available: true, status: { server, recipients: [] } }
    const html = render(<EmailSetup />)
    expect(html).toContain('Mail server')
    expect(html).toContain('Yandex')
    expect(html).toMatch(/id="smtp-password"[^>]*type="password"|type="password"[^>]*id="smtp-password"/)
    expect(html).toContain('Check and connect')
    expect(html).toContain('Where the password is stored')
  })

  it('shows the connected server and recipients as cards', () => {
    state.email = {
      available: true,
      status: {
        server: { ...server, configured: true, hasPassword: true, smarthost: 'smtp.yandex.ru:465', from: 'alerts@example.com', username: 'alerts@example.com' },
        recipients: [
          { id: 'ab12', name: 'On-call', to: ['a@example.com', 'b@example.com'], createdAt: '', receiver: 'email-ab12', usedIn: [{ system: 'shop', severities: ['critical', 'warning'], services: [] }] },
        ],
      },
    }
    const html = render(<EmailSetup />)
    expect(html).toContain('email connected · 1 recipient')
    expect(html).toContain('smtp.yandex.ru:465')
    expect(html).toContain('aria-label="Mail server actions"')
    expect(html).not.toContain('type="password"')
    expect(html).toContain('On-call')
    expect(html).toContain('2 addresses')
    expect(html).toContain('b@example.com')
    expect(html).toContain('all alerts')
    expect(html).toContain('Test email')
    expect(html).toContain('Add recipient')
  })

  it('says when setup is unavailable', () => {
    state.email = { available: false, reason: 'no cluster' }
    expect(render(<EmailSetup />)).toContain('Email setup is unavailable')
  })
})
