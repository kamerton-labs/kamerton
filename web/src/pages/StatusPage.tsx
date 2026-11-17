// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState } from 'react'
import { AlertTriangle, Plus, RefreshCw, Search } from 'lucide-react'
import { Page } from '@/components/common'
import DiscoverPanel from '@/components/stacks/DiscoverPanel'
import StackBlock from '@/components/stacks/StackBlock'
import StackFormDialog from '@/components/stacks/StackFormDialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useT } from '@/i18n'
import { useSettings, useStacks, useVersion } from '@/lib/api'
import type { StackDraft } from '@/lib/stacks'

export default function StatusPage() {
  const stacks = useStacks()
  const settings = useSettings()
  const version = useVersion()
  const { t } = useT()
  const [discovering, setDiscovering] = useState(false)
  const [creating, setCreating] = useState<StackDraft | null>(null)
  const s = settings.data
  const sets = stacks.data?.sets ?? []
  const discovery = stacks.data?.discovery
  const modeLabel = { cluster: t('status.mode.cluster'), file: t('status.mode.file'), none: t('status.mode.none') }
  return (
    <Page
      title={t('shell.status')}
      description={version.data ? t('status.version', { v: version.data.version }) : undefined}
      actions={
        <>
          <Button variant="outline" size="sm" onClick={() => stacks.refetch()} disabled={stacks.isFetching}>
            <RefreshCw /> {t('status.check')}
          </Button>
          <Button
            variant="outline"
            size="sm"
            data-testid="discover-button"
            onClick={() => setDiscovering(true)}
            title={discovery && !discovery.available ? discovery.reason : t('status.discoverHint')}
          >
            <Search /> {t('status.discover')}
          </Button>
          <Button size="sm" data-testid="add-stack" onClick={() => setCreating({})}>
            <Plus /> {t('status.addSet')}
          </Button>
        </>
      }
    >
      <p className="text-sm text-muted-foreground">
        {t('status.intro')}
      </p>
      {discovering && (
        <DiscoverPanel sets={sets} onClose={() => setDiscovering(false)} onCreateSet={setCreating} />
      )}
      {stacks.data?.warnings.map((w) => (
        <p key={w} className="flex gap-2 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2 text-sm">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warn" />
          {w}
        </p>
      ))}
      {stacks.isLoading ? (
        <Skeleton className="h-64" />
      ) : stacks.isError ? (
        <Alert variant="destructive">
          <AlertTitle>{t('status.checkError')}</AlertTitle>
          <AlertDescription>{stacks.error.message}</AlertDescription>
        </Alert>
      ) : (
        sets.map((set) => <StackBlock key={set.id} set={set} total={sets.length} />)
      )}
      <Card>
        <CardHeader>
          <CardTitle>{t('status.settings')}</CardTitle>
          <CardDescription>{t('status.settingsHint')}</CardDescription>
        </CardHeader>
        <CardContent>
          {settings.isError ? (
            <p className="text-sm text-destructive">{t('status.settingsError', { error: settings.error.message })}</p>
          ) : !s ? (
            <Skeleton className="h-16" />
          ) : (
            <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[max-content_1fr]">
              <dt className="text-muted-foreground">{t('status.externalURL')}</dt>
              <dd>
                {s.externalURL ? (
                  <code>{s.externalURL}</code>
                ) : (
                  <span>
                    {t('status.externalURLNone')} Helm: <code>--set externalURL=…</code>, {t('status.flag')} <code>--external-url</code>.
                  </span>
                )}
              </dd>
              <dt className="text-muted-foreground">{t('status.storage')}</dt>
              <dd>{modeLabel[s.mode]}</dd>
              {s.namespace && (
                <>
                  <dt className="text-muted-foreground">{t('status.namespace')}</dt>
                  <dd>
                    <code>{s.namespace}</code>
                  </dd>
                </>
              )}
              <dt className="text-muted-foreground">{t('status.discovery')}</dt>
              <dd>{!discovery ? '…' : discovery.available ? t('status.discoveryOn') : discovery.reason}</dd>
              <dt className="text-muted-foreground">{t('status.auth')}</dt>
              <dd>{s.auth.enabled ? t('status.authOn') : t('status.authOff')}</dd>
            </dl>
          )}
        </CardContent>
      </Card>
      {creating && <StackFormDialog draft={creating} onClose={() => setCreating(null)} />}
    </Page>
  )
}
