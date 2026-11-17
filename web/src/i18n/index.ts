// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Interface language. English is the default; Russian is the second
// language. The dictionaries are plain objects (en.ts, ru.ts): ru has the
// type of en, so a key missing in one of them fails the type check.
//
// The current language lives in a small module-level store, so code outside
// React (lib/model, validators) reads it through t() as well; components
// subscribe with useT() and re-render when the language changes.

import { createElement, Fragment, useSyncExternalStore, type ReactNode } from 'react'
import { en, type PluralEn, type PluralRu } from './en'
import { ru } from './ru'

export type Lang = 'en' | 'ru'
export const LANGS: Lang[] = ['en', 'ru']
export const DEFAULT_LANG: Lang = 'en'

type En = typeof en
/** Keys of plain strings. */
export type MsgKey = { [K in keyof En]: En[K] extends string ? K : never }[keyof En]
/** Keys of plural forms ({n} is the count). */
export type PluralKey = { [K in keyof En]: En[K] extends string ? never : K }[keyof En]
export type Vars = Record<string, string | number>

const dicts: Record<Lang, Record<string, string | PluralEn | PluralRu>> = { en, ru }

const STORAGE_KEY = 'kamerton-lang'

function initialLang(): Lang {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    if (v === 'en' || v === 'ru') return v
  } catch {
    // Storage may be unavailable (private mode, tests).
  }
  return DEFAULT_LANG
}

let current: Lang = initialLang()
const listeners = new Set<() => void>()

function applyHtmlLang() {
  if (typeof document !== 'undefined') document.documentElement.lang = current
}
applyHtmlLang()

export const getLang = (): Lang => current

/** Switches the language, remembers it (unless persist is false) and re-renders subscribers. */
export function setLang(lang: Lang, persist = true) {
  if (persist) {
    try {
      localStorage.setItem(STORAGE_KEY, lang)
    } catch {
      // Storage may be unavailable: the choice lasts until reload.
    }
  }
  if (lang === current) return
  current = lang
  applyHtmlLang()
  for (const l of listeners) l()
}

function subscribe(cb: () => void) {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

function interpolate(s: string, vars?: Vars): string {
  if (!vars) return s
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m))
}

/** Russian plural category: one (1, 21), few (2-4, 22-24), many (5-20, 25). */
export function ruPlural(n: number): 'one' | 'few' | 'many' {
  const a = Math.abs(n) % 100
  const b = a % 10
  if (a > 10 && a < 20) return 'many'
  if (b > 1 && b < 5) return 'few'
  if (b === 1) return 'one'
  return 'many'
}

/** A string of a given language (notification texts follow the description's language, not the interface). */
export function tl(lang: Lang, key: MsgKey, vars?: Vars): string {
  const v = dicts[lang][key] ?? dicts.en[key]
  return interpolate(typeof v === 'string' ? v : key, vars)
}

/** A plural form of a given language; {n} is replaced with the count. */
export function tnl(lang: Lang, key: PluralKey, n: number, vars?: Vars): string {
  const v = dicts[lang][key] ?? dicts.en[key]
  let form = key as string
  if (v && typeof v === 'object') {
    if (lang === 'ru' && 'many' in v) form = v[ruPlural(n)]
    else if ('other' in v) form = Math.abs(n) === 1 ? v.one : v.other
  }
  return interpolate(form, { n, ...vars })
}

/** A string in the current language. */
export const t = (key: MsgKey, vars?: Vars): string => tl(current, key, vars)

/** A plural form in the current language. */
export const tn = (key: PluralKey, n: number, vars?: Vars): string => tnl(current, key, n, vars)

/**
 * A string with React nodes in place of {name}: rich('rules.firedTimes', { n: <b>3</b> }).
 * Returns a fragment, so it can be put anywhere a node goes.
 */
export function richl(lang: Lang, key: MsgKey, nodes: Record<string, ReactNode>): ReactNode {
  const s = tl(lang, key)
  const parts: ReactNode[] = []
  let last = 0
  for (const m of s.matchAll(/\{(\w+)\}/g)) {
    if (!(m[1] in nodes)) continue
    parts.push(s.slice(last, m.index))
    parts.push(nodes[m[1]])
    last = m.index + m[0].length
  }
  parts.push(s.slice(last))
  return createElement(Fragment, null, ...parts)
}

export interface Translator {
  lang: Lang
  t: (key: MsgKey, vars?: Vars) => string
  tn: (key: PluralKey, n: number, vars?: Vars) => string
  rich: (key: MsgKey, nodes: Record<string, ReactNode>) => ReactNode
}

// One translator per language: its identity changes with the language, so
// memoized values that use it are recomputed after a switch.
const translators = Object.fromEntries(
  LANGS.map((lang) => [
    lang,
    {
      lang,
      t: (key: MsgKey, vars?: Vars) => tl(lang, key, vars),
      tn: (key: PluralKey, n: number, vars?: Vars) => tnl(lang, key, n, vars),
      rich: (key: MsgKey, nodes: Record<string, ReactNode>) => richl(lang, key, nodes),
    },
  ]),
) as Record<Lang, Translator>

/** The current language; re-renders the component when it changes. */
export function useLang(): Lang {
  return useSyncExternalStore(subscribe, getLang, getLang)
}

/** Translation functions bound to the current language. */
export function useT(): Translator {
  return translators[useLang()]
}
