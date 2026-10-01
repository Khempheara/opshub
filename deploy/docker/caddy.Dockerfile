# syntax=docker/dockerfile:1.7
# Caddy for the Docker install (deploy/install, HTTPS in front of OpsHub), built from source with
# the current Go and up-to-date libraries: the official image lags behind on both, and Trivy
# flags them. Runs unprivileged; compose lets it bind 80 and 443 (net.ipv4.ip_unprivileged_port_start).
FROM golang:1.27-alpine AS build
ARG CADDY_VERSION=v2.11.6
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    printf 'package main\n\nimport (\n\tcaddycmd "github.com/caddyserver/caddy/v2/cmd"\n\t_ "github.com/caddyserver/caddy/v2/modules/standard"\n)\n\nfunc main() { caddycmd.Main() }\n' > main.go \
 && go mod init opshub/caddy \
 && go get github.com/caddyserver/caddy/v2@${CADDY_VERSION} \
 && go get golang.org/x/crypto@latest golang.org/x/net@latest golang.org/x/text@latest google.golang.org/grpc@latest \
 && go mod tidy \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/caddy . \
 && mkdir -p /out/data /out/config

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/caddy /usr/bin/caddy
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/config /config
ENV XDG_DATA_HOME=/data XDG_CONFIG_HOME=/config
USER nonroot:nonroot
EXPOSE 80 443 443/udp
ENTRYPOINT ["/usr/bin/caddy"]
CMD ["run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
