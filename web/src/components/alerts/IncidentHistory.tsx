// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useEffect } from 'react'
import { ChevronDown } from 'lucide-react'
import { Evidence } from './IncidentBlock'
import NoteEditor, { NoteMeta } from './NoteEditor'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useT } from '@/i18n'
import { useIncidentHistory, useSystems, type HistoryIncident } from '@/lib/api'
import { dateTime, timeAgo } from '@/lib/format'
import { byNewest, hasEvidenceDetails, hasNote, historyAlertTitle, incidentDuration } from '@/lib/incidents'

const ALL = '*'

// Records and live incidents carry the system's name, so the filter uses names too.
function SystemFilter({ system, onChange }: { system: string; onChange: (s: string) => void }) {
  const systems = useSystems()
  const { t } = useT()
  const list = systems.data?.systems ?? []
  if (list.length < 2 && !system) return null
  return (
    <Select value={system || ALL} onValueChange={(v) => onChange(v === ALL ? '' : v)}>
      <SelectTrigger size="sm" className="min-w-40" aria-label={t('history.system')} data-testid="history-system">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>{t('history.allSystems')}</SelectItem>
        {list.map((s) => (
          <SelectItem key={s.id} value={s.name}>
            {s.name}
          </SelectItem>
        ))}
        {system && !list.some((s) => s.name === system) && <SelectItem value={system}>{system}</SelectItem>}
      </SelectContent>
    </Select>
  )
}

function Row({ inc, open }: { inc: HistoryIncident; open: boolean }) {
  const { t, tn } = useT()
  const root = inc.rootAlerts?.[0]
  const service = inc.roots?.length ? inc.roots.join(', ') : root?.service || inc.system || ''
  const title = historyAlertTitle(root)
  const affected = inc.affected?.length ?? 0
  const note = inc.note
  const ev = inc.evidence
  const domId = inc.id ? `history-${inc.id}` : undefined
  return (
    <li id={domId} className="scroll-mt-4" data-testid="history-row" data-incident={inc.id}>
      <details className="group" open={open}>
        <summary className="flex cursor-pointer list-none gap-2 rounded-lg px-1 py-3 outline-none focus-visible:ring-3 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
          <ChevronDown className="mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" />
          <div className="min-w-0 flex-1 space-y-1">
            <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
              <span className="min-w-0 [overflow-wrap:anywhere]">
                <span className="font-medium">{service}</span>
                {title && <span className="text-muted-foreground"> · {title}</span>}
              </span>
              <span className="shrink-0 text-sm text-muted-foreground">
                {inc.startedAt && <span title={dateTime(inc.startedAt)}>{timeAgo(inc.startedAt)}</span>}
                {inc.open ? (
                  <span className="font-medium text-firing" data-testid="history-ongoing">
                    {inc.startedAt && ' · '}
                    {t('history.ongoing')}
                  </span>
                ) : (
                  inc.durationSeconds !== undefined && (
                    <span>
                      {inc.startedAt && ' · '}
                      {t(inc.endEstimated ? 'history.lastedAbout' : 'history.lasted', { d: incidentDuration(inc.durationSeconds) })}
                    </span>
                  )
                )}
              </span>
            </div>
            <div className="text-xs text-muted-foreground">
              {[inc.system, affected > 0 && tn('history.affected', affected)].filter(Boolean).join(' · ')}
            </div>
            {hasNote(note) && (
              <p className="line-clamp-2 text-sm [overflow-wrap:anywhere]" data-testid="history-note">
                <span className="text-muted-foreground">{note?.fix?.trim() ? t('note.fix') : t('note.cause')}: </span>
                {note?.fix?.trim() || note?.cause}
              </p>
            )}
          </div>
        </summary>
        <div className="space-y-4 pb-4 pl-7" data-testid="history-details">
          {ev?.cause && (
            <p className="text-sm" data-testid="history-cause">
              {t(ev.likely ? 'incident.causeLikely' : 'incident.cause', { cause: ev.cause })}
            </p>
          )}
          {inc.affected && inc.affected.length > 0 && (
            <p className="text-sm">
              <span className="text-muted-foreground">{t('incident.alsoAffected')} </span>
              {inc.affected.join(', ')}
            </p>
          )}
          {ev && hasEvidenceDetails(ev) && <Evidence e={ev} />}
          {inc.id ? <NoteEditor id={inc.id} note={note} /> : <NoteMeta note={note} />}
        </div>
      </details>
    </li>
  )
}

/** Past incidents with the notes people left; filters come from the URL (system, service). */
export default function IncidentHistory({ system, service, selected, onSystem }: { system: string; service: string; selected: string; onSystem: (s: string) => void }) {
  const history = useIncidentHistory(system, service)
  const { t } = useT()
  const list = history.data?.incidents ? byNewest(history.data.incidents) : []

  useEffect(() => {
    if (selected && list.length) document.getElementById(`history-${selected}`)?.scrollIntoView({ block: 'start' })
  }, [selected, list.length])

  if (history.isLoading) return <Skeleton className="h-40" />
  if (history.isError)
    return (
      <Alert variant="destructive">
        <AlertTitle>{t('history.loadError')}</AlertTitle>
        <AlertDescription>{history.error.message}</AlertDescription>
      </Alert>
    )
  // An older core without the history answers 404.
  if (history.data === null) return null
  const filtered = Boolean(system || service)
  const missing = Boolean(selected) && !list.some((i) => i.id === selected)
  return (
    <div className="space-y-3" data-testid="history">
      <SystemFilter system={system} onChange={onSystem} />
      {missing && (
        <p className="text-sm text-muted-foreground" data-testid="history-missing">
          {t('history.missing')}
        </p>
      )}
      {list.length === 0 ? (
        <div className="rounded-xl border border-dashed p-8 text-center" data-testid="history-empty">
          <p className="text-sm text-muted-foreground">{filtered ? t('history.emptyFiltered') : t('history.empty')}</p>
        </div>
      ) : (
        <ul className="divide-y rounded-xl border bg-card px-3">
          {list.map((inc, i) => (
            <Row key={inc.id ?? i} inc={inc} open={Boolean(selected) && inc.id === selected} />
          ))}
        </ul>
      )}
    </div>
  )
}
