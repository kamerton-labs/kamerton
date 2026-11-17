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

Known limitations are listed in the README under "Status and limitations".
