// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Kept apart from the components so it can be unit-tested.

import { t } from '@/i18n'

const EMAIL_RE = /^[^\s@<>(),;:"]+@[^\s@<>(),;:"]+\.[^\s@<>(),;:"]+$/

export function isEmail(v: string): boolean {
  return EMAIL_RE.test(v.trim())
}

/** Splits pasted text ("a@x.ru, b@x.ru; c@x.ru") into addresses, deduplicated case-insensitively. */
export function splitAddresses(text: string, existing: string[] = []): { added: string[]; invalid: string[] } {
  const seen = new Set(existing.map((a) => a.toLowerCase()))
  const added: string[] = []
  const invalid: string[] = []
  for (const raw of text.split(/[\s,;]+/)) {
    const a = raw.trim()
    if (!a) continue
    if (!isEmail(a)) {
      invalid.push(a)
      continue
    }
    if (seen.has(a.toLowerCase())) continue
    seen.add(a.toLowerCase())
    added.push(a)
  }
  return { added, invalid }
}

export const SMARTHOST_RE = /^[^\s:/]+:\d{1,5}$/

const PRESETS = [
  { id: 'yandex', smarthost: 'smtp.yandex.ru:465' },
  { id: 'mailru', smarthost: 'smtp.mail.ru:465' },
  { id: 'gmail', smarthost: 'smtp.gmail.com:587' },
] as const

export interface SmtpPreset {
  name: string
  smarthost: string
  hint: string
}

/** Common mail services: server and a short hint, in the interface language. */
export function smtpPresets(): SmtpPreset[] {
  return PRESETS.map((p) => ({ name: t(`smtp.${p.id}.name` as const), smarthost: p.smarthost, hint: t(`smtp.${p.id}.hint` as const) }))
}

export function presetFor(smarthost: string): SmtpPreset | undefined {
  return smtpPresets().find((p) => p.smarthost === smarthost.trim())
}
