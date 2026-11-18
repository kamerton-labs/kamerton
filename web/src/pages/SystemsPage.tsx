// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { useState } from 'react'
import { Link } from 'react-router'
import { Plus } from 'lucide-react'
import CreateSystemDialog from '@/components/CreateSystemDialog'
import GitStatus from '@/components/GitStatus'
import Orientation from '@/components/Orientation'
import { Page, StatePill } from '@/components/common'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useAlerts, useSystems, type Incident, type SystemSummary } from '@/lib/api'
import { useT } from '@/i18n'
import { channelNames, systemStateText } from '@/lib/format'
import { systemIncidents } from '@/lib/incidents'
import { stackBadge, stackName } from '@/lib/stacks'

function SystemCard({ s, stacks, incidents }: { s: SystemSummary; stacks: number; incidents: Incident[] }) {
  const { t, tn } = useT()
  const set = stackBadge(stacks, stackName(s.stack, s.stackName))
  return (
    <Link
      to={`/systems/${encodeURIComponent(s.id)}`}
      className="group flex flex-col gap-3 rounded-xl border bg-card p-4 transition-colors hover:border-foreground/20 hover:bg-muted/30 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate text-base font-semibold">{s.name}</div>
          <div className="truncate text-sm text-muted-foreground">{s.description || (s.environment ? t('systems.environment', { env: s.environment }) : ' ')}</div>
        </div>
        {s.draft ? (
          <span className="shrink-0 rounded-full bg-warn/10 px-2 py-0.5 text-xs font-medium text-warn" data-testid="system-draft">
            {t('systems.draft')}
          </span>
        ) : (
          <StatePill state={s.state}>{systemStateText(s, incidents)}</StatePill>
        )}
      </div>
      {s.valid ? (
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm text-muted-foreground">
          <span>
            <span className="font-medium text-foreground tabular-nums">{s.services}</span> {tn('word.services', s.services)}
          </span>
          <span>
            <span className="font-medium text-foreground tabular-nums">{s.rules}</span> {tn('word.rules', s.rules)}
          </span>
          <span>{s.receivers === 0 ? <span className="text-warn">{t('systems.noReceivers')}</span> : tn('count.channels', s.receivers)}</span>
          {s.git && (
            <span data-testid="system-git" title={`${s.git.repo} · ${s.git.path}`}>
              {t('systems.fromGit', { commit: s.git.commit.slice(0, 8) })}
            </span>
          )}
          {s.channelsOff && s.channelsOff.length > 0 && (
            <span className="text-warn" data-testid="system-channels-off">
              {t('systems.channelsOff', { channels: channelNames(s.channelsOff) })}
            </span>
          )}
          {set && (
            <span data-testid="system-stack" title={t('systems.stackTitle')}>
              {t('systems.stack', { name: set })}
            </span>
          )}
          {s.stackUnknown && <span className="text-warn">{t('systems.stackUnknown', { name: s.stackUnknown })}</span>}
        </div>
      ) : (
        <p className="line-clamp-2 text-sm text-firing">{t('systems.invalid', { problem: s.problem || t('systems.invalidDefault') })}</p>
      )}
    </Link>
  )
}

export default function SystemsPage() {
  const systems = useSystems()
  // The shell's alert counter keeps this query alive, so reading it here sends no extra request.
  const alerts = useAlerts()
  const { t } = useT()
  const [creating, setCreating] = useState(false)
  const list = systems.data?.systems ?? []
  return (
    <Page
      title={t('systems.title')}
      description={t('systems.description')}
      actions={
        <Button onClick={() => setCreating(true)} disabled={systems.data && !systems.data.writable}>
          <Plus /> {t('systems.add')}
        </Button>
      }
    >
      <Orientation onAddSystem={() => setCreating(true)} />
      {systems.data?.git && <GitStatus git={systems.data.git} />}
      {systems.isLoading ? (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
        </div>
      ) : systems.isError ? (
        <Alert variant="destructive">
          <AlertTitle>{t('systems.loadError')}</AlertTitle>
          <AlertDescription>{systems.error.message}</AlertDescription>
        </Alert>
      ) : list.length === 0 ? (
        <div className="rounded-xl border border-dashed p-8 text-center">
          <p className="text-base font-medium">{t('systems.empty')}</p>
          <p className="mt-1 text-sm text-muted-foreground">{t('systems.emptyHint')}</p>
          {systems.data?.writable ? (
            <Button className="mt-4" onClick={() => setCreating(true)}>
              <Plus /> {t('systems.add')}
            </Button>
          ) : (
            <p className="mt-3 text-sm text-muted-foreground">{t('systems.readOnly')}</p>
          )}
        </div>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {list.map((s) => (
            <SystemCard key={s.id} s={s} stacks={systems.data?.stacks ?? 1} incidents={systemIncidents(alerts.data, s.id)} />
          ))}
        </div>
      )}
      <CreateSystemDialog open={creating} onOpenChange={setCreating} />
    </Page>
  )
}
