// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from '@/i18n'
import { httpUrl, shortVersion, sinceText, systemStateText } from './format'

describe('httpUrl', () => {
  it('keeps http and https addresses', () => {
    expect(httpUrl('https://wiki.example.com/runbooks/checkout')).toBe('https://wiki.example.com/runbooks/checkout')
    expect(httpUrl(' http://example.com ')).toBe('http://example.com/')
  })

  it('refuses everything else', () => {
    for (const raw of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,<b>x</b>', 'vbscript:x', '/relative/path', 'wiki/page', '', undefined]) {
      expect(httpUrl(raw)).toBeNull()
    }
  })
})

afterEach(() => setLang('en', false))

describe('sinceText', () => {
  it('reads naturally for a fresh alert and an older one', () => {
    expect(sinceText(new Date().toISOString())).toBe('started just now')
    expect(sinceText(new Date(Date.now() - 5 * 60_000).toISOString())).toBe('started 5 minutes ago')
    expect(sinceText(new Date(Date.now() - 60 * 60_000).toISOString())).toBe('started 1 hour ago')
  })

  it('reads naturally in Russian', () => {
    setLang('ru', false)
    expect(sinceText(new Date().toISOString())).toBe('началось только что')
    expect(sinceText(new Date(Date.now() - 5 * 60_000).toISOString())).toBe('началось 5 минут назад')
    expect(sinceText(new Date(Date.now() - 3 * 60 * 60_000).toISOString())).toBe('началось 3 часа назад')
  })
})

describe('systemStateText', () => {
  it('never prints a missing count', () => {
    expect(systemStateText({ state: 'firing', alerts: null })).toBe('firing: 0')
    expect(systemStateText({ state: 'firing', alerts: { critical: 2, warning: 1 } })).toBe('firing: 2, warnings: 1')
    expect(systemStateText({ state: 'unknown', alerts: null })).toBe('no alert data')
  })
})

describe('shortVersion', () => {
  it('fits a version on one line', () => {
    expect(shortVersion('0.1.0')).toBe('v0.1.0')
    expect(shortVersion('v1.2.3')).toBe('v1.2.3')
    expect(shortVersion('b238306-dirty')).toBe('b238306')
    expect(shortVersion('v0.1.0-3-g9795a51c')).toBe('9795a51')
    expect(shortVersion('sha-9795a51')).toBe('9795a51')
    expect(shortVersion('main')).toBe('main')
  })
})
