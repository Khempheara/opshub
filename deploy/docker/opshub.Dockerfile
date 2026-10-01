# syntax=docker/dockerfile:1.7
# All-in-one OpsHub image (ghcr.io/<owner>/opshub): PostgreSQL 17, the API, the web UI, Caddy
# and the backup tools in one container, run with a single `docker run` (docs/install-docker.md).
# Everything runs as the unprivileged postgres user (uid 70); data lives in the /data volume.
FROM golang:1.27-alpine AS api
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opshub-api ./cmd/api

FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
COPY api/openapi.yaml /src/api/openapi.yaml
RUN npm run build

# Caddy built from source with current libraries (the same build as deploy/docker/caddy.Dockerfile).
FROM golang:1.27-alpine AS caddy
ARG CADDY_VERSION=v2.11.6
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    printf 'package main\n\nimport (\n\tcaddycmd "github.com/caddyserver/caddy/v2/cmd"\n\t_ "github.com/caddyserver/caddy/v2/modules/standard"\n)\n\nfunc main() { caddycmd.Main() }\n' > main.go \
 && go mod init opshub/caddy \
 && go get github.com/caddyserver/caddy/v2@${CADDY_VERSION} \
 && go get golang.org/x/crypto@latest golang.org/x/net@latest golang.org/x/text@latest google.golang.org/grpc@latest \
 && go mod tidy \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/caddy .

FROM postgres:18-alpine
# nginx serves the UI, age encrypts backups, tini is PID 1. gosu (the postgres image's root
# entrypoint helper) isn't used: this image never runs as root.
RUN apk upgrade --no-cache && apk add --no-cache nginx age tini \
 && rm -f /usr/local/bin/gosu \
 && mkdir -p /data /etc/opshub /usr/local/lib/opshub && chown 70:70 /data && chmod 700 /data
COPY --from=caddy /out/caddy /usr/local/bin/caddy
COPY --from=api /out/opshub-api /usr/local/bin/opshub-api
COPY --from=web /src/web/dist /usr/share/opshub/web
COPY deploy/docker/nginx.conf.template /etc/opshub/site.conf.template
COPY deploy/aio/nginx.conf deploy/aio/Caddyfile /etc/opshub/
COPY --chmod=755 deploy/install/postgres/init-roles.sh /usr/local/lib/opshub/init-roles.sh
COPY --chmod=755 deploy/backup/backup.sh /usr/local/bin/opshub-backup
COPY --chmod=755 deploy/backup/restore.sh /usr/local/bin/opshub-restore
COPY --chmod=755 deploy/backup/schedule.sh /usr/local/bin/opshub-backup-schedule
COPY --chmod=755 deploy/aio/start.sh /usr/local/bin/opshub-start
COPY --chmod=755 deploy/aio/opshub /usr/local/bin/opshub
USER 70:70
VOLUME /data
# 8080: the UI (or, with OPSHUB_DOMAIN, Caddy's HTTP side); 8443: HTTPS with OPSHUB_DOMAIN.
EXPOSE 8080 8443
HEALTHCHECK --interval=15s --timeout=5s --start-period=90s --retries=5 CMD ["/usr/local/bin/opshub", "health"]
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/opshub-start"]
