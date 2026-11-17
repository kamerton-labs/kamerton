// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// The page behind the runbook links of notifications:
// <external URL>/runbooks/<service>/<alert>. It is rendered from the current
// description, so it always matches the rules that fired.

import { useEffect } from 'react'
import { Download } from 'lucide-react'
import { Link, useParams } from 'react-router'
import { cn } from 'cn'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { SeverityBadge } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useRunbook, type RunbookExperience } from '@/lib/api'
import { dateTime, httpUrl } from '@/lib/format'
import { historyHref, incidentDuration } from '@/lib/incidents'
import { t, useT, type MsgKey } from '@/i18n'
import { serviceTypeLabel } from '@/lib/model/types'

const SIGNALS = ['error_rate', 'latency', 'saturation', 'queue_lag', 'slo_burn_rate', 'traffic', 'no_data', 'availability', 'custom'] as const
type Signal = (typeof SIGNALS)[number]
const isSignal = (s: string): s is Signal => (SIGNALS as readonly string[]).includes(s)

/** Advice for rules that need no threshold from the team (internal/generator/runbook.go runbookHints); others use the signal's advice. */
const ALERT_ADVICE = {
  KamertonTelemetryMissing: 'runbook.adviceAlert.telemetryMissing',
  KamertonServiceTelemetryMissing: 'runbook.adviceAlert.serviceTelemetryMissing',
  KamertonNoReadyPods: 'runbook.adviceAlert.noReadyPods',
  KamertonOOMKilled: 'runbook.adviceAlert.oomKilled',
  KamertonMemoryNearLimit: 'runbook.adviceAlert.memoryNearLimit',
  KamertonCPUNearLimit: 'runbook.adviceAlert.cpuNearLimit',
  KamertonCPUAboveNormal: 'runbook.adviceAlert.cpuAboveNormal',
  KamertonQueueConsumerStopped: 'runbook.adviceAlert.queueStopped',
  KamertonLatencyAboveNormal: 'runbook.adviceAlert.latencyAboveNormal',
} as const satisfies Record<string, MsgKey>

/** Advice lines for an alert; one dictionary entry holds the lines separated by new lines. */
function adviceFor(alert: string, signal: string): string[] {
  const key: MsgKey | undefined =
    alert in ALERT_ADVICE ? ALERT_ADVICE[alert as keyof typeof ALERT_ADVICE] : isSignal(signal) && signal !== 'no_data' && signal !== 'availability' ? (`runbook.advice.${signal}` as const) : undefined
  return key ? t(key).split('\n') : []
}

/** Notes of earlier incidents of the service; placed above the generic advice because they are about this service. */
function Experience({ items, system }: { items: RunbookExperience[]; system: string }) {
  const { t } = useT()
  const shown = items.filter((e) => e.cause?.trim() || e.fix?.trim())
  if (shown.length === 0) return null
  return (
    <Card data-testid="runbook-experience">
      <CardHeader>
        <CardTitle>{t('runbook.experience')}</CardTitle>
      </CardHeader>
      <CardContent>
        <ul className="divide-y">
          {shown.map((e, i) => (
            <li key={e.id ?? i} className="space-y-1 py-3 text-sm first:pt-0 last:pb-0" data-testid="runbook-experience-item">
              <div className="text-xs text-muted-foreground">
                {[e.startedAt && dateTime(e.startedAt), e.durationSeconds !== undefined && t('history.lasted', { d: incidentDuration(e.durationSeconds) })].filter(Boolean).join(' · ')}
                {e.id && (
                  <>
                    {' · '}
                    <Link className="underline-offset-2 hover:underline" to={historyHref(e.id, system)}>
                      {t('history.open')}
                    </Link>
                  </>
                )}
              </div>
              {e.fix?.trim() && (
                <p className="font-medium [overflow-wrap:anywhere] whitespace-pre-line">
                  <span className="text-muted-foreground">{t('note.fix')}: </span>
                  {e.fix}
                </p>
              )}
              {e.cause?.trim() && (
                <p className="[overflow-wrap:anywhere] whitespace-pre-line">
                  <span className="text-muted-foreground">{t('note.cause')}: </span>
                  {e.cause}
                </p>
              )}
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

export default function RunbookPage() {
  const { service = '', alert, id } = useParams()
  const rb = useRunbook(service, id)
  const { t } = useT()

  useEffect(() => {
    if (alert && rb.data) document.getElementById(`alert-${alert}`)?.scrollIntoView({ block: 'start' })
  }, [alert, rb.data])

  if (rb.isLoading) return <Skeleton className="m-6 h-64" />
  if (rb.isError)
    return (
      <div className="p-4 md:p-6">
        <Alert variant="destructive">
          <AlertTitle>{t('runbook.unavailable')}</AlertTitle>
          <AlertDescription>
            {t('runbook.unavailableHint', { service })}{' '}
            <Link className="underline" to="/">
              {t('system.backToList')}
            </Link>
          </AlertDescription>
        </Alert>
      </div>
    )
  const data = rb.data
  if (!data) return null
  const s = data.service
  const ownRunbook = httpUrl(s.runbookURL)
  const download = () => {
    const url = URL.createObjectURL(new Blob([data.markdown], { type: 'text/markdown' }))
    const a = document.createElement('a')
    a.href = url
    a.download = s.system ? `${data.systemID}.md` : `${s.name}.md`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="mx-auto w-full max-w-4xl space-y-4 p-4 md:p-6">
      <div className="flex flex-wrap items-start justify-between gap-2">
        {s.system ? (
          <div>
            <h1 className="text-2xl font-semibold">{t('runbook.systemTitle', { name: data.system })}</h1>
            <p className="text-sm text-muted-foreground" data-testid="runbook-system">
              {t('runbook.systemAbout')}{' '}
              <Link className="underline-offset-2 hover:underline" to={`/systems/${encodeURIComponent(data.systemID)}`}>
                {data.system}
              </Link>
            </p>
          </div>
        ) : (
          <div>
            <h1 className="text-2xl font-semibold">{t('runbook.title', { name: s.name })}</h1>
            <p className="text-sm text-muted-foreground">
              {t('runbook.system')}{' '}
              <Link className="underline-offset-2 hover:underline" to={`/systems/${encodeURIComponent(data.systemID)}?service=${encodeURIComponent(s.name)}`}>
                {data.system}
              </Link>{' '}
              · {serviceTypeLabel(s.type ?? '')} · {s.critical ? t('runbook.critical') : t('runbook.regular')}
              {s.owner ? ` · ${t('runbook.owner', { owner: s.owner })}` : ''}
            </p>
          </div>
        )}
        <div className="flex gap-2">
          <Button variant="outline" size="sm" asChild>
            <Link to={s.system ? `/alerts?system=${encodeURIComponent(data.system)}` : `/alerts?system=${encodeURIComponent(data.system)}&service=${encodeURIComponent(s.name)}`}>
              {t('runbook.firing')}
            </Link>
          </Button>
          <Button variant="outline" size="sm" onClick={download}>
            <Download /> Markdown
          </Button>
        </div>
      </div>

      {s.description && <p className="text-sm">{s.description}</p>}
      {s.runbookURL && (
        <Alert>
          <AlertTitle>{t('runbook.own')}</AlertTitle>
          <AlertDescription>
            {ownRunbook ? (
              <a className="underline" href={ownRunbook} target="_blank" rel="noreferrer">
                {s.runbookURL}
              </a>
            ) : (
              // Not an http(s) address: shown as text, never as a link.
              <span className="break-all">{s.runbookURL}</span>
            )}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap gap-4 text-sm">
        {s.dependsOn?.length ? <div>{t('runbook.dependsOn', { list: s.dependsOn.join(', ') })}</div> : null}
        {s.calledBy?.length ? <div>{t('runbook.calledBy', { list: s.calledBy.join(', ') })}</div> : null}
      </div>

      {data.experience && <Experience items={data.experience} system={data.system} />}
      {data.alerts.length === 0 && (
        <Alert>
          <AlertTitle>{t('runbook.noRules')}</AlertTitle>
          <AlertDescription>{t('runbook.noRulesHint')}</AlertDescription>
        </Alert>
      )}
      {data.alerts.map((a, i) => (
        <Card
          key={`${a.alert}-${a.operation ?? ''}-${i}`}
          id={data.alerts.findIndex((x) => x.alert === a.alert) === i ? `alert-${a.alert}` : undefined}
          className={cn('scroll-mt-4', alert === a.alert && 'border-primary ring-2 ring-primary/30')}
        >
          <CardHeader>
            <div className="flex flex-wrap items-center gap-2">
              <CardTitle>{(a.title || a.summary).replace(/\{\{[^}]*\}\}/g, t('runbook.any'))}</CardTitle>
              <SeverityBadge severity={a.severity === 'critical' ? 'critical' : 'warning'} />
              <Badge variant="outline">{isSignal(a.signal) ? t(`runbook.signal.${a.signal}` as const) : a.signal}</Badge>
            </div>
            <CardDescription>
              {a.operation ? t('runbook.operation', { op: a.operation }) : ''}
              {a.threshold ? `${a.operation ? ' · ' : ''}${t('runbook.threshold', { v: a.threshold })}` : ''}
              {a.for && a.for !== '0s' ? ` · ${t('runbook.for', { d: a.for })}` : ''}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <ul className="list-disc space-y-1 pl-5 text-sm">
              {adviceFor(a.alert, a.signal).map((line) => (
                <li key={line}>{line}</li>
              ))}
            </ul>
            <details>
              <summary className="cursor-pointer text-sm text-muted-foreground">{t('runbook.promql')}</summary>
              <div className="mt-2 text-xs text-muted-foreground">{a.alert}</div>
              <pre className="mt-1 overflow-auto rounded-md bg-muted p-3 text-xs">{a.expr}</pre>
            </details>
          </CardContent>
        </Card>
      ))}
    </div>
  )
}
