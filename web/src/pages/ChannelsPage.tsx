// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Notification channels, shared by all systems: one card per channel. What
// each system sends where is chosen inside the system.

import { Page } from '@/components/common'
import EmailSetup from '@/components/notifications/EmailSetup'
import { LinksNotice, NoAuthWarning } from '@/components/notifications/Notices'
import TelegramSetup from '@/components/notifications/TelegramSetup'
import { useT } from '@/i18n'

export default function ChannelsPage() {
  const { t } = useT()
  return (
    <Page title={t('channels.title')} description={t('channels.description')}>
      <div className="space-y-5">
        <TelegramSetup />
        <EmailSetup />
      </div>
      <LinksNotice />
      <NoAuthWarning />
    </Page>
  )
}
