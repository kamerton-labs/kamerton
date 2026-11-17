// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Fragment } from 'react'
import { ChevronDown } from 'lucide-react'
import { Link } from 'react-router'
import { cn } from 'cn'
import { SeverityBadge } from '@/components/common'
import { Button } from '@/components/ui/button'
import { useT } from '@/i18n'
import type { Incident, IncidentAlert, IncidentEvidence } from '@/lib/api'
import { dateTime, httpUrl, sinceText, timeAgo } from '@/lib/format'
import { alertTitle, capText, hasEvidenceDetails, isCritical, isSystemIncident, LOG_LINE_MAX, runbookHref, serviceHref } from '@/lib/incidents'

const sev = (s: string) => (s === 'critical' ? 'critical' : 'warning')
const linkCls = 'underline-offset-2 hover:underline'

function ServiceLinks({ system, services }: { system: string; services: string[] }) {
  return services.map((s, i) => (
    <Fragment key={s}>
      {i > 0 && ', '}
      <Link className={linkCls} to={serviceHref(system, s)}>
        {s}
      </Link>
    </Fragment>
  ))
}

function RootAlert({ a, system }: { a: IncidentAlert; system: string }) {
  const { t } = useT()
  return (
    <li className="flex flex-wrap items-start justify-between gap-2" data-testid="incident-root-alert">
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <SeverityBadge severity={sev(a.severity)} />
          <span className="text-base font-semibold">{alertTitle(a)}</span>
        </div>
        <div className="text-sm text-muted-foreground">
          {a.service ? (
            <>
              {system} · <ServiceLinks system={system} services={[a.service]} />
            </>
          ) : (
            <Link className={linkCls} to={serviceHref(system, '')}>
              {system}
            </Link>
          )}
          {a.value && (
            <>
              {' · '}
              {t('alerts.now')} <span className="font-medium text-foreground">{a.value}</span>
            </>
          )}
          {' · '}
          <span title={dateTime(a.startsAt)}>{sinceText(a.startsAt)}</span>
        </div>
      </div>
      <Button variant="outline" size="sm" asChild>
        <Link to={runbookHref(system, a.service, a.name)}>{t('alerts.runbook')}</Link>
      </Button>
    </li>
  )
}

function Evidence({ e }: { e: IncidentEvidence }) {
  const { t, tn } = useT()
  const traces = httpUrl(e.tracesURL)
  const row = 'grid gap-0.5 sm:grid-cols-[9rem_1fr] sm:gap-3'
  return (
    <div className="space-y-2 border-t pt-3 text-sm" data-testid="incident-evidence">
      <dl className="space-y-2">
        {e.changes && e.changes.length > 0 && (
          <div className={row} data-testid="incident-changes">
            <dt className="text-muted-foreground">{t('incident.changes')}</dt>
            <dd>
              <ul className="space-y-0.5">
                {e.changes.map((c, i) => (
                  <li key={i}>{c}</li>
                ))}
              </ul>
            </dd>
          </div>
        )}
        {e.operation && (
          <div className={row} data-testid="incident-operation">
            <dt className="text-muted-foreground">{t('incident.operation')}</dt>
            <dd className="min-w-0">
              <code className="font-mono text-xs [overflow-wrap:anywhere]">{e.operation}</code>
              {e.operationFailures ? <span className="text-muted-foreground"> · {tn('incident.failures', e.operationFailures)}</span> : null}
            </dd>
          </div>
        )}
        {e.logLine && (
          <div className={row} data-testid="incident-log">
            <dt className="text-muted-foreground">{t('incident.log')}</dt>
            <dd className="min-w-0">
              <code className="block rounded bg-muted px-2 py-1 font-mono text-xs whitespace-pre-wrap [overflow-wrap:anywhere]">{capText(e.logLine, LOG_LINE_MAX)}</code>
            </dd>
          </div>
        )}
        {traces && (
          <div className={row}>
            <dt className="text-muted-foreground">{t('incident.traces')}</dt>
            <dd>
              <a className="underline underline-offset-2" href={traces} target="_blank" rel="noreferrer" data-testid="incident-traces">
                {t('incident.tracesLink')}
              </a>
            </dd>
          </div>
        )}
      </dl>
      {e.collectedAt && <p className="text-xs text-muted-foreground">{t('incident.collected', { ago: timeAgo(e.collectedAt) })}</p>}
    </div>
  )
}

/** One incident: what broke (root alerts), why, and who else is affected. systemText is the core's message text of a system-wide alert. */
export default function IncidentBlock({ inc, openAffected = false, systemText }: { inc: Incident; openAffected?: boolean; systemText?: string }) {
  const { t, tn } = useT()
  const ev = inc.evidence
  const roots = inc.roots.join(', ')
  const wide = isSystemIncident(inc)
  const cause = ev?.cause
    ? t(ev.likely ? 'incident.causeLikely' : 'incident.cause', { cause: ev.cause })
    : wide
      ? systemText || t('incident.systemCause')
      : tn(ev?.likely ? 'incident.causeRootsLikely' : 'incident.causeRoots', inc.roots.length, { roots })
  const muted = inc.affectedAlerts.filter((a) => a.muted).length
  return (
    <section data-testid="incident" data-incident={inc.id} className={cn('space-y-3 rounded-xl border bg-card p-4', isCritical(inc) && 'border-firing/40')}>
      <ul className="space-y-3">
        {inc.rootAlerts.map((a, i) => (
          <RootAlert key={a.fingerprint || `${a.service}-${a.name}-${i}`} a={a} system={inc.system} />
        ))}
      </ul>
      <div className="space-y-0.5">
        <p className="text-sm" data-testid="incident-cause">
          {cause}
        </p>
        {ev?.explains && ev.explains.length > 0 && (
          <p className="text-xs text-muted-foreground" data-testid="incident-explains">
            {tn('incident.explains', ev.explains.length, { services: ev.explains.join(', ') })}
          </p>
        )}
      </div>
      {ev && hasEvidenceDetails(ev) && <Evidence e={ev} />}
      {inc.affected.length > 0 && (
        <div className="space-y-1.5 border-t pt-3 text-sm" data-testid="incident-affected">
          <div>
            <span className="text-muted-foreground">{t('incident.alsoAffected')} </span>
            <ServiceLinks system={inc.system} services={inc.affected} />
          </div>
          {inc.affectedAlerts.length > 0 && (
            <details className="group" open={openAffected} data-testid="incident-muted">
              <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded text-muted-foreground outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
                <ChevronDown className="size-3.5 transition-transform group-open:rotate-180" />
                {tn('incident.affectedAlerts', inc.affectedAlerts.length)}
              </summary>
              <div className="mt-2 space-y-2 pl-4.5">
                {muted > 0 && (
                  <p className="text-xs text-muted-foreground">
                    {wide ? t('incident.mutedSystem') : muted === inc.affectedAlerts.length ? t('incident.mutedAll', { roots }) : tn('incident.mutedSome', muted, { roots })}
                  </p>
                )}
                <ul className="space-y-1.5">
                  {inc.affectedAlerts.map((a, i) => (
                    <li key={a.fingerprint || `${a.service}-${a.name}-${i}`} className="flex flex-wrap items-center gap-x-2 gap-y-0.5" data-testid="incident-muted-alert">
                      <SeverityBadge severity={sev(a.severity)} />
                      <Link className={cn(linkCls, 'font-medium')} to={serviceHref(inc.system, a.service)}>
                        {a.service}
                      </Link>
                      <span className="min-w-0">{alertTitle(a)}</span>
                      {a.value && <span className="text-muted-foreground">· {a.value}</span>}
                      <span className="text-muted-foreground" title={dateTime(a.startsAt)}>
                        · {sinceText(a.startsAt)}
                      </span>
                      <span className="text-muted-foreground">
                        ·{' '}
                        <Link className={linkCls} to={runbookHref(inc.system, a.service, a.name)}>
                          {t('alerts.runbook')}
                        </Link>
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            </details>
          )}
        </div>
      )}
    </section>
  )
}
