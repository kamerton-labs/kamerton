// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Link2, Link2Off, ShieldAlert } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { useT } from '@/i18n'
import { useSettings } from '@/lib/api'

/** Shown while login is disabled on the core; screens that handle credentials say so. */
export function NoAuthWarning() {
  const settings = useSettings()
  const { t, rich } = useT()
  // Shown only once the core has answered, so it does not flash while loading.
  if (!settings.data || settings.data.auth?.enabled) return null
  return (
    <Alert variant="destructive">
      <ShieldAlert />
      <AlertTitle>{t('notice.noAuth.title')}</AlertTitle>
      <AlertDescription>
        {rich('notice.noAuth.text', {
          user: <code>KAMERTON_AUTH_USER</code>,
          password: <code>KAMERTON_AUTH_PASSWORD</code>,
          env: <code>.env</code>,
          auth: <code>auth</code>,
          pf: <code>kubectl port-forward</code>,
        })}
      </AlertDescription>
    </Alert>
  )
}

/** Whether notifications carry links, which depends on the external URL. */
export function LinksNotice() {
  const settings = useSettings()
  const { t, rich } = useT()
  if (!settings.data) return null
  if (settings.data.linksEnabled) {
    return (
      <Alert>
        <Link2 />
        <AlertTitle>{t('notice.links.onTitle')}</AlertTitle>
        <AlertDescription>{rich('notice.links.onText', { url: <code>{settings.data.externalURL}</code> })}</AlertDescription>
      </Alert>
    )
  }
  return (
    <Alert>
      <Link2Off />
      <AlertTitle>{t('notice.links.offTitle')}</AlertTitle>
      <AlertDescription>
        {rich('notice.links.offText', { value: <code>externalURL</code>, example: <code>http://localhost:8080</code>, flag: <code>--external-url</code> })}
      </AlertDescription>
    </Alert>
  )
}
