// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { createContext, use, useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'

/** Two themes only. The first visit follows the system setting; after that the choice is remembered. */
export type Theme = 'light' | 'dark'

const STORAGE_KEY = 'kamerton-theme'

interface ThemeState {
  theme: Theme
  setTheme: (t: Theme) => void
  toggle: () => void
}

const ThemeContext = createContext<ThemeState | null>(null)

const systemTheme = (): Theme => (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')

function initialTheme(): Theme {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    // Anything else (including "system" from earlier versions) counts as a first visit.
    if (v === 'light' || v === 'dark') return v
  } catch {
    // Storage may be unavailable (private mode).
  }
  return systemTheme()
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(initialTheme)

  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark')
  }, [theme])

  const setTheme = useCallback((t: Theme) => {
    setThemeState(t)
    try {
      localStorage.setItem(STORAGE_KEY, t)
    } catch {
      // Storage may be unavailable (private mode): the choice lasts until reload.
    }
  }, [])

  const toggle = useCallback(() => setTheme(theme === 'dark' ? 'light' : 'dark'), [theme, setTheme])

  const value = useMemo(() => ({ theme, setTheme, toggle }), [theme, setTheme, toggle])
  return <ThemeContext value={value}>{children}</ThemeContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useTheme(): ThemeState {
  const ctx = use(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used inside ThemeProvider')
  return ctx
}
