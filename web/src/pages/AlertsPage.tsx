// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Active alerts of all systems, optionally filtered by ?alertname=&service=
// (the "Open in Kamerton" link of notifications points here) and ?system=.

import { useMemo } from 'react'
import { ChevronRight } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { Page, SeverityBadge } from '@/components/common'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useAlerts, type ActiveAlert } from '@/lib/api'
import { useT } from '@/i18n'
import { dateTime, sinceText } from '@/lib/format'

function runbookLink(a: ActiveAlert) {
  const tail = `runbooks/${encodeURIComponent(a.service)}/${encodeURIComponent(a.name)}`
  return a.system ? `/systems/${encodeURIComponent(a.system)}/${tail}` : `/${tail}`
}

function AlertCard({ a }: { a: ActiveAlert }) {
  const sev = a.severity === 'critical' ? 'critical' : 'warning'
  const { t } = useT()
  return (
    <div className={`space-y-3 rounded-xl border bg-card p-4 ${sev === 'critical' ? 'border-firing/40' : ''}`}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <SeverityBadge severity={sev} />
            <span className="text-base font-semibold">{a.title || a.summary || a.name}</span>
          </div>
          <div className="text-sm text-muted-foreground">
            {a.system && (
              <Link className="underline-offset-2 hover:underline" to={`/systems/${encodeURIComponent(a.system)}?service=${encodeURIComponent(a.service)}`}>
                {a.system} · {a.service}
              </Link>
            )}
            {!a.system && a.service}
            {' · '}
            <span title={dateTime(a.startsAt)}>{sinceText(a.startsAt)}</span>
          </div>
        </div>
        <Button variant="outline" size="sm" asChild>
          <Link to={runbookLink(a)}>{t('alerts.runbook')}</Link>
        </Button>
      </div>
      {(a.value || a.threshold) && (
        <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
          {a.value && (
            <span>
              <span className="text-muted-foreground">{t('alerts.now')} </span>
              <span className="font-medium">{a.value}</span>
            </span>
          )}
          {a.threshold && (
            <span>
              <span className="text-muted-foreground">{t('alerts.threshold')} </span>
              {a.threshold}
            </span>
          )}
        </div>
      )}
      {a.triage && a.triage.chain.length > 1 && (
        <div className="flex flex-wrap items-center gap-1 text-sm">
          <span className="text-muted-foreground">{t('alerts.probableSource')}</span>
          <span className="font-medium text-firing">{a.triage.probableSource}</span>
          <span className="mx-1 text-muted-foreground">·</span>
          {a.triage.chain.map((s, j) => (
            <span key={s} className="flex items-center gap-1">
              {j > 0 && <ChevronRight className="size-3.5 text-muted-foreground" />}
              <span className={s === a.triage?.probableSource ? 'font-medium text-firing' : ''}>{s}</span>
            </span>
          ))}
        </div>
      )}
      <details className="text-xs text-muted-foreground">
        <summary className="cursor-pointer">{t('alerts.labels')}</summary>
        <div className="mt-1 flex flex-wrap gap-1 font-mono">
          {Object.entries(a.labels).map(([k, v]) => (
            <span key={k} className="rounded bg-muted px-1.5 py-0.5">
              {k}={v}
            </span>
          ))}
        </div>
      </details>
    </div>
  )
}

export default function AlertsPage() {
  const [params] = useSearchParams()
  const alertname = params.get('alertname') ?? ''
  const service = params.get('service') ?? ''
  const system = params.get('system') ?? ''
  const alerts = useAlerts()
  const { t } = useT()
  const list = useMemo(
    () =>
      (alerts.data?.alerts ?? [])
        .filter((a) => (!alertname || a.name === alertname) && (!service || a.service === service) && (!system || a.system === system))
        .sort((a, b) => (a.severity === b.severity ? Date.parse(b.startsAt) - Date.parse(a.startsAt) : a.severity === 'critical' ? -1 : 1)),
    [alerts.data, alertname, service, system],
  )
  const filtered = Boolean(alertname || service || system)
  const runbook = service ? `/runbooks/${encodeURIComponent(service)}${alertname ? `/${encodeURIComponent(alertname)}` : ''}` : ''

  return (
    <Page
      title={t('alerts.title')}
      description={
        filtered
          ? t('alerts.filtered', { what: [system, service].filter(Boolean).join(' · ') + (alertname ? t('alerts.filteredRule') : '') })
          : t('alerts.description')
      }
      actions={
        <>
          {runbook && (
            <Button variant="outline" size="sm" asChild>
              <Link to={runbook}>{t('alerts.runbook')}</Link>
            </Button>
          )}
          {filtered && (
            <Button variant="ghost" size="sm" asChild>
              <Link to="/alerts">{t('alerts.showAll')}</Link>
            </Button>
          )}
        </>
      }
    >
      {alerts.isLoading ? (
        <Skeleton className="h-40" />
      ) : alerts.isError ? (
        <Alert variant="destructive">
          <AlertTitle>{t('alerts.loadError')}</AlertTitle>
          <AlertDescription>{alerts.error.message}</AlertDescription>
        </Alert>
      ) : !alerts.data?.configured ? (
        <Alert>
          <AlertTitle>{t('alerts.noAm')}</AlertTitle>
          <AlertDescription>
            {t('alerts.noAmText')}{' '}
            <Link className="underline" to="/status">
              {t('shell.status')}
            </Link>
          </AlertDescription>
        </Alert>
      ) : list.length === 0 ? (
        <div className="rounded-xl border border-dashed p-8 text-center">
          <p className="text-base font-medium">{filtered ? t('alerts.emptyFiltered') : t('alerts.empty')}</p>
          <p className="mt-1 text-sm text-muted-foreground">{filtered ? t('alerts.emptyFilteredHint') : t('alerts.emptyHint')}</p>
        </div>
      ) : (
        <div className="space-y-3">
          {list.map((a, i) => (
            <AlertCard key={`${a.name}-${a.system}-${a.service}-${i}`} a={a} />
          ))}
        </div>
      )}
    </Page>
  )
}
