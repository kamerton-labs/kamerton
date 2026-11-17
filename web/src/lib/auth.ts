// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Login (enabled on the core with KAMERTON_AUTH_USER / KAMERTON_AUTH_PASSWORD).
// The session lives in an HttpOnly cookie; the UI only asks the core whether
// it has one. Every request carries X-Requested-With, which the core requires
// on every state-changing request, with or without login (CSRF protection).

import { t } from '@/i18n'

export const CSRF_HEADER = 'X-Requested-With'
export const CSRF_VALUE = 'kamerton'

export interface Session {
  enabled: boolean
  authenticated: boolean
  user: string
}

export const LOGIN_PATH = '/login'

/** A return address is only ever a path on this site (no open redirect), and never the login page itself. */
export function safeNext(raw: string | null | undefined): string {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//') || raw.startsWith('/\\')) return '/'
  const path = raw.split(/[?#]/, 1)[0]
  if (path === LOGIN_PATH || path.startsWith(LOGIN_PATH + '/')) return '/'
  return raw
}

/** The login page address that brings the user back to `current` afterwards. */
export function loginPath(current: string): string {
  const next = safeNext(current)
  return next === '/' ? LOGIN_PATH : `${LOGIN_PATH}?next=${encodeURIComponent(next)}`
}

export const isLoginPath = (pathname: string) => pathname === LOGIN_PATH || pathname.startsWith(LOGIN_PATH + '/')

/** Message for a failed login, in the interface language. */
export function loginErrorMessage(status: number, retryAfter?: number | null, fallback?: string): string {
  if (status === 401) return t('login.error.wrong')
  if (status === 429) return t('login.error.tooMany', { s: Math.max(1, Math.ceil(retryAfter ?? 60)) })
  if (status === 0) return t('login.error.noCore')
  return fallback ? t('login.error.failedWith', { reason: fallback }) : t('login.error.failed')
}

export class LoginError extends Error {
  readonly status: number
  readonly retryAfter: number | null

  constructor(status: number, retryAfter: number | null, message: string) {
    super(message)
    this.status = status
    this.retryAfter = retryAfter
  }
}

async function authRequest<T>(path: string, init?: RequestInit): Promise<{ res: Response; body: T | undefined }> {
  const headers = new Headers(init?.headers)
  headers.set(CSRF_HEADER, CSRF_VALUE)
  const res = await fetch(`/api/auth${path}`, { ...init, headers, credentials: 'same-origin' })
  let body: T | undefined
  try {
    body = (await res.json()) as T
  } catch {
    body = undefined
  }
  return { res, body }
}

export async function fetchSession(): Promise<Session> {
  const { res, body } = await authRequest<Session>('/session')
  if (res.status === 404) {
    // A core without login support.
    return { enabled: false, authenticated: false, user: '' }
  }
  if (!res.ok || !body) throw new Error(`session: ${res.status}`)
  return body
}

export async function login(username: string, password: string): Promise<Session> {
  let out: Awaited<ReturnType<typeof authRequest<Session & { error?: string; retryAfter?: number }>>>
  try {
    out = await authRequest('/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) })
  } catch {
    throw new LoginError(0, null, loginErrorMessage(0))
  }
  const { res, body } = out
  if (!res.ok) {
    const header = Number(res.headers.get('Retry-After'))
    const retryAfter = body?.retryAfter ?? (Number.isFinite(header) && header > 0 ? header : null)
    throw new LoginError(res.status, retryAfter, loginErrorMessage(res.status, retryAfter, body?.error ?? res.statusText))
  }
  return body as Session
}

export async function logout(): Promise<void> {
  await authRequest('/logout', { method: 'POST' })
}

let unauthorizedHandler: (() => void) | null = null

/** Called by the API client on 401: the session is missing or has expired. */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  unauthorizedHandler = fn
}

export function notifyUnauthorized() {
  if (unauthorizedHandler) {
    unauthorizedHandler()
    return
  }
  const { pathname, search, hash } = window.location
  if (!isLoginPath(pathname)) window.location.assign(loginPath(pathname + search + hash))
}
