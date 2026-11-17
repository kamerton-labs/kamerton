// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Also shown when a code chunk of an older build fails to load after an update.

import { useRouteError } from 'react-router'
import { Button } from '@/components/ui/button'
import { useT } from '@/i18n'

export default function ErrorPage() {
  const error = useRouteError()
  const { t } = useT()
  const message = error instanceof Error ? error.message : ''
  return (
    <div className="mx-auto w-full max-w-xl space-y-4 p-6">
      <h1 className="text-2xl font-semibold">{t('error.title')}</h1>
      <p className="text-sm text-muted-foreground">
        {t('error.text')}
        {message && <span className="mt-2 block font-mono text-xs break-all">{message}</span>}
      </p>
      <div className="flex gap-2">
        <Button onClick={() => window.location.reload()}>{t('error.reload')}</Button>
        <Button variant="outline" asChild>
          <a href="/">{t('common.home')}</a>
        </Button>
      </div>
    </div>
  )
}
