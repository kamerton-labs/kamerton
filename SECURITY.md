# Security policy

## Reporting a vulnerability

Please do **not** open a public issue for a security problem.

Report it privately to **`<security contact: TODO, to be set by the maintainers>`**
(or through the repository's private vulnerability reporting, once enabled).
Include:

- the Kamerton version (`kamerton version`, or the image tag / chart version);
- how Kamerton is deployed (Helm values that matter, login on or off, ingress);
- steps to reproduce and what an attacker gains.

We aim to acknowledge a report within 5 working days and to agree on a
disclosure date with you. Please give us a reasonable time to release a fix
before publishing details.

## Supported versions

Kamerton is at 0.1.x. Fixes go into the latest release only.

## Security model in short

- The UI and API are **open to anyone who can reach them unless login is
  turned on** (`auth.existingSecret` in the chart). There is one shared user,
  without roles. Serve the UI over HTTPS (TLS at the ingress).
- The core stores the Telegram bot token and the SMTP password only in
  Kubernetes Secrets in its own namespace; they are never returned by the API
  or written to logs. RBAC limits the core to those Secrets by name (plus
  `create`, which Kubernetes cannot restrict by name).
- Finding installed programs lists Services in all namespaces (read-only);
  turn it off with `stacks.discovery.enabled=false`.
- `POST /api/v1/hooks/alertmanager` is called by Alertmanager, not by a
  browser, so it needs no login session and no `X-Requested-With` header.
  It requires `Authorization: Bearer <token>`; the token is 256 random bits
  that the core generates into the Secret `kamerton-webhook` (key `token`)
  in its own namespace, compared in constant time. The generated
  AlertmanagerConfig references that Secret. The endpoint only starts the
  follow-up message (it never changes settings); a valid token lets a
  caller trigger messages to the system's own chats and addresses, at most
  one per incident. Turn it off with `incidents.followUp.enabled=false`.
- Follow-up messages read pods, events, ReplicaSets and StatefulSets in all
  namespaces (get/list, read-only, no Secrets); turn this off with
  `incidents.followUp.workloadRead=false`. A quoted log line is shortened
  but not scrubbed: whatever the service logs may appear in the chat.
- eBPF auto-instrumentation (`autoInstrument.enabled`, off by default)
  installs OpenTelemetry eBPF Instrumentation as a DaemonSet in Kamerton's
  namespace. By default each pod is a **privileged container** with
  `hostPID: true` (all capabilities, host devices, no AppArmor or seccomp
  confinement; no host paths, no host network). Verified on a k3s cluster
  (Ubuntu kernel 6.8, AppArmor on): with only the capabilities OBI's
  documentation lists (`BPF`, `PERFMON`, `SYS_PTRACE`, `DAC_READ_SEARCH`,
  `CHECKPOINT_RESTORE`, `NET_RAW`) it could not enter pods' network
  namespaces and recorded nothing; privileged, it worked. From the
  documentation and source only, not tried: with
  `opentelemetry-ebpf-instrumentation.privileged=false` it runs as UID 0
  with hostPID, those capabilities plus `SYS_ADMIN` (needed by `setns`
  into a network namespace), others dropped and a read-only root
  filesystem; `SYS_ADMIN` is close to root. Either way this is node-level
  access: it can load eBPF programs, read every process's executable and
  `/proc` entries and see request
  metadata (method, path, status, peer addresses) of HTTP, gRPC, SQL and
  other calls on the node, including TLS traffic of Go and OpenSSL programs.
  Trace context propagation (writing headers into application traffic) and
  Java agent / Node.js signal injection are off. It exports only traces, to
  Kamerton's Collector, which stores them in Tempo; spans carry URL query
  strings (OBI replaces values of known sensitive keys with `REDACTED`), not
  SQL text or bodies by default. Its ClusterRole reads pods, services, nodes and
  ReplicaSets in all namespaces. Kamerton's namespace must admit privileged
  pods (Pod Security level `privileged`) while it is on.

Known limitations are listed in the README under "Status and limitations".
