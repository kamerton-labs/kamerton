// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Check, ChevronDown } from 'lucide-react'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { LANGS, setLang, useT, type Lang } from '@/i18n'

const NAMES: Record<Lang, string> = { en: 'English', ru: 'Русский' }

/** Small flags drawn inline: emoji flags do not render on Windows. */
function Flag({ lang }: { lang: Lang }) {
  const cls = 'h-3 w-[18px] shrink-0 rounded-[2px] ring-1 ring-black/10'
  if (lang === 'ru') {
    return (
      <svg viewBox="0 0 9 6" className={cls} aria-hidden="true">
        <rect width="9" height="6" fill="#fff" />
        <rect y="2" width="9" height="2" fill="#0039a6" />
        <rect y="4" width="9" height="2" fill="#d52b1e" />
      </svg>
    )
  }
  return (
    <svg viewBox="0 0 60 40" className={cls} aria-hidden="true">
      <rect width="60" height="40" fill="#012169" />
      <path d="M0 0l60 40M60 0L0 40" stroke="#fff" strokeWidth="8" />
      <path d="M0 0l60 40M60 0L0 40" stroke="#c8102e" strokeWidth="3" />
      <path d="M30 0v40M0 20h60" stroke="#fff" strokeWidth="12" />
      <path d="M30 0v40M0 20h60" stroke="#c8102e" strokeWidth="7" />
    </svg>
  )
}

export default function LangSwitch() {
  const { t, lang } = useT()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={`${t('shell.language')}: ${NAMES[lang]}`}
          title={t('shell.language')}
          data-testid="lang-switch"
          className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-xs font-medium text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          <Flag lang={lang} />
          <span className="uppercase">{lang}</span>
          <ChevronDown className="size-3" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-36">
        {LANGS.map((l) => (
          <DropdownMenuItem key={l} lang={l} onSelect={() => setLang(l)} data-testid={`lang-option-${l}`}>
            <Flag lang={l} />
            <span className="flex-1">{NAMES[l]}</span>
            {l === lang && <Check className="size-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
