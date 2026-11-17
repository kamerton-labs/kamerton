# Contributing to Kamerton

Thank you for your interest. Bug reports, questions and pull requests are
welcome.

## Before you start

- For anything larger than a small fix, open an issue first and describe the
  problem and the change you have in mind.
- Security problems are not reported in public issues: see
  [SECURITY.md](SECURITY.md).
- Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Contributor licence agreement

Kamerton is licensed under AGPL-3.0-only. Before a pull request can be merged,
its author must agree to the Kamerton Contributor Licence Agreement (CLA).

> **TODO (maintainers):** the CLA text and the way to sign it are being
> prepared by the maintainers' lawyer and will be published here. Until then,
> pull requests can be reviewed but not merged.

## Developer Certificate of Origin: sign off your commits

Every commit must carry a `Signed-off-by` line, which certifies the
[Developer Certificate of Origin](https://developercertificate.org/):

```sh
git commit -s -m "rules: explain the latency threshold in the card"
```

Use your real name and an email address you can be reached at.

## Development setup

Requirements: Go 1.25 (the Makefile sets `GOTOOLCHAIN=local`, so the installed
Go must be 1.25 or newer), Node 24, pnpm 12, make. Optional: Helm (chart
checks), golangci-lint,
Docker (image). No Kubernetes cluster is needed for development.

```sh
make build        # web UI (pnpm) + Go binary -> bin/kamerton
make run          # build and serve on :8080 with testdata/otel-demo/system.yaml
make ui-dev       # Vite dev server, /api proxied to a core on :8080
make help         # all targets
```

`make core` alone builds the binary with whatever is in `web/dist`; in a
fresh clone that is only a placeholder, so run `make ui` (or `make build`)
first if you want the UI.

Layout: `cmd/kamerton` (entry point), `api/v1alpha1` (description types, the
source of the CRD and JSON Schema), `internal/*` (core packages),
`web/` (React UI), `deploy/helm/kamerton` (chart),
`testdata/` (the demo description used by tests). Examples for trying Kamerton
live in [kamerton-labs/examples](https://github.com/kamerton-labs/examples).
See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Checks to run before a pull request

```sh
make lint         # gofmt, go vet, golangci-lint (when installed), ESLint
make test         # go vet + go test ./..., web typecheck + vitest
make helm-lint    # downloads the pinned chart dependencies, helm lint --strict, helm template
make check        # all of the above
```

`make helm-deps` (called by `make helm-lint`) adds the upstream chart
repositories to your local Helm configuration and downloads the charts into
`deploy/helm/kamerton/charts/` (not committed).

If you change `api/v1alpha1`, run `make generate` and commit the regenerated
deepcopy code, CRD (`deploy/helm/kamerton/crds/`) and JSON Schema
(`internal/schema/systemdescription.schema.json`); a unit test fails when the
embedded schema is stale. If you change RBAC needs in the code, update both
the `+kubebuilder:rbac` markers and `deploy/helm/kamerton/templates/rbac.yaml`.

## Pull requests

- Keep a pull request to one topic; explain what changes for the user and how
  you tested it.
- Add or update tests with the change. Rules, generators and the server have Go
  unit tests; the UI model and components have vitest tests.
- Update the documentation when behaviour or configuration changes, and add a
  line to
  [CHANGELOG.md](CHANGELOG.md).
- New source files start with the licence header used in the rest of the
  tree:

  ```
  // Copyright 2026 The Kamerton Authors
  // SPDX-License-Identifier: AGPL-3.0-only
  ```

- Never commit secrets, real chat IDs, real email addresses or local paths.
  Keep local copies of examples as `*.local.yaml` and local settings in
  `.env`; both are ignored by git.
