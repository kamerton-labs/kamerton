// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useT } from '@/i18n'
import { RANGES, type Range } from '@/lib/model/noise'

export default function RangeSwitch({ value, onChange, label }: { value: Range; onChange: (v: Range) => void; label: string }) {
  const { t } = useT()
  return (
    <div className="flex gap-0.5 rounded-md bg-muted p-0.5" role="group" aria-label={label}>
      {RANGES.map((r) => (
        <button
          key={r.v}
          type="button"
          aria-pressed={value === r.v}
          onClick={() => onChange(r.v)}
          className={`rounded px-1.5 py-0.5 text-[11px] ${value === r.v ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground'}`}
        >
          {t(r.label)}
        </button>
      ))}
    </div>
  )
}
