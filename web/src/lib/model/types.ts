// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// TypeScript view of the system description (api/v1alpha1). The core
// validates the full document; these types only cover what the UI edits.

import { t } from '@/i18n'

export type ServiceType = 'http' | 'grpc' | 'queue' | 'database'
export type Severity = 'critical' | 'warning'

/** Notification text written by the team (see api/v1alpha1 NotificationMessage). */
export interface NotificationMessage {
  title?: string
  text?: string
  action?: string
}

export interface ErrorRatePolicy {
  message?: NotificationMessage
  enabled?: boolean
  threshold?: number
  minRequestsPerSecond?: number
  for?: string
  perOperation?: boolean
  severity?: Severity
}

export interface LatencyPolicy {
  message?: NotificationMessage
  enabled?: boolean
  quantile?: number
  thresholdMs?: number
  minRequestsPerSecond?: number
  for?: string
  perOperation?: boolean
  /** "Slower than normal": factor over the normal quantile; 0 turns it off. */
  aboveNormalFactor?: number
  severity?: Severity
}

export interface SaturationPolicy {
  enabled?: boolean
  memory?: string
  cpuCores?: number
  /** Share of the memory limit (0..1) when memory is not set; 0 turns it off. */
  memoryLimitShare?: number
  /** Share of the CPU limit (0..1) when cpuCores is not set; 0 turns it off. */
  cpuLimitShare?: number
  /** CPU above normal: factor; 0 turns it off. */
  cpuAboveNormalFactor?: number
  oomKilledEnabled?: boolean
  maxRestartsPerHour?: number
  restartsEnabled?: boolean
  for?: string
  severity?: Severity
  memoryMessage?: NotificationMessage
  cpuMessage?: NotificationMessage
  restartsMessage?: NotificationMessage
  oomKilledMessage?: NotificationMessage
}

/** "Requests stopped arriving" (api/v1alpha1 TrafficDropPolicy). */
export interface TrafficDropPolicy {
  enabled?: boolean
  /** Share of normal traffic, 0..1. */
  threshold?: number
  minRequestsPerSecond?: number
  for?: string
  severity?: Severity
  message?: NotificationMessage
}

/** "No data from the service" (api/v1alpha1 NoDataPolicy). */
export interface NoDataPolicy {
  enabled?: boolean
  /** How long the service may send no span metrics; the alert fires 1 minute later. */
  after?: string
  severity?: Severity
  message?: NotificationMessage
}

/** "Kamerton cannot see the system" (api/v1alpha1 SystemNoDataPolicy); always critical. */
export interface SystemNoDataPolicy {
  enabled?: boolean
  after?: string
  message?: NotificationMessage
}

/** "No ready pods" (api/v1alpha1 NoReadyPodsPolicy). */
export interface NoReadyPodsPolicy {
  enabled?: boolean
  for?: string
  severity?: Severity
  message?: NotificationMessage
}

export interface Operation {
  name: string
  errorRate?: ErrorRatePolicy
  latency?: LatencyPolicy
}

export interface QueueSpec {
  topic: string
  consumerGroups?: string[]
  maxLag: number
  for?: string
  severity?: Severity
  message?: NotificationMessage
  /** "Nobody reads the queue": on by default; false turns it off. */
  consumerStoppedEnabled?: boolean
  consumerStoppedMessage?: NotificationMessage
}

export interface SLO {
  objective: number
  window?: string
  message?: NotificationMessage
}

export type CustomOperator = 'gt' | 'gte' | 'lt' | 'lte' | 'eq' | 'ne'
export type CustomFunction = 'none' | 'rate' | 'increase'
export type CustomAggregation = 'max' | 'min' | 'avg' | 'sum' | 'count'

export interface CustomCondition {
  metric: string
  labels?: Record<string, string>
  function?: CustomFunction
  aggregation?: CustomAggregation
  by?: string[]
  operator: CustomOperator
  threshold: number
}

export interface CustomRule {
  name: string
  title: string
  enabled?: boolean
  condition?: CustomCondition
  expr?: string
  for?: string
  severity?: Severity
  unit?: string
  message?: NotificationMessage
}

export interface Service {
  name: string
  type: ServiceType
  critical?: boolean
  description?: string
  owner?: string
  telemetryName?: string
  dependsOn?: string[]
  workload?: { namespace?: string; podRegex?: string }
  errorRate?: ErrorRatePolicy
  latency?: LatencyPolicy
  saturation?: SaturationPolicy
  trafficDrop?: TrafficDropPolicy
  noData?: NoDataPolicy
  noReadyPods?: NoReadyPodsPolicy
  operations?: Operation[]
  excludeOperations?: string[]
  queue?: QueueSpec
  slo?: SLO
  runbookURL?: string
  customRules?: CustomRule[]
}

export interface SecretKeyRef {
  name: string
  key: string
}

export interface Receiver {
  name: string
  telegram?: { chatID: number; botTokenSecret: SecretKeyRef }
  email?: { to: string[]; from: string; smarthost: string; authUsername?: string; authPasswordSecret?: SecretKeyRef }
}

export interface Route {
  receiver: string
  severities?: Severity[]
  services: string[]
}

export interface Routing {
  critical?: string[]
  warning?: string[]
  groupBy?: string[]
  repeatInterval?: string
  routes?: Route[]
}

export interface Defaults {
  namespace?: string
  rateWindow?: string
  excludeOperations?: string[]
  errorRate?: ErrorRatePolicy
  latency?: LatencyPolicy
  saturation?: SaturationPolicy
  trafficDrop?: TrafficDropPolicy
  noData?: NoDataPolicy
  noReadyPods?: NoReadyPodsPolicy
}

export interface Description {
  apiVersion: string
  kind: string
  metadata: { name: string; namespace?: string; labels?: Record<string, string>; annotations?: Record<string, string> }
  spec: {
    environment?: string
    /** Set of programs the system uses; empty = the built-in set. */
    stack?: string
    telemetry?: { spans?: { serviceLabel?: string; callsMetric?: string } & Record<string, unknown>; containers?: { readyMetric?: string } & Record<string, unknown> } & Record<
      string,
      unknown
    >
    defaults?: Defaults
    services: Service[]
    notifications?: { receivers?: Receiver[]; routing?: Routing; language?: string; timeZone?: string }
    noData?: SystemNoDataPolicy
  }
}

export const SERVICE_TYPES: ServiceType[] = ['http', 'grpc', 'queue', 'database']

export const serviceTypeLabel = (type: ServiceType | string): string =>
  (SERVICE_TYPES as string[]).includes(type) ? t(`service.type.${type as ServiceType}` as const) : type

export const severityLabel = (s: Severity): string => t(`severity.${s}` as const)

export const NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

/** Deep copy for immutable edits. */
export function clone<T>(v: T): T {
  return structuredClone(v)
}
