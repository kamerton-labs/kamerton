// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { useT } from '@/i18n'

export default function NotFoundPage() {
  const { t } = useT()
  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">{t('notFound.title')}</h1>
      <Button asChild variant="outline">
        <Link to="/">{t('common.home')}</Link>
      </Button>
    </div>
  )
}
