// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

import { Database, Globe, Inbox, Network, type LucideIcon } from 'lucide-react'
import { t } from '@/i18n'
import { SERVICE_TYPES, type ServiceType } from '@/lib/model/types'

export const typeIcon: Record<ServiceType, LucideIcon> = { http: Globe, grpc: Network, queue: Inbox, database: Database }
export const typeShort = (type: ServiceType | string): string =>
  (SERVICE_TYPES as string[]).includes(type) ? t(`canvas.typeShort.${type as ServiceType}` as const) : type
export const typeHint = (type: ServiceType): string => t(`canvas.typeHint.${type}` as const)

/** MIME type of palette items dragged onto the canvas. */
export const DND_TYPE = 'application/x-kamerton-service'
