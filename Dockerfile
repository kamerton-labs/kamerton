# Copyright 2026 The Kamerton Authors
# SPDX-License-Identifier: AGPL-3.0-only
#
# One image, one binary: the web UI is built first and embedded into the Go
# binary with go:embed.

# ---- Web UI ----
# The UI and the Go compiler run on the build machine's platform; only the
# resulting binary is built for the target platform.
FROM --platform=$BUILDPLATFORM node:24.18.0-alpine AS ui
RUN npm install -g pnpm@12.4.1
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---- Core ----
FROM --platform=$BUILDPLATFORM golang:1.25.5-alpine AS core
ENV GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
ARG VERSION=0.2.3
ARG COMMIT=unknown
ARG TARGETOS=linux
ARG TARGETARCH
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X github.com/kamerton-labs/kamerton/internal/version.Version=${VERSION} -X github.com/kamerton-labs/kamerton/internal/version.Commit=${COMMIT}" \
      -o /out/kamerton ./cmd/kamerton

# ---- Runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=core /out/kamerton /usr/local/bin/kamerton
USER 65532:65532
EXPOSE 8080 8081 8082
ENTRYPOINT ["/usr/local/bin/kamerton"]
CMD ["serve"]
