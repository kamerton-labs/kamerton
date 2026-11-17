// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { describe, expect, it } from 'vitest'
import { isEmail, presetFor, SMARTHOST_RE, splitAddresses } from './email-form'

describe('email form helpers', () => {
  it('splits pasted addresses and drops duplicates', () => {
    expect(splitAddresses('a@example.com, B@example.com; c@example.com\nb@example.com', ['A@example.com'])).toEqual({
      added: ['B@example.com', 'c@example.com'],
      invalid: [],
    })
    expect(splitAddresses('oops a@example.com')).toEqual({ added: ['a@example.com'], invalid: ['oops'] })
  })
  it('checks addresses and servers', () => {
    expect(isEmail('oncall@example.com')).toBe(true)
    expect(isEmail('oncall@example')).toBe(false)
    expect(SMARTHOST_RE.test('smtp.yandex.ru:465')).toBe(true)
    expect(SMARTHOST_RE.test('smtp.yandex.ru')).toBe(false)
    expect(presetFor(' smtp.gmail.com:587 ')?.name).toBe('Gmail')
  })
})
