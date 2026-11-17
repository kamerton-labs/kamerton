# Copyright 2026 The Kamerton Authors
# SPDX-License-Identifier: AGPL-3.0-only

# Never let `go` silently download a newer toolchain: go.mod pins go 1.25.
export GOTOOLCHAIN := local

BIN        ?= bin/kamerton
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS    := -s -w -X github.com/kamerton-labs/kamerton/internal/version.Version=$(VERSION) -X github.com/kamerton-labs/kamerton/internal/version.Commit=$(COMMIT)
IMAGE      ?= ghcr.io/kamerton-labs/kamerton:$(VERSION)
ADDR       ?= :8080
DESCRIPTION ?= testdata/otel-demo/system.yaml
CRD        := deploy/helm/kamerton/crds/kamerton.dev_systemdescriptions.yaml

.PHONY: help
help: ## Show targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

## ---- Code generation ----

.PHONY: generate
generate: ## Regenerate deepcopy, CRD and JSON Schema from api/v1alpha1
	go tool controller-gen object:headerFile=hack/boilerplate.go.txt paths=./api/...
	go tool controller-gen crd:allowDangerousTypes=true paths=./api/... output:crd:artifacts:config=deploy/helm/kamerton/crds
	go run ./hack/schemagen -crd $(CRD) -out internal/schema/systemdescription.schema.json

## ---- Web UI ----

web/node_modules: web/package.json web/pnpm-lock.yaml
	cd web && pnpm install --frozen-lockfile
	@touch $@

.PHONY: ui
ui: web/node_modules ## Build the web UI into web/dist (embedded into the binary)
	cd web && pnpm build

.PHONY: ui-dev
ui-dev: web/node_modules ## Vite dev server with /api proxied to a core on :8080
	cd web && pnpm dev

## ---- Core ----

.PHONY: core
core: ## Build the Go binary (embeds whatever is in web/dist)
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/kamerton

.PHONY: build
build: ui core ## Build UI and core into one binary

.PHONY: run
run: build ## Build and run locally with the OTel Demo description
	$(BIN) serve --listen-address $(ADDR) --description $(DESCRIPTION)

## ---- Quality ----

.PHONY: test
test: test-go test-web ## Run all tests and checks

.PHONY: test-go
test-go: ## Go vet and tests
	go vet ./...
	go test ./...

.PHONY: test-web
test-web: web/node_modules ## Web typecheck and unit tests
	cd web && pnpm typecheck
	cd web && pnpm test

.PHONY: lint
lint: lint-go lint-web ## Lint Go and web

.PHONY: lint-go
lint-go: ## gofmt check, go vet, and golangci-lint when installed
	@test -z "$$(gofmt -l $$(git ls-files '*.go' 2>/dev/null || find . -name '*.go' -not -path './web/node_modules/*'))" || (gofmt -l . ; echo "run gofmt -w"; exit 1)
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else echo "golangci-lint not installed; skipped"; fi

.PHONY: lint-web
lint-web: web/node_modules ## ESLint
	cd web && pnpm lint

CHART := deploy/helm/kamerton

$(CHART)/charts: $(CHART)/Chart.yaml $(CHART)/Chart.lock
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
	helm repo add grafana-community https://grafana-community.github.io/helm-charts --force-update
	helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts --force-update
	helm dependency build $(CHART)
	@touch $@

.PHONY: helm-deps
helm-deps: $(CHART)/charts ## Download the pinned chart dependencies (adds their Helm repositories)

.PHONY: helm-lint
helm-lint: helm-deps ## Lint and render the Helm chart (never installs)
	helm lint --strict $(CHART)
	helm template kamerton $(CHART) --namespace kamerton >/dev/null

.PHONY: check
check: lint test helm-lint ## Everything CI should run

## ---- Container ----

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE) .

.PHONY: clean
clean: ## Remove build outputs
	rm -rf bin
	find web/dist -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
