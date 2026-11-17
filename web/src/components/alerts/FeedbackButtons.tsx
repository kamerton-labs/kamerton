// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState } from 'react'
import { cn } from 'cn'
import { Button } from '@/components/ui/button'
import { useT } from '@/i18n'
import { useNoiseFeedback, type NoiseVerdict } from '@/lib/api'

const VERDICTS = [
  { v: 'useful', label: 'noise.useful' },
  { v: 'noise', label: 'noise.noise' },
] as const

export default function FeedbackButtons({ system, alert, service, operation, className }: { system: string; alert: string; service: string; operation?: string; className?: string }) {
  const m = useNoiseFeedback(system)
  const [mine, setMine] = useState<NoiseVerdict | null>(null)
  const { t } = useT()
  const send = (verdict: NoiseVerdict) => {
    setMine(verdict)
    m.mutate({ alert, service, operation, verdict }, { onError: () => setMine(null) })
  }
  return (
    <span role="group" aria-label={t('noise.ask')} title={t('noise.ask')} className={cn('inline-flex flex-wrap items-center gap-1', className)} data-testid="noise-feedback">
      {VERDICTS.map((x) => (
        <Button
          key={x.v}
          type="button"
          variant="ghost"
          size="xs"
          aria-pressed={mine === x.v}
          disabled={m.isPending || mine === x.v}
          className="text-muted-foreground aria-pressed:bg-muted aria-pressed:text-foreground disabled:aria-pressed:opacity-100"
          onClick={() => send(x.v)}
          data-testid={`noise-feedback-${x.v}`}
        >
          {t(x.label)}
        </Button>
      ))}
      {m.isError && <span className="text-xs text-destructive">{t('common.failed', { error: m.error.message })}</span>}
    </span>
  )
}
