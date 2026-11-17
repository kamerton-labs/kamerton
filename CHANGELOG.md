# Changelog

All notable changes to Kamerton are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Quieter rules. `GET /api/v1/systems/{id}/noise?window=7d` (1d, 3d, 7d)
  returns, per rule, how often it fired in the window, how many firings
  cleared within 5 minutes, the longest firing, when it last fired, the
  firings Alertmanager muted while a dependency (or the whole system)
  failed (`consequences`, left out of everything else), the verdicts of
  people and whether it is noisy (at least 10 firings and 3 a day, three
  quarters short; or at least 2 "noise" verdicts outnumbering "useful").
  For a noisy rule with a threshold or `for`, a suggestion found with the
  backtest: a threshold at most 1.5 times higher first, then a longer `for`
  (at most 3m for critical rules, 10m for warnings) with the delay it adds
  (`delaySeconds`), then larger thresholds; it must remove at least 80% of
  the short firings and miss no firing of 10 minutes or more, and may
  silence the rule completely only after 20 firings. Values are written as
  the description field takes them. Two range queries per system for the
  statistics; suggestions for at most 5 rules, computed on request and
  never applied by Kamerton.
- "Useful" / "Noise" buttons under the follow-up message in Telegram
  («По делу» / «Шум» in Russian), handled by the bot's polling (callback
  queries, `allowed_updates` now includes `callback_query`), accepted only
  from chats that receive the system's alerts. Verdicts also come from
  `POST /api/v1/systems/{id}/noise/feedback`. Counters only, in the
  ConfigMap `kamerton-noise-state` (memory without a cluster).
- One message per incident: generated AlertmanagerConfigs carry inhibit
  rules built from `dependsOn`. While a service has an error, latency or
  "requests stopped" alert, the same kinds of alerts of the services that
  call it are muted (memory, CPU, restart, queue, SLO and custom alerts never
  are; services in a dependency cycle do not mute each other).
  `spec.notifications.groupByCause: false` turns it off.
- Incidents in the alerts API (`GET /api/v1/alerts`,
  `GET /api/v1/systems/{id}/alerts`): new fields `incidents` (root services,
  their alerts, affected services and their muted alerts), `muted` (alerts
  muted by grouping) and, per alert, `fingerprint`, `muted` and `incident`.
- Follow-up message with evidence: Alertmanager also notifies Kamerton
  (webhook `POST /api/v1/hooks/alertmanager`, bearer token in the Secret
  `kamerton-webhook`), and Kamerton sends one short message after the alert
  to the same chats and addresses: the probable cause by the dependency
  graph, what changed in the workload in the last 30 minutes (new version
  with image tags, restarts with reason, pods not ready, Warning events),
  the operation with most errors, an error log line from Loki and a link to
  failing traces in Grafana. The alert itself still goes straight from
  Alertmanager. `spec.notifications.evidenceFollowUp: false` turns it off
  per system; Helm values `incidents.followUp.enabled`,
  `incidents.followUp.workloadRead`, `incidents.grafanaExternalURL`.
- "No data" alerts. `KamertonTelemetryMissing` (critical, one per system):
  none of the system's services has sent span metrics for 3 minutes although
  some did in the last day, so Kamerton cannot see the system and no other
  alert can fire; the message says so. `KamertonServiceTelemetryMissing`
  (http/grpc services): one service sent span metrics in the last day, none
  for 3 minutes, while others still do; severity of its "requests stopped"
  rule where that is on, else warning. A service that never sent data never
  fires, and one silent for a day stops. The system alert mutes the
  per-service ones. Settings: `spec.noData` (`enabled`, `after`, `message`),
  `defaults.noData` and `services[].noData` (`enabled`, `after`, `severity`,
  `message`). Runbook stubs, including the system runbook `_system`.
- "No ready pods" alert `KamertonNoReadyPods` from kube-state-metrics
  `kube_pod_status_ready` (`telemetry.containers.readyMetric`): pods of the
  service exist and none is ready, or the last one is gone (for an hour
  after the last ready pod). Pending 30s, so a service scaled to zero is
  reported within about a minute, before its callers' alerts. Settings:
  `defaults.noReadyPods`, `services[].noReadyPods` (`enabled`, `for`,
  `severity`, `message`).
- The follow-up names the likely cause when the alerting service has no
  failing dependency yet: it checks the direct dependencies at that moment
  (pending alerts, no ready pods, error rate, calls far below normal) and
  says what it observed. A later follow-up for that cause says which earlier
  alert it explains.
- `evidence` on each incident of the alerts API: when collected, the cause
  (or likely cause), workload changes, the operation with most errors, a log
  line, a trace link, and the earlier alerts it explains.
- Runbook endpoints serve `_system`: the runbook of the alerts about the
  whole system.

### Changed

- Grouping by cause: "no ready pods" is a cause, and the SLO burn-rate
  alerts of callers are muted too (they never mute others). The incidents in
  the alerts API follow the same rules; the system "no data" alert forms an
  incident `<system>:_system` without roots.
- "Requests stopped arriving" no longer fires for a service with no
  span-metric series at all while its "no data" alert is on: that case is
  the "no data" alert's.
- Sent follow-ups are remembered in the ConfigMap `kamerton-followup-state`
  instead of memory, so a restart or a second replica does not send them
  again.
- A follow-up with nothing to say yet no longer uses up its incident:
  Kamerton tries again about 1 and 3 minutes later and on later
  notifications, then gives up and logs the reason at Info
  (`follow-up not sent`). Before, a root alert that arrived before its
  consequences (such as "no ready pods" of a service scaled to zero) got no
  follow-up at all.
- The follow-up waits 45 seconds after the notification instead of 10, so
  the alerts of the callers have fired and "also affected" is filled.
- The workload evidence reports scaling within the window (from the
  Deployment's scaling events, of type Normal, and a StatefulSet at zero
  replicas), a workload left at zero replicas, and "no pods exist".
- Notification templates omit the service when an alert has none.
- The demo description (`testdata/otel-demo/system.yaml`) generates 195
  rules instead of 164.

### Fixed

- The bundled Prometheus accepts samples up to 30 minutes late
  (`tsdb.outOfOrderTimeWindow`). Before, one late sample made it reject the
  Collector's whole write request and span metrics stopped arriving.

## [0.1.0] - 2026-11-17

First public version.

### Added

- System description (`SystemDescription`, `kamerton.dev/v1alpha1`) with
  JSON Schema and semantic validation; CLI `validate`, `generate`, `schema`.
- Rule library: error rate (per service and per operation), latency, requests
  stopped arriving, saturation, memory/CPU near the limit, CPU and latency far
  above normal, OOMKilled, queue lag and queue not read, SLO burn rate, custom
  rules, team-written notification text.
- Generation of Prometheus rules, a basic Grafana dashboard, Alertmanager
  routes and runbook stubs; a Kubernetes controller that applies them with
  server-side apply and reports status conditions.
- Web UI: systems list, canvas, rule cards with backtest, YAML mode, runbook
  pages, active alerts with the probable source, status page.
- Telegram and email set up from the UI; Kamerton's own message templates in
  Russian and English with time zones.
- Sets of programs: several Prometheus/Alertmanager instances, one set per
  system, finding programs already installed in the cluster.
- Optional login with one shared user.
- Web UI and notification messages in English (default) and Russian.
- Helm chart installing the core with kube-prometheus-stack, Grafana, Loki,
  Tempo and the OpenTelemetry Collector.

### Known limitations

See "Status and limitations" in the README.
