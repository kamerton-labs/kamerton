// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Page } from '@/components/common'
import EmailSetup from '@/components/notifications/EmailSetup'
import { ChannelsOffNotice, LinksNotice, NoAuthWarning } from '@/components/notifications/Notices'
import TelegramSetup from '@/components/notifications/TelegramSetup'
import { useT } from '@/i18n'

export default function ChannelsPage() {
  const { t } = useT()
  return (
    <Page title={t('channels.title')} description={t('channels.description')}>
      <div className="space-y-5">
        <ChannelsOffNotice />
        <TelegramSetup />
        <EmailSetup />
      </div>
      <LinksNotice />
      <NoAuthWarning />
    </Page>
  )
}
