// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Typed client for the core API (/api/v1). When a backend dependency is not
// configured the API says so and the UI shows an empty state.

import { keepPreviousData, useMutation, useQuery, useQueryClient, type QueryClient, type QueryKey } from '@tanstack/react-query'
import { CSRF_HEADER, CSRF_VALUE, fetchSession, notifyUnauthorized } from '@/lib/auth'

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  // X-Requested-With is required by the core on every state-changing request, with or without login.
  const headers = new Headers(init?.headers)
  headers.set(CSRF_HEADER, CSRF_VALUE)
  const res = await fetch(`/api/v1${path}`, { ...init, headers, credentials: 'same-origin' })
  const text = await res.text()
  let body: unknown = undefined
  try {
    body = text ? JSON.parse(text) : undefined
  } catch {
    // Not JSON (e.g. a plain-text error from a proxy): keep the status text.
  }
  if (!res.ok) {
    const err = (body ?? {}) as { error?: string; code?: string }
    if (res.status === 401 && err.code === 'unauthorized') notifyUnauthorized()
    throw new ApiError(res.status, err.code ?? 'http_error', err.error ?? res.statusText)
  }
  return body as T
}

const postYAML = <T>(path: string, yaml: string) =>
  request<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/yaml' }, body: yaml })

const postJSON = <T>(path: string, body: unknown) =>
  request<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })

// Types below mirror internal/server.

export type JSONSchema = Record<string, unknown>

export interface VersionInfo {
  version: string
  commit: string
  apiVersion: string
}

/** Where a change of a system kept in git went instead of the cluster. */
export interface GitProposal {
  branch: string
  /** Committed to the followed branch: Kamerton applies it with the next read. Otherwise it waits for a merge. */
  direct: boolean
  /** Pull or merge request, when the hosting service is known. */
  url?: string
  commit: string
}

export interface SaveDescriptionResult {
  /** 202: the change of a system kept in git is saved and waits for a push. */
  staged?: boolean
  saved?: boolean
  source?: string
  mode?: string
  warnings?: Issue[]
  /** 202: the change was written to the repository, not to the cluster. */
  proposed?: boolean
  proposal?: GitProposal
}

export interface SystemSummary {
  id: string
  name: string
  namespace?: string
  source: string
  environment?: string
  description?: string
  services: number
  rules: number
  receivers: number
  /** Made here and not pushed yet: it runs nowhere until its file is on the followed branch. */
  draft?: boolean
  /** Set for a system that comes from a git repository: it is changed there, not in the UI. */
  git?: GitSource
  /** Receivers whose Secret is missing: nothing reaches their channel yet. */
  channelsOff?: { receiver: string; channel: 'telegram' | 'email' }[]
  valid: boolean
  problem?: string
  alerts: { critical: number; warning: number } | null
  state: 'ok' | 'warning' | 'firing' | 'invalid' | 'unknown'
  /** Set of programs the system uses (an unknown set resolves to the built-in one). */
  stack: string
  stackName: string
  /** The set the description asks for when no such set exists. */
  stackUnknown?: string
}

/** Where a system's file is: repo is "https://host/team/repo.git#branch". */
export interface GitSource {
  repo: string
  path: string
  commit: string
}

/** gitsync.Status: the last look at the repository. */
export interface GitStatus {
  repo: string
  branch: string
  path?: string
  commit?: string
  checkedAt?: string
  syncedAt?: string
  error?: string
  write?: 'pullRequest' | 'commit'
  files: { path: string; system?: string; state: 'applied' | 'unchanged' | 'invalid' | 'conflict' | 'failed'; error?: string }[]
  removed?: string[]
  /** Name from the project file (kamerton.yaml); hasProjectFile is false for a repository laid out by the installation's settings. */
  project?: string
  hasProjectFile?: boolean
  systemsPath?: string
  generatedPath?: string
  /** The repository has no commits yet. */
  empty?: boolean
  /** Open pull or merge requests Kamerton made, when the hosting service is known. */
  requests?: GitRequest[]
  repoURL?: string
  commitURL?: string
}

export interface GitRequest {
  system: string
  title: string
  url: string
  branch: string
}

/** The connection to the repository as the UI edits it; the token is write-only. */
export interface GitSettings {
  url: string
  branch?: string
  path?: string
  write?: '' | 'pullRequest' | 'commit'
  provider?: string
  apiURL?: string
  commitStatus?: boolean
}

export interface GitSettingsView extends GitSettings {
  /** False where a repository cannot be connected from the UI (no cluster). */
  available: boolean
  /** Set by the installation: shown, not editable. */
  locked: boolean
  connected: boolean
  hasToken: boolean
}

export interface GitCheck {
  ok: boolean
  /** What is wrong when ok is false: address, no_host, unreachable, timeout, tls, auth, not_found, no_branch. */
  problem?: string
  empty?: boolean
  error?: string
  files: number
  /** The folder holds a project file; without one a project can be created there. */
  hasProject?: boolean
  project?: string
  /** A token to push with is there (typed or stored), or the address needs none. */
  canPush?: boolean
}

const gitInvalidate = (qc: ReturnType<typeof useQueryClient>) => {
  // A repository connected or disconnected can make or end the project, too.
  void qc.invalidateQueries({ queryKey: ['project'] })
  void qc.invalidateQueries({ queryKey: ['git'] })
  void qc.invalidateQueries({ queryKey: ['systems'] })
  void qc.invalidateQueries({ queryKey: ['system'] })
}

/** The project: what the first screen and the sidebar show. Mirrors internal/project.Info. */
export interface ProjectInfo {
  /** A project is open; without one only the first screen is shown. */
  exists: boolean
  name?: string
  where: 'local' | 'cluster' | ''
  /** The project's folder on this machine (local). */
  path?: string
  /** Where the systems are kept (cluster). */
  namespace?: string
  /** Where new projects go unless another folder is named (local). */
  defaultRoot?: string
  recent?: { name: string; path: string }[]
  canClone: boolean
  /** The folder is a git working copy (local). */
  git?: boolean
}

export interface CloneRequest {
  url: string
  branch?: string
  /** The project's folder inside the repository; empty: its root. */
  folder?: string
  token?: string
  /** Where the clone goes (local). */
  path?: string
  /** Name of the project when the repository has none yet. */
  name?: string
}

export const useProject = () => useQuery({ queryKey: ['project'], queryFn: () => request<ProjectInfo>('/project') })

// Another project means other systems, sets and repository: everything is read again.
const projectChanged = (qc: ReturnType<typeof useQueryClient>) => void qc.invalidateQueries()

export const useNewProject = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { name: string; path?: string; git?: boolean }) => postJSON<ProjectInfo>('/project', v),
    onSuccess: () => projectChanged(qc),
  })
}

export const useOpenProject = () => {
  const qc = useQueryClient()
  return useMutation({ mutationFn: (path: string) => postJSON<ProjectInfo>('/project/open', { path }), onSuccess: () => projectChanged(qc) })
}

export const useCloneProject = () => {
  const qc = useQueryClient()
  return useMutation({ mutationFn: (v: CloneRequest) => postJSON<ProjectInfo>('/project/clone', v), onSuccess: () => projectChanged(qc) })
}

export const useCloseProject = () => {
  const qc = useQueryClient()
  return useMutation({ mutationFn: () => postJSON<ProjectInfo>('/project/close', {}), onSuccess: () => projectChanged(qc) })
}

/** Reads a repository before a project is taken from it; nothing is stored. */
export const checkProjectRepo = (v: CloneRequest) => postJSON<GitCheck>('/project/check', v)

export const useGitSettings = () => useQuery({ queryKey: ['git', 'settings'], queryFn: () => request<GitSettingsView>('/git/settings') })

export const testGitSettings = (v: GitSettings & { token?: string }) => postJSON<GitCheck>('/git/settings/test', v)

export const useSaveGitSettings = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: GitSettings & { token?: string; create?: boolean; name?: string }) =>
      request<{ settings: GitSettingsView; proposal?: GitProposal }>('/git/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(v) }),
    onSuccess: () => gitInvalidate(qc),
  })
}

export const useDisconnectGit = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => request<GitSettingsView>('/git/settings', { method: 'DELETE' }),
    onSuccess: () => gitInvalidate(qc),
  })
}

export type GitChangeKind = 'change' | 'new' | 'delete' | 'move'

/** A change saved in the UI and not pushed, with what it does to the rules. */
export interface GitChange {
  system: string
  kind: GitChangeKind
  path?: string
  savedAt: string
  author?: string
  diff?: GitPreview['diff']
  error?: string
  /** The change of the description file, line by line. */
  text?: DiffLine[]
}

/** One line of a text diff: " " same, "+" added, "-" removed, "@" lines left out. */
export interface DiffLine {
  op: ' ' | '+' | '-' | '@'
  text?: string
}

/** One description file of a commit: what it does to the rules and to the text. */
export interface GitCommitFile {
  path: string
  system?: string
  kind: GitChangeKind
  diff?: GitPreview['diff']
  text?: DiffLine[]
}

export interface GitCommitDetail {
  commit: GitCommit
  message?: string
  files: GitCommitFile[]
  /** Changed files that are not descriptions. */
  others: number
}

export const useGitCommit = (hash: string | null) =>
  useQuery({ queryKey: ['git', 'commit', hash], queryFn: () => request<GitCommitDetail>(`/git/commits/${hash}`), enabled: !!hash, staleTime: Infinity })

export interface GitCommit {
  hash: string
  title: string
  author: string
  when: string
  url?: string
  systems?: string[]
  applied?: boolean
}

export const useGitChanges = (enabled: boolean) =>
  useQuery({ queryKey: ['git', 'changes'], queryFn: () => request<{ changes: GitChange[] }>('/git/changes'), enabled })

export const useGitCommits = (enabled: boolean, limit: number) =>
  useQuery({
    queryKey: ['git', 'commits', limit],
    queryFn: () => request<{ commits: GitCommit[]; more: boolean }>(`/git/commits?limit=${limit}`),
    enabled,
    staleTime: 30_000,
    placeholderData: (prev) => prev,
  })

export const useGitPush = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { message: string; systems?: string[] }) => postJSON<{ proposal: GitProposal }>('/git/push', v),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['git'] })
      void qc.invalidateQueries({ queryKey: ['systems'] })
      void qc.invalidateQueries({ queryKey: ['system'] })
    },
  })
}

export const useGitDiscard = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (system: string) => request<{ discarded: boolean }>(`/git/changes/${encodeURIComponent(system)}`, { method: 'DELETE' }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['git'] })
      void qc.invalidateQueries({ queryKey: ['systems'] })
      void qc.invalidateQueries({ queryKey: ['system'] })
    },
  })
}

/** What a description would change if written to the repository (POST /git/preview). */
export interface GitPreview {
  system: string
  mode: 'pullRequest' | 'commit'
  branch: string
  repo: string
  repoURL?: string
  file: string
  new: boolean
  /** A system made by hand that would move to git as it is. */
  move?: boolean
  diff: { total: number; new: boolean; changed: RuleLine[]; added: RuleLine[]; removed: RuleLine[] }
}

export interface RuleLine {
  rule: string
  what?: string
}

/** A change that may go to git: the commit message a person wrote travels in a header. */
export interface GitNote {
  message?: string
}

const noteHeaders = (note?: GitNote): Record<string, string> => (note?.message?.trim() ? { 'X-Kamerton-Message': encodeURIComponent(note.message.trim()) } : {})

export const gitPreview = (yaml: string) => postYAML<GitPreview>('/git/preview', yaml)

export const useGitSync = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => request<GitStatus>('/git/sync', { method: 'POST' }),
    onSettled: () => qc.invalidateQueries({ queryKey: ['systems'] }),
  })
}

export interface SystemsResponse {
  mode: 'cluster' | 'file' | 'none'
  writable: boolean
  /** Changes saved in the UI and not pushed yet. */
  gitChanges?: { system: string; kind: GitChangeKind }[]
  /** Present when system descriptions are read from a git repository. */
  git?: GitStatus
  systems: SystemSummary[]
  alertsConfigured: boolean
  alertsError?: string
  starter: string
  stacks: number
}

export interface SystemResponse {
  id: string
  name: string
  yaml: string
  source: string
  mode: 'cluster' | 'file' | 'none'
  writable: boolean
  git?: GitSource
  /** How a save of a system from git is written to the repository; absent when it is read-only. */
  gitWrite?: 'pullRequest' | 'commit'
  /** The file on the hosting service, when it is known. */
  gitFileURL?: string
  /** Made here and not pushed yet. */
  draft?: boolean
  /** The kind of this system's saved, not pushed change; the yaml above is then that saved version. */
  gitSaved?: GitChangeKind
  valid: boolean
  errors: Issue[]
  warnings: Issue[]
  stack?: { id: string; name: string; unknown?: string; grafanaURL?: string }
}

export interface RuleStatesResponse {
  configured: boolean
  reachable: boolean
  message?: string
  states: { alert: string; service: string; operation?: string; rule?: string; state: string; severity: string }[]
}

export interface RuleBacktestRequest {
  yaml: string
  service: string
  alert: string
  operation?: string
  range: string
  step?: string
}

export interface RuleBacktestResult extends BacktestResult {
  rule: { expr: string; for: string; title: string; threshold: string }
}

export type NoiseWindow = '1d' | '3d' | '7d'

export type NoiseVerdict = 'useful' | 'noise'

/** Mirrors noise.Suggestion (internal/noise/noise.go): values are in the form of the description field at path, see format. */
export interface NoiseSuggestion {
  field?: string
  current?: string
  suggested?: string
  unit?: string
  wouldFire?: number
  wouldMiss?: number
  wouldShort?: number
  /** How much later a real failure would be reported; 0 for a threshold. */
  delaySeconds?: number
  /** Relative to the service, e.g. "operations[PlaceOrder].latency.thresholdMs" (internal/generator/tunable.go). */
  path?: string
  /** share | number | integer | quantity | duration. */
  format?: string
}

/** Rules that never fired in the window are absent. */
export interface NoiseRule {
  alert?: string
  service?: string
  operation?: string
  fired?: number
  short?: number
  /** Firings muted while a dependency failed: nobody received them, and they are not in fired or short. */
  consequences?: number
  longestSeconds?: number
  lastFiredAt?: string
  feedback?: { useful?: number; noise?: number }
  noisy?: boolean
  suggestion?: NoiseSuggestion
}

export interface NoiseResponse {
  window?: string
  available?: boolean
  reason?: string
  shortUnder?: string
  longFrom?: string
  rules?: NoiseRule[]
}

/** operation only for a rule of one operation; the aggregated per-operation rules are keyed with "". */
export interface NoiseFeedback {
  alert: string
  service: string
  operation?: string
  verdict: NoiseVerdict
}

export interface NoiseFeedbackResult {
  recorded?: boolean
  alert?: string
  service?: string
  operation?: string
  feedback?: { useful?: number; noise?: number }
}

export interface Settings {
  externalURL: string
  linksEnabled: boolean
  mode: 'cluster' | 'file' | 'none'
  namespace: string
  telegram: { available: boolean; reason: string }
  email?: { available: boolean; reason: string }
  auth: { enabled: boolean }
}

export interface TelegramBot {
  id: number
  username: string
  firstName: string
  checkedAt: string
}

export type Severity = 'critical' | 'warning'

export interface TelegramChat {
  id: number
  title: string
  type: '' | 'private' | 'group' | 'supergroup' | 'channel'
  status: string
  available: boolean
  problem?: string
  migratedFrom?: number
  updatedAt: string
  source: 'update' | 'check' | 'description'
  verified: boolean
  receiver?: string
  routing: { severities: Severity[]; services: string[]; custom?: boolean }
  usedIn: { system: string; severities: Severity[]; services: string[]; custom?: boolean }[]
}

export interface TelegramStatus {
  token: {
    configured: boolean
    bot?: TelegramBot
    problem?: string
    secretName: string
    secretKey: string
    namespace: string
  }
  polling: {
    poller?: string
    lastPollAt?: string
    lastError?: string
    errorAt?: string
    conflict?: boolean
    active: boolean
  }
  chats: TelegramChat[]
  description: {
    loaded: boolean
    mode: string
    source?: string
    name?: string
    namespace?: string
    services: string[]
    writable: boolean
    problem?: string
  }
}

export type TelegramResponse = { available: false; reason: string } | { available: true; status: TelegramStatus }

export interface TelegramTestResult {
  ok: boolean
  error?: string
  migratedTo?: number
}

export interface ChannelUse {
  system: string
  severities: Severity[]
  services: string[]
  custom?: boolean
}

export interface EmailRecipient {
  id: string
  name: string
  to: string[]
  createdAt: string
  /** Receiver name to use in a description. */
  receiver: string
  usedIn: ChannelUse[]
}

export interface EmailServer {
  configured: boolean
  smarthost?: string
  from?: string
  username?: string
  hasPassword: boolean
  problem?: string
  secretName: string
  secretKey: string
  namespace: string
}

export interface EmailStatus {
  server: EmailServer
  recipients: EmailRecipient[]
}

export type EmailResponse = { available: false; reason: string } | { available: true; status: EmailStatus }

export interface EmailServerInput {
  smarthost: string
  from: string
  username: string
  /** Empty keeps the stored password when the login is unchanged. */
  password: string
}

export interface RunbookAlert {
  alert: string
  severity: string
  signal: string
  operation?: string
  title: string
  summary: string
  threshold?: string
  for?: string
  expr: string
}

export interface Runbook {
  system: string
  systemID: string
  service: {
    name: string
    /** The runbook of the alerts about the whole system (name "_system"); it has no type or criticality. */
    system?: boolean
    type?: string
    critical?: boolean
    description?: string
    owner?: string
    dependsOn?: string[]
    calledBy?: string[]
    runbookURL?: string
  }
  alerts: RunbookAlert[]
  markdown: string
  /** Notes of earlier incidents of the service, most recent first. */
  experience?: RunbookExperience[]
}

export interface RunbookExperience {
  id?: string
  startedAt?: string
  durationSeconds?: number
  cause?: string
  fix?: string
}

export interface Issue {
  path: string
  message: string
  severity: 'error' | 'warning'
}

export interface ValidationResult {
  valid: boolean
  errors: Issue[]
  warnings: Issue[]
}

export interface DiscoveredService {
  name: string
  requestsPerSecond: number
  errorRatio: number
  described: boolean
  /** Other systems that describe the service (system-scoped discovery only). */
  inSystems?: string[]
}

export interface DiscoveryResponse {
  configured: boolean
  services: DiscoveredService[]
  message?: string
}

export type ProgramKind = 'prometheus' | 'alertmanager' | 'grafana' | 'loki' | 'tempo' | 'otel-collector'

/** bundled: the address installed with Kamerton; own: any other; none: not connected. */
export type ProgramOrigin = 'bundled' | 'own' | 'none'

export type CheckState = 'ok' | 'not_ready' | 'no_answer' | 'wrong_program' | 'no_access' | 'bad_url'

export interface StackComponent {
  name: ProgramKind
  url?: string
  configured: boolean
  external: boolean
  origin: ProgramOrigin
  healthy: boolean | null
  state?: CheckState
  message?: string
  version?: string
  error?: string
  latencyMs?: number
}

export interface StackProgram {
  kind: ProgramKind
  url: string
  origin: ProgramOrigin
  /** Install-time address (built-in set only). */
  flagURL?: string
  overridden?: boolean
}

export interface StackSet {
  id: string
  name: string
  builtin: boolean
  programs: StackProgram[]
  ruleLabels: Record<string, string>
  alertmanagerConfigLabels: Record<string, string>
  health: StackComponent[]
  usedBy: string[]
  warnings: string[]
}

export interface StacksResponse {
  sets: StackSet[]
  discovery: { available: boolean; reason?: string }
  warnings: string[]
}

export interface CheckResult {
  kind: ProgramKind
  url: string
  state: CheckState
  identified: boolean
  healthy: boolean
  version?: string
  message: string
  error?: string
  latencyMs: number
}

export interface OperatorInstance {
  kind: 'Prometheus' | 'Alertmanager'
  namespace: string
  name: string
  labels: Record<string, string>
  selectsAll: boolean
  selectsNone: boolean
  hasExpressions: boolean
  namespaces: string
}

export interface FoundProgram {
  kind: ProgramKind
  namespace: string
  service: string
  url: string
  version?: string
  healthy: boolean
  state: CheckState
  message: string
  operator?: OperatorInstance
  usedBy: string[]
}

export type StackDiscovery =
  | { available: false; reason: string }
  | {
      available: true
      found: FoundProgram[]
      operator: { available: boolean; reason?: string; prometheuses: OperatorInstance[]; alertmanagers: OperatorInstance[] }
      warnings: string[]
    }

export interface StackInput {
  id?: string
  name?: string
  urls?: Partial<Record<ProgramKind, string>>
  ruleLabels?: Record<string, string>
  alertmanagerConfigLabels?: Record<string, string>
}

export interface TriageFinding {
  alerting: string
  probableSource: string
  chain: string[]
}

export interface ActiveAlert {
  name: string
  system: string
  service: string
  severity: string
  startsAt: string
  summary: string
  title?: string
  value?: string
  threshold?: string
  labels: Record<string, string>
  triage?: TriageFinding
  fingerprint?: string
  muted?: boolean
  /** ID of the incident the alert's service belongs to. */
  incident?: string
  /** Kamerton's or the team's text and first steps, {since} filled in. */
  text?: string
  action?: string
}

/** triage.Alert in internal/triage/incidents.go. */
export interface IncidentAlert {
  fingerprint?: string
  name: string
  service: string
  severity: string
  startsAt: string
  title?: string
  value?: string
  muted: boolean
}

/** What the core found out about the cause; strings are in the system's notification language. */
export interface IncidentEvidence {
  collectedAt?: string
  cause?: string
  likely?: boolean
  changes?: string[]
  operation?: string
  operationFailures?: number
  logLine?: string
  tracesURL?: string
  /** Services of earlier incidents whose alerts this cause explains. */
  explains?: string[]
}

/** triage.Incident in internal/triage/incidents.go. */
export interface Incident {
  id: string
  system: string
  roots: string[]
  rootAlerts: IncidentAlert[]
  affected: string[]
  affectedAlerts: IncidentAlert[]
  startsAt: string
  evidence?: IncidentEvidence
  /** The last recorded occurrence of the same failure. */
  previous?: PreviousIncident
  /** ID of this incident's record in the history, for a note while it is fresh. */
  historyId?: string
  /** Set when these alerts are the tail of an incident that already ended, not a new failure. */
  aftermath?: { roots: string[]; endedAt?: string }
}

export interface PreviousIncident {
  id?: string
  startedAt?: string
  durationSeconds?: number
  cause?: string
  fix?: string
  /** Earlier closed incidents like this one in the last 30 days, the previous one included and the live one not (internal/history Recall). */
  count30d?: number
  /** The cause or fix was written by a person; otherwise cause is what Kamerton collected. */
  noted?: boolean
}

export interface IncidentNote {
  cause?: string
  fix?: string
  updatedAt?: string
  source?: 'ui' | 'telegram' | string
}

export interface HistoryAlert {
  name?: string
  service?: string
  title?: string
  severity?: string
}

/** history.View (internal/history/history.go); system is the system's name. Every field may be absent. */
export interface HistoryIncident {
  id?: string
  system?: string
  roots?: string[]
  rootAlerts?: HistoryAlert[]
  affected?: string[]
  startedAt?: string
  endedAt?: string
  durationSeconds?: number
  /** The end is the last time the incident was seen, not a resolve. */
  endEstimated?: boolean
  open?: boolean
  evidence?: IncidentEvidence
  note?: IncidentNote
}

export interface HistoryResponse {
  incidents?: HistoryIncident[]
}

export interface NoteInput {
  cause: string
  fix: string
}

export interface AlertsResponse {
  configured: boolean
  alerts: ActiveAlert[]
  /** Alerts Alertmanager inhibits because their cause fires. */
  muted?: ActiveAlert[]
  incidents?: Incident[]
  message?: string
}

export interface BacktestEpisode {
  labels: Record<string, string>
  firingAt: string
  resolvedAt: string
  ongoing: boolean
}

export interface BacktestResult {
  firings: number
  series: number
  episodes: BacktestEpisode[]
  start: string
  end: string
  step: string
}

/** Whether login is enabled and the browser has a session. */
export const useSession = () => useQuery({ queryKey: ['session'], queryFn: fetchSession, staleTime: Infinity, retry: false })

export const useVersion = () => useQuery({ queryKey: ['version'], queryFn: () => request<VersionInfo>('/version') })

export const useSchema = () =>
  useQuery({ queryKey: ['schema'], queryFn: () => request<JSONSchema>('/schema'), staleTime: Infinity })

/** Sets of programs with the health of each program and the systems using each set. */
export const useStacks = () => useQuery({ queryKey: ['stacks'], queryFn: () => request<StacksResponse>('/stacks'), refetchInterval: 30_000 })

/** Programs found in the cluster; asked only when the person presses "Find installed". */
export const useStackDiscovery = (enabled: boolean) =>
  useQuery({ queryKey: ['stacks', 'discover'], queryFn: () => request<StackDiscovery>('/stacks/discover'), enabled, staleTime: 60_000, retry: false })

/** Checks an address before it is saved (the core only reads from it). */
export const checkAddress = (kind: ProgramKind, url: string) => postJSON<CheckResult>('/stacks/check', { kind, url })

/** Services seen in telemetry; described refers to the default system. */
export const useDiscovery = () =>
  useQuery({ queryKey: ['discovery'], queryFn: () => request<DiscoveryResponse>('/discovery/services') })

export const useAlerts = () =>
  useQuery({ queryKey: ['alerts'], queryFn: () => request<AlertsResponse>('/alerts'), refetchInterval: 30_000 })

export const useSettings = () => useQuery({ queryKey: ['settings'], queryFn: () => request<Settings>('/settings') })

/** Runbook of a service: system-scoped with a system, else found by service name for links without one. */
export const useRunbook = (service: string, system?: string) =>
  useQuery({
    queryKey: ['runbook', system ?? '', service],
    queryFn: () =>
      request<Runbook>(
        system ? `/systems/${encodeURIComponent(system)}/runbooks/${encodeURIComponent(service)}` : `/runbooks/${encodeURIComponent(service)}`,
      ),
    retry: false,
  })

const sys = (id: string) => `/systems/${encodeURIComponent(id)}`

export const HISTORY_LIMIT = 50

/** null when the core is older than the incident history (404): the UI then shows nothing. */
export async function fetchIncidentHistory(system: string, service: string): Promise<HistoryResponse | null> {
  const q = new URLSearchParams({ limit: String(HISTORY_LIMIT) })
  if (system) q.set('system', system)
  if (service) q.set('service', service)
  try {
    return (await request<HistoryResponse>(`/incidents/history?${q}`)) ?? {}
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null
    throw e
  }
}

export const useIncidentHistory = (system: string, service: string) =>
  useQuery({
    queryKey: ['incidents', 'history', system, service],
    queryFn: () => fetchIncidentHistory(system, service),
    retry: false,
    refetchInterval: 60_000,
  })

export const withNote = (data: HistoryResponse | null | undefined, rec: HistoryIncident) =>
  data?.incidents ? { ...data, incidents: data.incidents.map((i) => (i.id === rec.id ? { ...i, ...rec } : i)) } : data

export const useSaveIncidentNote = () => {
  const qc = useQueryClient()
  return useMutation({
    // The core answers with the whole record; its note is absent once both fields are empty.
    mutationFn: (v: { id: string } & NoteInput) => send<HistoryIncident>('PUT', `/incidents/history/${encodeURIComponent(v.id)}/note`, { cause: v.cause, fix: v.fix }),
    onSuccess: (rec) => {
      if (rec?.id) qc.setQueriesData<HistoryResponse | null>({ queryKey: ['incidents', 'history'] }, (d) => withNote(d, rec))
      void qc.invalidateQueries({ queryKey: ['incidents', 'history'] })
      // The note shows up as "Last time" on live incidents and in runbooks.
      void qc.invalidateQueries({ queryKey: ['alerts'] })
      void qc.invalidateQueries({ queryKey: ['runbook'] })
    },
  })
}

export const useSystems = () =>
  useQuery({ queryKey: ['systems'], queryFn: () => request<SystemsResponse>('/systems'), refetchInterval: 30_000 })

export const useSystem = (id: string) =>
  useQuery({ queryKey: ['system', id], queryFn: () => request<SystemResponse>(sys(id)), retry: false, refetchOnWindowFocus: false, staleTime: Infinity })

export const useSystemDiscovery = (id: string) =>
  useQuery({ queryKey: ['system', id, 'discovery'], queryFn: () => request<DiscoveryResponse>(`${sys(id)}/discovery`), refetchInterval: 60_000 })

export const useRuleStates = (id: string) =>
  useQuery({ queryKey: ['system', id, 'states'], queryFn: () => request<RuleStatesResponse>(`${sys(id)}/rules/state`), refetchInterval: 30_000 })

export const useNoise = (id: string, window: NoiseWindow) =>
  useQuery({
    queryKey: ['system', id, 'noise', window],
    queryFn: () => request<NoiseResponse>(`${sys(id)}/noise?window=${window}`),
    retry: false,
    staleTime: 5 * 60_000,
    placeholderData: keepPreviousData,
  })

const sameRule = (r: NoiseRule, k: { alert?: string; service?: string; operation?: string }) =>
  (r.alert ?? '') === (k.alert ?? '') && (r.service ?? '') === (k.service ?? '') && (r.operation ?? '') === (k.operation ?? '')

function mapRules(data: NoiseResponse | undefined, k: { alert?: string; service?: string; operation?: string }, fn: (fb: NoiseRule['feedback']) => NoiseRule['feedback']) {
  if (!data?.rules) return data
  return { ...data, rules: data.rules.map((r) => (sameRule(r, k) ? { ...r, feedback: fn(r.feedback) } : r)) }
}

export const withFeedback = (data: NoiseResponse | undefined, v: NoiseFeedback) => mapRules(data, v, (fb) => ({ ...fb, [v.verdict]: (fb?.[v.verdict] ?? 0) + 1 }))

export function noiseFeedbackOptions(qc: QueryClient, id: string) {
  const key = ['system', id, 'noise']
  return {
    mutationFn: (v: NoiseFeedback) => postJSON<NoiseFeedbackResult>(`${sys(id)}/noise/feedback`, v.operation ? v : { alert: v.alert, service: v.service, verdict: v.verdict }),
    onMutate: async (v: NoiseFeedback) => {
      await qc.cancelQueries({ queryKey: key })
      const before = qc.getQueriesData<NoiseResponse>({ queryKey: key })
      qc.setQueriesData<NoiseResponse>({ queryKey: key }, (d) => withFeedback(d, v))
      return { before }
    },
    onSuccess: (res: NoiseFeedbackResult | undefined, v: NoiseFeedback) => {
      // The core answers with the rule it counted (an aggregated rule loses the operation) and the totals.
      const fb = res?.feedback
      if (fb) qc.setQueriesData<NoiseResponse>({ queryKey: key }, (d) => mapRules(d, res.alert === undefined ? v : res, () => fb))
      // Verdicts can change the noisy flag, which only the core decides.
      void qc.invalidateQueries({ queryKey: key })
    },
    onError: (_e: Error, _v: NoiseFeedback, ctx?: { before: [QueryKey, NoiseResponse | undefined][] }) => {
      for (const [k, d] of ctx?.before ?? []) qc.setQueryData(k, d)
    },
  }
}

export const useNoiseFeedback = (id: string) => {
  const qc = useQueryClient()
  return useMutation(noiseFeedbackOptions(qc, id))
}

export const validateSystem = (id: string, yaml: string) => postYAML<ValidationResult>(`${sys(id)}/validate`, yaml)

export const backtestRule = (id: string, req: RuleBacktestRequest) => postJSON<RuleBacktestResult>(`${sys(id)}/backtest`, req)

export const useSaveSystem = (id: string) => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: string | ({ yaml: string } & GitNote)) => {
      const yaml = typeof v === 'string' ? v : v.yaml
      return request<SaveDescriptionResult>(sys(id), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/yaml', ...noteHeaders(typeof v === 'string' ? undefined : v) },
        body: yaml,
      })
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['systems'] })
      void qc.invalidateQueries({ queryKey: ['telegram'] })
    },
  })
}

export const useCreateSystem = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: string | ({ yaml: string } & GitNote)) =>
      request<SaveDescriptionResult & { id: string }>('/systems', {
        method: 'POST',
        headers: { 'Content-Type': 'application/yaml', ...noteHeaders(typeof v === 'string' ? undefined : v) },
        body: typeof v === 'string' ? v : v.yaml,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['systems'] }),
  })
}

/** Puts a system made by hand under the git repository; it keeps working until the merge. */
export const useMoveToGit = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => request<{ staged: boolean }>(`${sys(id)}/git`, { method: 'POST' }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['git'] })
      void qc.invalidateQueries({ queryKey: ['systems'] })
    },
  })
}

export const useDeleteSystem = () => {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: string | ({ id: string } & GitNote)) =>
      request<{ deleted?: boolean; staged?: boolean }>(sys(typeof v === 'string' ? v : v.id), {
        method: 'DELETE',
        headers: noteHeaders(typeof v === 'string' ? undefined : v),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['systems'] }),
  })
}

// Chats appear when the bot is added to a group; poll so the list updates
// without a manual refresh.
export const useTelegram = (system?: string, refetchInterval: number | false = 3_000) =>
  useQuery({
    queryKey: ['telegram', system ?? ''],
    queryFn: () => request<TelegramResponse>(system ? `${sys(system)}/telegram` : '/telegram'),
    refetchInterval,
  })

const send = <T>(method: string, path: string, body?: unknown) =>
  request<T>(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })

// Mutations that carry a secret (bot token, SMTP password) pass gcTime 0, so
// React Query drops their variables as soon as the form that used them is gone.
function useTelegramMutation<TVars, TRes>(fn: (v: TVars) => Promise<TRes>, gcTime?: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    gcTime,
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['telegram'] })
      void qc.invalidateQueries({ queryKey: ['systems'] })
    },
  })
}

/** The token is sent once and never comes back: the response carries only the bot's public name. */
export const useSetTelegramToken = () =>
  useTelegramMutation((token: string) => send<{ bot: TelegramBot }>('PUT', '/telegram/token', { token }), 0)

export const useDeleteTelegramToken = () => useTelegramMutation(() => send<unknown>('DELETE', '/telegram/token'))

export const useAddTelegramChat = () =>
  useTelegramMutation((chatID: string) => send<{ chat: TelegramChat }>('POST', '/telegram/chats', { chatID: Number(chatID) }))

export const useDeleteTelegramChat = () =>
  useTelegramMutation((chatID: number) => send<unknown>('DELETE', `/telegram/chats/${chatID}`))

export const useTestTelegramChat = () =>
  useTelegramMutation((chatID: number) => send<TelegramTestResult>('POST', `/telegram/chats/${chatID}/test`))

export const useEmail = (refetchInterval: number | false = false) =>
  useQuery({ queryKey: ['email'], queryFn: () => request<EmailResponse>('/email'), refetchInterval })

function useEmailMutation<TVars, TRes>(fn: (v: TVars) => Promise<TRes>, gcTime?: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    gcTime,
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['email'] })
      void qc.invalidateQueries({ queryKey: ['systems'] })
    },
  })
}

// The password goes only into this request body; it is never read back.
export const useSetEmailServer = () => useEmailMutation((v: EmailServerInput) => send<{ server: EmailServer }>('PUT', '/email/server', v), 0)

export const useDeleteEmailServer = () => useEmailMutation(() => send<unknown>('DELETE', '/email/server'))

export const useAddEmailRecipient = () =>
  useEmailMutation((v: { name: string; to: string[] }) => send<{ recipient: EmailRecipient }>('POST', '/email/recipients', v))

export const useUpdateEmailRecipient = () =>
  useEmailMutation((v: { id: string; name: string; to: string[] }) =>
    send<{ recipient: EmailRecipient }>('PUT', `/email/recipients/${encodeURIComponent(v.id)}`, { name: v.name, to: v.to }),
  )

export const useDeleteEmailRecipient = () => useEmailMutation((id: string) => send<unknown>('DELETE', `/email/recipients/${encodeURIComponent(id)}`))

export const useTestEmailRecipient = () =>
  useEmailMutation((id: string) => send<{ ok: boolean; error?: string }>('POST', `/email/recipients/${encodeURIComponent(id)}/test`))

const stk = (id: string) => `/stacks/${encodeURIComponent(id)}`

function useStackMutation<TVars, TRes>(fn: (v: TVars) => Promise<TRes>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['stacks'] })
      void qc.invalidateQueries({ queryKey: ['systems'] })
    },
  })
}

export const useCreateStack = () => useStackMutation((v: StackInput) => send<{ set: StackSet }>('POST', '/stacks', v))

export const useUpdateStack = () => useStackMutation((v: { id: string } & StackInput) => send<{ set: StackSet }>('PUT', stk(v.id), { ...v, id: undefined }))

export const useDeleteStack = () => useStackMutation((id: string) => send<{ deleted: boolean }>('DELETE', stk(id)))

export const useSetStackAddress = () =>
  useStackMutation((v: { id: string; kind: ProgramKind; url: string }) => send<{ set: StackSet }>('PUT', `${stk(v.id)}/programs/${v.kind}`, { url: v.url }))

/** Built-in set: back to the install-time address; other sets: not connected. */
export const useResetStackAddress = () =>
  useStackMutation((v: { id: string; kind: ProgramKind }) => send<{ set: StackSet }>('DELETE', `${stk(v.id)}/programs/${v.kind}`))
