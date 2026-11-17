// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { describe, expect, it } from 'vitest'
import { isLoginPath, loginErrorMessage, loginPath, safeNext } from './auth'

describe('safeNext', () => {
  it('keeps paths on this site', () => {
    expect(safeNext('/systems/shop?tab=rules#x')).toBe('/systems/shop?tab=rules#x')
    expect(safeNext('/runbooks/api/HighErrorRate')).toBe('/runbooks/api/HighErrorRate')
  })
  it('rejects other sites and odd values', () => {
    for (const v of [null, undefined, '', 'https://evil.example', '//evil.example', '/\\evil.example', 'systems']) {
      expect(safeNext(v)).toBe('/')
    }
  })
  it('never returns to the login page', () => {
    expect(safeNext('/login')).toBe('/')
    expect(safeNext('/login?next=/x')).toBe('/')
  })
})

describe('loginPath', () => {
  it('carries the return address', () => {
    expect(loginPath('/systems/otel-demo/runbooks/cart/HighErrorRate?x=1')).toBe(
      '/login?next=%2Fsystems%2Fotel-demo%2Frunbooks%2Fcart%2FHighErrorRate%3Fx%3D1',
    )
    expect(new URLSearchParams(loginPath('/alerts').split('?')[1]).get('next')).toBe('/alerts')
  })
  it('omits next for the home page and the login page', () => {
    expect(loginPath('/')).toBe('/login')
    expect(loginPath('/login?next=/x')).toBe('/login')
  })
})

describe('login errors', () => {
  it('says what went wrong', () => {
    expect(loginErrorMessage(401)).toBe('Wrong username or password')
    expect(loginErrorMessage(429, 42)).toBe('Too many attempts, wait 42 s')
    expect(loginErrorMessage(429, null)).toBe('Too many attempts, wait 60 s')
    expect(loginErrorMessage(500, null, 'boom')).toBe('Could not sign in: boom')
    expect(loginErrorMessage(500)).toBe('Could not sign in')
  })
  it('recognises the login route', () => {
    expect(isLoginPath('/login')).toBe(true)
    expect(isLoginPath('/loginx')).toBe(false)
  })
})
