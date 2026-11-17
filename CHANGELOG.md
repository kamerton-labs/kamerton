# Changelog

All notable changes to Kamerton are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - unreleased

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
