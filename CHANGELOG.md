# Changelog

All notable changes to Kamerton are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- A receiver whose Secret does not exist yet no longer switches off the
  whole system. The Prometheus Operator rejects an AlertmanagerConfig that
  refers to a missing Secret, so a description naming Telegram or email
  before the channel was connected got no routes and no muting by cause at
  all. Kamerton now leaves out only that channel, applies the rest, sets
  the condition `ChannelsConnected` to False on the description with the
  receiver and Secret named, and looks again every 30 seconds. A Secret
  Kamerton may not read is kept as before.

## [0.2.0] - 2026-11-18

### Added

- Incident history and what helped. Every incident (root services and
  alerts, affected services, start, end, duration, the follow-up's
  evidence) is kept in the ConfigMap `kamerton-incident-history` (at most
  300 incidents, 180 days; memory without a cluster); open incidents are
  listed too. People add a cause and what helped
  (`PUT /api/v1/incidents/history/{id}/note`, or a reply in Telegram to
  the new "incident is over" message, sent after an incident with a
  Telegram follow-up that lasted at least 2 minutes; off with
  `spec.notifications.askWhatHelped: false`). The follow-up of the next
  incident of the same root service reminds of the last one (when, how
  long, cause, what helped, how many times in 30 days), and a note alone
  is worth a follow-up. `GET /api/v1/incidents/history`, `previous` and
  `historyId` on live incidents in the alerts API, and `experience` in a
  service's runbook data. Callers' alerts in the 35 minutes after the
  cause resolved (unmuted errors, a lingering SLO burn) are the incident's
  aftermath, not a new incident with a second follow-up; still firing
  after that, they become an incident of their own. A record opened for a
  caller before the cause's alert fired is merged into the cause's.
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
- Services without OpenTelemetry: optional eBPF auto-instrumentation in the
  Helm chart, off by default (`autoInstrument.enabled`). It installs
  OpenTelemetry eBPF Instrumentation v1.0.0 (chart
  `opentelemetry-ebpf-instrumentation` 0.16.1) as a DaemonSet that sends
  traces of HTTP/gRPC calls to Kamerton's Collector, so uninstrumented
  services get the same span metrics, rules and "found in telemetry" entry
  as instrumented ones. Scope with `autoInstrument.namespaces.include` /
  `exclude` (default: all but `kube-system` and Kamerton's own namespace) and
  the pod annotation `kamerton.dev/auto-instrument: "false"`; services that
  already send OpenTelemetry are left out by default
  (`autoInstrument.skipSDKInstrumented`), though the agent's detection
  misses some SDKs (nginx OTel module, C++, Python grpcio in the test).
  Monitoring programs and browsers are excluded by executable
  (`autoInstrument.excludeExecutables`). Runs as a privileged container
  with `hostPID` by default (see SECURITY.md); memory limit 2Gi.
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
- The per-service "no data" alert (`KamertonServiceTelemetryMissing`) could
  never fire with the bundled Collector: its spanmetrics connector kept
  exporting a silent service's series as flat counters forever. It now sets
  `metrics_expiration: 2m`, so a silent service is reported about 6 minutes
  after its last span. A Collector of your own needs the same setting. Side
  effects: a critical service that stops completely gets "requests stopped",
  which resolves when its series expire and is followed by "no data"; a
  service or system with no calls at all for that long is reported as
  sending no data.
- "No data" means "this normally has traffic now, and nothing arrives".
  Both "no data" alerts fire only at hours when the service (the system,
  for `KamertonTelemetryMissing`) had calls in every 10 minutes of the two
  hours around the same time a week ago, averaging above
  `trafficDrop.minRequestsPerSecond` (a day ago while it has no data from
  a week ago). An idle test cluster at night, a batch service or a tool
  idle on weekends no longer reports missing data, and nothing fires in the
  first day after a service starts sending. "Requests stopped arriving"
  counts no calls at all (a flat counter or a vanished series) under the
  same condition and keeps firing when the series vanish, and the
  per-service "no data" alert stays silent while that rule covers the stop:
  a critical service that stops completely gets one notification. The
  first-day exception: a drop to a few percent of normal still fires after
  an hour of history; a complete stop needs a day.
- Starved services no longer page. When a service fails, the services it
  calls and the consumers of a queue it produces to lose their traffic, and
  their "no data" and "requests stopped" alerts fired on their own (with
  `payment` down: `email`, `accounting`, `fraud-detection`). Such an alert
  now carries `kamerton_starved="true"` while a feeder's alert that the
  service did not cause fires and the service has no error, latency or "no
  ready pods" alert of its own; inhibit rules mute it, the incident lists
  the service as affected, and the noise statistics count it as a
  consequence. Queue consumers are the services named in
  `queue.consumerGroups`. Off with `spec.notifications.groupByCause: false`.

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
