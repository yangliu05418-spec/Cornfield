# syntax=docker/dockerfile:1.7
FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend/ .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/modelctl ./cmd/modelctl && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/adminctl ./cmd/adminctl && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/canaryctl ./cmd/canaryctl

FROM gcr.io/distroless/static-debian12:nonroot@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b AS api
COPY --from=build /out/api /api
USER 65532:65532
ENTRYPOINT ["/api"]

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS worker
ARG LIBVIPS_TOOLS_VERSION=8.14.1-3+deb12u3
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates "libvips-tools=${LIBVIPS_TOOLS_VERSION}" && rm -rf /var/lib/apt/lists/* && \
    useradd --uid 65532 --no-create-home --shell /usr/sbin/nologin studio
COPY --from=build /out/worker /worker
USER 65532:65532
ENTRYPOINT ["/worker"]

# Use the exact PostgreSQL server distribution for psql/pg_dump/pg_restore.
# Debian bookworm's unversioned postgresql-client currently resolves to an
# older major and is not a safe restore or migration client for PostgreSQL 18.
FROM postgres:18.4-bookworm@sha256:882236b897e39051d2368c5ccc6cda944904723506b2dfc97f2a8f5bc9afa382 AS tools
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* && \
    useradd --uid 65532 --no-create-home --shell /usr/sbin/nologin studio
COPY --from=build /out/migrate /out/modelctl /out/adminctl /out/canaryctl /usr/local/bin/
COPY ops/db-bootstrap.sh /usr/local/bin/db-bootstrap
RUN chmod 0755 /usr/local/bin/db-bootstrap && rm /usr/local/bin/gosu
WORKDIR /app
USER 65532:65532
