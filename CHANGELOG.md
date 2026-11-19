# Changelog

All notable changes to Kamerton are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.3.1] - 2026-11-19

### Changed

- Every generated alert keeps firing for 5 minutes after its condition stops holding
  (`keep_firing_for`). A condition that drops out for a minute used to resolve the alert and
  fire it again, a message and a "resolved" each time: on the demo shop one latency rule
  flapped 22 times in 3 hours. A real recovery now reads as resolved 5 minutes later.

### Fixed

- A request longer than a minute was taken for a load generator's session, and its page showed
  one step of it ("went through without errors, 1 step") instead of all 126. A session is now
  told by its shape: several requests at the root of the trace, or one long root whose children
  run one after another. Seen on a checkout that waited 62 s to write to Kafka.
- p95 and p99 stopped at 15 s: the spanmetrics connector's buckets ended there, so a request of
  a minute read as 15 s on the charts and in the alerts. The chart now sets buckets up to 5
  minutes.

## [0.3.0] - 2026-11-19

### Added

- Looking into a failure, in three steps. A system opens on Now: one sentence on
  what is broken (the cause, since when, which services suffer from it), the error
  budget of each SLO, and the map, where the cause is marked as the source and the
  way to the services it drags along is lit. A service opens its page: what is
  wrong in words, then errors, latency p50/p95/p99, requests, queue lag, memory
  against the limit and CPU over a range, each against its usual level (the median
  of the day before). A click on a chart lists the failed or the slowest requests
  of those minutes; a request opens with its steps (the failed one picked, small
  steps folded away, one request cut out of a long session) and the log lines of
  its trace_id. An arrow of the map turns red with its share of failed calls (from
  Tempo's map of calls) and opens the calls from one service to the other: how many,
  how many fail, p95, the failed ones. The service page says what changed in the pods
  in the last 30 minutes (rollouts, restarts, scalings), read in a cluster or with
  the kubeconfig next to one. API: `GET /api/v1/systems/{id}/now`, `…/calls`,
  `…/services/{service}/changes`,
  `…/services/{service}/series?range=`, `…/services/{service}/requests`,
  `…/traces/{trace}`; Tempo and Loki are read with the URLs of the system's set.
- Five Grafana dashboards per system instead of one, built from the description like
  the rules: Overview (alerts firing now, each service's numbers, Tempo's map of
  calls), Services (requests, errors, latency p50/p95/p99, memory against the limit,
  CPU, restarts), Queues (messages written and read, consumer lag against its
  limit), Infrastructure (nodes, the system's pods, restarts, readiness) and SLO
  (availability over the window, error budget left, burn rate over 1h and 6h).
  Every service panel links to the failed requests of its range in Tempo and to the
  service in Kamerton; the dashboards link to each other.
- The chart turns on Tempo's metrics generator for the map of calls
  (`traces_service_graph_*`, written to Prometheus), and links the signals in
  Grafana: a trace opens the log lines of its trace_id, a log line with a trace_id
  opens its trace, a trace shows its service map.
- System descriptions from a git repository. With the chart value `git.url`
  set (also `git.branch`, `git.path`, `git.secretName`, `git.interval`),
  Kamerton reads the description files of one branch, applies them and keeps
  following the branch every minute, so a merged change is in the cluster
  without anyone running a command. A system from git is changed only
  there: the UI shows it read-only with its file and commit, and saving or
  deleting it through the API answers 409 `from_git`. Systems made in the UI
  or with kubectl are left alone, and a file that names one of them is
  reported as a conflict instead of taking it over. A file with an error
  keeps the system as it was and the error is shown on the Systems screen;
  a deleted file deletes its system; a repository that cannot be read
  deletes nothing. A private repository needs a read-only token in the
  Secret `kamerton-git`, key `token`. `GET /api/v1/systems` carries the state
  of the last read in `git` and each system's file and commit.
- Writing to git. With `git.write: pullRequest` a save of a system from git
  in the UI is pushed to a new branch `kamerton/<system>-<commit>` and, on
  GitHub and GitLab (also self-hosted, `git.provider` and `git.apiURL`),
  opened as a pull or merge request; the API answers 202 with the request's
  address and the change reaches the cluster after the merge. With
  `git.write: commit` the save is committed straight to the followed branch.
  Deleting such a system removes its file the same way, and a system
  created in the UI becomes a new file `<git.path>/<name>.yaml`: with
  writing on, the repository is where systems are made. The UI then edits
  the file as it is in the repository, comments included. Changes Kamerton
  makes to a description by itself (a chat added on the Channels screen) are
  still refused for a system from git.
- "Move to git" in a system's menu (`POST /api/v1/systems/{id}/git`): a
  system made in the UI or with kubectl is written to the repository as a
  new file. It keeps working as it is, and once the file is on the followed
  branch the file takes it over in place: no deletion, no gap in its rules.
- A project file. `kamerton.yaml` at the root of the project (kind
  `Project`) names the project and says where things are: `spec.systems`
  (default `systems`), `spec.generated` (default `generated`, an empty
  string turns it off) and `spec.ignore`, folders Kamerton does not read.
  The repository then describes itself, and moving a project to another
  cluster needs its address and nothing else. Without the file the
  installation's settings lay the repository out as before. A mistyped
  field in the file is an error and changes nothing.
- Starting from an empty repository. A repository without commits is a
  state, not an error. The first system created in the UI makes the first
  commit, with the project file, a README and a `.gitignore`, on the
  followed branch even in pull-request mode. A folder of systems that does
  not exist yet is fine while no system has come from the repository.
- Git in the UI. Before a change goes to the repository a dialog shows the
  file, the branch and the repository, says in words which rules the change
  affects, and takes the commit message; the logged-in user is named in the
  commit. The Systems screen links to the repository and the applied
  commit, can check the branch at once (`POST /api/v1/git/sync`) and lists
  the open pull or merge requests Kamerton made; a system shows its open
  request. `POST /api/v1/git/preview` returns what a description would
  change.
- Saving is not pushing. A save of a system from git, a new system and a
  removal are kept as changes to push (ConfigMap `kamerton-git-changes`);
  the repository and the cluster stay as they are. "Git" in the sidebar
  lists them, each with its effect on the rules, and one Push writes them
  in one commit with one message (`GET /api/v1/git/changes`,
  `POST /api/v1/git/push`, `DELETE /api/v1/git/changes/{system}`). A save
  answers 202 `{"staged": true}`.
- The list of commits of the followed branch, with the systems each one
  touches and the applied one marked (`GET /api/v1/git/commits`).
- The repository is connected, changed and disconnected in the UI: address,
  branch, folder, token and push mode, with "Test" that reads the
  repository before anything is stored (`/api/v1/git/settings`). The
  settings are kept in the ConfigMap `kamerton-git-settings`, the token in
  the Secret `kamerton-git`. Disconnecting deletes nothing: the systems
  stay and become editable in the UI. A repository set with `git.url` is
  shown and cannot be changed there.
- Drafts. A system created in the UI while git push is on opens at once as
  a draft: it is listed on the Systems screen with a mark, can be drawn,
  edited and deleted, and runs nothing until it is pushed and on the
  followed branch.
- The diff of a change and of a commit. Each change to push has "Diff": the
  description file line by line, removed and added. A commit in the list
  opens to show, for every description file it touched, what happened to
  the rules and the same line-by-line diff
  (`GET /api/v1/git/commits/{hash}`). The commit list loads more on demand.
- Projects and a first screen. Kamerton starts with a project, a name and a place, and until
  one is open shows only the first screen, without the sidebar, the way an editor starts:
  "New project" or "Clone from git" (`GET/POST /api/v1/project`).
  - On a machine a project is a folder: `~/kamerton/<name>` by default, with the project
    file, a README, a `.gitignore` and a `systems` folder, optionally `git init` and a first
    commit. A clone goes into a folder as an ordinary working copy; the token is used for the
    clone and not kept. Projects opened before are listed and any project folder can be
    opened (`/api/v1/project/open`); the project opened last opens on start, and "Close
    project" goes back to the first screen, where it stays after a restart. `--description`
    also takes a project folder.
  - In a cluster the place is the cluster: the name is kept in the ConfigMap
    `kamerton-project`, and git can be connected at once or later. A project taken from a
    repository keeps its name there too, so disconnecting the repository does not end it. An
    installation that already has systems is a project and never sees the first screen.
  - Cloning reads the repository first (`/api/v1/project/check`) and then opens the project
    there or starts one in it with the first commit, named as given; the folder in the
    repository (the root by default) and the push mode are under "More". Starting a project
    and push need a token with write access: the check says so (`canPush`) and the server
    refuses with 422. An empty repository cannot be opened (409 `git_empty`), a project
    cannot be created over another (409), and a create that fails leaves nothing connected
    or on disk.
  - A local project in a git working copy is pulled and pushed from its page with the
    person's own git and its access to the repository (`/api/v1/project/git`, `…/pull`,
    `…/push`): what is not committed and not pushed is listed, Commit & Push commits only the
    project's folder, Pull only follows on. Without `user.email` set Kamerton does not commit,
    so git cannot put an address made up from the host name into the history. Check
    (`…/fetch`) asks the repository for new commits without changing the folder. A failed
    step answers with a `problem` (`auth`, `identity`, `behind`, `diverged`, `no_host`, …)
    and git's own message as `detail`; the UI says the problem in its own language.
  - A project is renamed on its page (`PATCH /api/v1/project`): locally the name in
    `kamerton.yaml` changes and nothing else in the file; in a cluster the kept name. With a
    repository connected the name is its project file's and is changed there.
  - What is wrong with a repository is said in words: no such server, no answer, a
    certificate that is not trusted, no access, no such repository or branch (`problem` in
    the check), with the message of git under it.
- Git in the sidebar: the project with its branch and folder at the top, and
  a "Project" page with the changes to push, the repository, open pull
  requests, commits and description files. Wording follows git: Commit,
  Push, Pull.
- `git.generatedPath`: a change written by Kamerton also carries the rules,
  dashboards and runbooks generated from the description, in
  `<generatedPath>/<system>/`, laid out like `kamerton generate -o`, so the
  review shows what changes.
- `git.commitStatus`: every read commit is marked on GitHub or GitLab as
  applied or not, with the file and the error.
- `kamerton diff -f new.yaml --against old.yaml` prints which rules a change
  of a description changes: threshold, duration and severity in words, rules
  added and removed. `--exit-code` exits with 2 when the rules differ. For
  a check in CI.

### Changed

- Tempo keeps traces for a week on a disk of its own (10Gi, `tempo.persistence`)
  instead of a day in the pod, so a trace from last week's failure still opens and
  a restart loses nothing. Kubernetes does not let a StatefulSet get a disk in
  place: before `helm upgrade`, run
  `kubectl -n <namespace> delete statefulset kamerton-tempo`. Tempo is down
  until the upgrade starts it again, and the traces it held are not carried over.
  (With `--cascade=orphan` the old pod is kept, and it goes on without the disk.)
- Prometheus keeps metrics on a disk of its own too (20Gi, `prometheusSpec.storageSpec`),
  so the 15 days of `retention` survive a restart of the pod, and `retentionSize: 18GB`
  drops the oldest days before the disk fills. The operator recreates its StatefulSet by
  itself; the metrics gathered before the upgrade are not carried over, so the usual
  levels (the day before) come back a day later.

## [0.2.4] - 2026-11-18

### Fixed

- Upgrading to 0.2.3 with `helm upgrade --reuse-values` left the new
  setting `incidents.tailMute` empty, and the new pod exited with
  "--tail-mute: invalid duration" (the old pod kept serving). An empty value
  now means the default, in the chart and in the core. The documentation
  and the chart notes now use `--reset-then-reuse-values`, which keeps your
  own values and takes the defaults of new settings from the new chart.

## [0.2.3] - 2026-11-18

### Added

- The consequences of an incident stay out of the channels after its cause
  is gone. Error rates and error budgets look back over a window and keep
  firing for minutes after the fix, and Alertmanager stops muting them the
  moment the cause resolves, so they used to arrive as new alerts. While an
  alert is muted by an inhibit rule, Kamerton now keeps an Alertmanager
  silence on exactly that alert (a matcher per label, author `kamerton`)
  and prolongs it every 30 seconds; after the cause resolves the silence
  runs out by itself, 10 minutes later by default. A different alert of the
  same service is not silenced, and one that still fires after that time is
  sent. Chart value `incidents.tailMute` (flag `--tail-mute`), `0` turns it
  off. The Alerts screen keeps showing such alerts as the tail of the
  incident.

### Changed

- Alerts that linger after an incident has ended (error budgets, windows
  that still see the failure) are shown on the Alerts screen as the tail of
  that incident, "Settling after the payment incident", not as a new
  incident with its own cause. The system's state says "settling after
  payment". `GET /api/v1/alerts` names the finished incident in
  `aftermath`.
- The evidence words the first rollout of a workload as "first deployed
  at", not "new version at": a service installed minutes ago has not
  changed.

## [0.2.2] - 2026-11-18

### Added

- The UI says when a system sends alerts to a channel nobody has connected:
  a note on the system's card and a notice on the Channels screen naming the
  system and the channel. `GET /api/v1/systems` lists such receivers in
  `channelsOff`.

## [0.2.1] - 2026-11-18

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
