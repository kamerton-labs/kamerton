// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLang } from '@/i18n'
import type { HistoryIncident, HistoryResponse } from '@/lib/api'
import { NOTE_CAUSE_MAX, NOTE_FIX_MAX, noteCounter, notePayload } from '@/lib/incidents'

type Query = { isLoading: boolean; isError: boolean; data?: HistoryResponse | null; error?: Error }
const state: { history: Query; save: { isPending: boolean; isError: boolean; isSuccess: boolean; error: Error | null }; asked: string[][] } = {
  history: { isLoading: false, isError: false },
  save: { isPending: false, isError: false, isSuccess: false, error: null },
  asked: [],
}

vi.mock('@/lib/api', () => ({
  useAlerts: () => ({ isLoading: false, isError: false, data: { configured: true, alerts: [], incidents: [] } }),
  useNoiseFeedback: () => ({ mutate: () => {}, isPending: false, isError: false, error: null }),
  useSystems: () => ({ data: { systems: [{ id: 'shop', name: 'shop' }, { id: 'bank', name: 'bank' }] } }),
  useIncidentHistory: (system: string, service: string) => {
    state.asked.push([system, service])
    return state.history
  },
  useSaveIncidentNote: () => ({ mutate: () => {}, ...state.save }),
}))

const { default: AlertsPage } = await import('./AlertsPage')
const { SavedNote } = await import('@/components/alerts/NoteEditor')

const render = (url = '/alerts?view=history') => renderToStaticMarkup(<MemoryRouter initialEntries={[url]}><AlertsPage /></MemoryRouter>)
const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()

const closed: HistoryIncident = {
  id: 'h-1',
  system: 'shop',
  roots: ['payment'],
  rootAlerts: [{ name: 'KamertonNoReadyPods', service: 'payment', title: 'No ready pods', severity: 'critical' }],
  affected: ['checkout', 'frontend'],
  startedAt: ago(3 * 24 * 60),
  endedAt: ago(3 * 24 * 60 - 5),
  durationSeconds: 312,
  open: false,
  evidence: { cause: 'payment: No ready pods', changes: ['container payment restarted (OOMKilled)'], collectedAt: ago(3 * 24 * 60) },
  note: { cause: 'Memory limit too low after the release', fix: 'Raised the memory limit to 512Mi', updatedAt: ago(2 * 24 * 60), source: 'telegram' },
}

const ongoing: HistoryIncident = { id: 'h-2', system: 'shop', roots: ['db'], rootAlerts: [{ name: 'KamertonErrorRateHigh', service: 'db', severity: 'critical' }], affected: [], startedAt: ago(4), open: true }

const between = (html: string, testid: string) => {
  const start = html.indexOf(`data-testid="${testid}"`)
  return start < 0 ? '' : html.slice(start)
}

beforeEach(() => {
  state.history = { isLoading: false, isError: false, data: { incidents: [closed, ongoing] } }
  state.save = { isPending: false, isError: false, isSuccess: false, error: null }
  state.asked = []
})
afterEach(() => setLang('en', false))

describe('incident history', () => {
  it('switches views through the URL and keeps the live view as the default', () => {
    let html = render('/alerts?system=shop&service=payment&alertname=X')
    expect(html).toMatch(/aria-current="page"[^>]*data-testid="alerts-view-live"/)
    expect(between(html, 'alerts-view-history')).toContain('href="/alerts?view=history&amp;system=shop&amp;service=payment"')
    expect(html).not.toContain('data-testid="history"')
    html = render('/alerts?view=history&system=shop&service=payment')
    expect(html).toMatch(/aria-current="page"[^>]*data-testid="alerts-view-history"/)
    expect(state.asked).toContainEqual(['shop', 'payment'])
    expect(html).toContain('Only: shop · payment')
  })

  it('lists incidents newest first: ongoing marked, closed with duration, affected count and note', () => {
    const html = render()
    const rows = html.split('data-testid="history-row"').slice(1)
    expect(rows).toHaveLength(2)
    expect(rows[0]).toContain('data-incident="h-2"')
    expect(rows[0]).toContain('data-testid="history-ongoing"')
    expect(rows[0]).toContain('ongoing')
    expect(rows[0]).not.toContain('lasted')
    expect(rows[0]).not.toContain('data-testid="history-note"')
    expect(rows[0]).not.toContain('affected service')
    expect(rows[0]).toContain('KamertonErrorRateHigh')
    expect(rows[1]).toContain('<span class="font-medium">payment</span>')
    expect(rows[1]).toContain(' · No ready pods')
    expect(rows[1]).toContain('3 days ago')
    expect(rows[1]).toContain('lasted 5 min')
    expect(rows[1]).toContain('shop · 2 affected services')
    expect(between(rows[1], 'history-note')).toContain('What helped: </span>Raised the memory limit to 512Mi')
    expect(rows[1]).not.toContain('data-testid="history-ongoing"')
    expect(html).not.toContain('undefined')
  })

  it('shows the collected evidence and both note fields in the expanded row', () => {
    const html = render('/alerts?view=history&system=shop&incident=h-1')
    expect(html).toMatch(/<li id="history-h-1"[^>]*data-incident="h-1"><details class="group" open=""/)
    expect(html).not.toMatch(/data-incident="h-2"><details class="group" open=""/)
    const row = between(html, 'history-details')
    expect(row).toContain('Cause: payment: No ready pods')
    expect(row).toContain('Also affected: </span>checkout, frontend')
    expect(row).toContain('data-testid="incident-evidence"')
    expect(row).toContain('OOMKilled')
    expect(between(row, 'note-cause')).toContain('Memory limit too low after the release</textarea>')
    expect(between(row, 'note-fix')).toContain('Raised the memory limit to 512Mi</textarea>')
    expect(between(row, 'note-cause')).toMatch(/^data-testid="note-cause"[^>]*maxLength="200"|maxLength="200"[^>]*data-testid="note-cause"/)
    expect(row).toContain('maxLength="500"')
    expect(between(row, 'note-meta')).toContain('updated 2 days ago, from Telegram')
    expect(row).not.toContain('data-testid="note-cause-counter"')
  })

  it('says "updated" without a source for notes written in the interface', () => {
    state.history.data = { incidents: [{ ...closed, note: { ...closed.note, source: 'ui' } }] }
    const meta = between(render(), 'note-meta')
    expect(meta).toContain('updated 2 days ago<')
    expect(meta).not.toContain('Telegram')
  })

  it('shows a counter per field near its own cap and the error of a failed save', () => {
    state.history.data = { incidents: [{ ...closed, note: { cause: 'c'.repeat(180), fix: 'f'.repeat(399) } }] }
    state.save = { isPending: false, isError: true, isSuccess: false, error: new Error('cause may have at most 200 characters and fix at most 500') }
    let html = render()
    expect(between(html, 'note-cause-counter')).toContain('180 / 200')
    expect(html).not.toContain('data-testid="note-fix-counter"')
    expect(between(html, 'note-error')).toContain('Failed: cause may have at most 200 characters')
    state.history.data = { incidents: [{ ...closed, note: { fix: 'f'.repeat(500) } }] }
    html = render()
    expect(html).not.toContain('data-testid="note-cause-counter"')
    expect(between(html, 'note-fix-counter')).toMatch(/^data-testid="note-fix-counter">500 \/ 500</)
    expect(html).toMatch(/text-destructive[^"]*" data-testid="note-fix-counter"/)
  })

  it('trims and caps each field as the core counts characters', () => {
    expect(notePayload('  pool exhausted \n', '\trestart ')).toEqual({ cause: 'pool exhausted', fix: 'restart' })
    expect(notePayload('x'.repeat(NOTE_CAUSE_MAX + 50), 'y'.repeat(NOTE_FIX_MAX + 1))).toEqual({ cause: 'x'.repeat(200), fix: 'y'.repeat(500) })
    expect([...notePayload('й😀'.repeat(150), '').cause]).toHaveLength(200)
    expect(notePayload('', '')).toEqual({ cause: '', fix: '' })
    expect(noteCounter(159, NOTE_CAUSE_MAX)).toBeNull()
    expect(noteCounter(160, NOTE_CAUSE_MAX)).toBe('160 / 200')
    expect(noteCounter(399, NOTE_FIX_MAX)).toBeNull()
    expect(noteCounter(400, NOTE_FIX_MAX)).toBe('400 / 500')
  })

  it('shows the saved text from the core after a save', () => {
    const html = renderToStaticMarkup(<SavedNote note={{ cause: 'Pool exhausted', fix: 'Restarted payment', updatedAt: ago(0), source: 'ui' }} onEdit={() => {}} />)
    expect(html).toContain('What helped: </span>Restarted payment')
    expect(html).toContain('Cause: </span>Pool exhausted')
    expect(html).toContain('Saved')
    expect(html).toContain('data-testid="note-edit"')
  })

  it('marks an estimated end with "about"', () => {
    state.history.data = { incidents: [{ ...closed, endEstimated: true }] }
    expect(render()).toContain('lasted about 5 min')
    setLang('ru', false)
    expect(render()).toContain('длилось около 5 мин')
  })

  it('says when a linked record is no longer in the history', () => {
    let html = render('/alerts?view=history&system=shop&incident=gone')
    expect(between(html, 'history-missing')).toContain('This incident is no longer in the history.')
    expect(html.match(/data-testid="history-row"/g)).toHaveLength(2)
    html = render('/alerts?view=history&system=shop&incident=h-1')
    expect(html).not.toContain('data-testid="history-missing"')
    expect(render()).not.toContain('data-testid="history-missing"')
  })

  it('explains an empty history', () => {
    state.history.data = { incidents: [] }
    let html = render()
    expect(between(html, 'history-empty')).toContain('No incidents recorded yet. Kamerton records an incident when alerts of a service and of the services that depend on it fire together.')
    state.history.data = {}
    html = render('/alerts?view=history&service=payment')
    expect(between(html, 'history-empty')).toContain('No incidents recorded for this filter.')
  })

  it('shows nothing for a core without the history and an error for other failures', () => {
    state.history = { isLoading: false, isError: false, data: null }
    let html = render()
    expect(html).toContain('data-testid="alerts-view"')
    expect(html).not.toContain('data-testid="history"')
    expect(html).not.toContain('Could not load')
    state.history = { isLoading: false, isError: true, error: new Error('timeout') }
    html = render()
    expect(html).toContain('Could not load the history')
    expect(html).toContain('timeout')
  })

  it('renders in Russian', () => {
    setLang('ru', false)
    const html = render()
    expect(html).toContain('>История<')
    expect(html).toContain('продолжается')
    expect(html).toContain('длилось 5 мин')
    expect(html).toContain('2 затронутых сервиса')
    expect(html).toContain('Что помогло: </span>Raised the memory limit')
    expect(html).toContain('обновлено 2 дня назад, из Telegram')
    expect(html).toContain('>Причина<')
  })
})
