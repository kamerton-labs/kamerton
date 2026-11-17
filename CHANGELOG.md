# Changelog

All notable changes to Kamerton are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

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
