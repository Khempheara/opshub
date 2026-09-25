# syntax=docker/dockerfile:1.7
# opshub-runner: runs pipeline jobs as sibling containers through the host's Docker socket.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opshub-runner ./cmd/runner
# State directory (runner token config), owned by the non-root user.
RUN mkdir -p /out/state

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/opshub-runner /opshub-runner
COPY --from=build --chown=65532:65532 /out/state /var/lib/opshub-runner
ENV OPSHUB_RUNNER_CONFIG=/var/lib/opshub-runner/config.json
VOLUME ["/var/lib/opshub-runner"]
USER nonroot:nonroot
ENTRYPOINT ["/opshub-runner"]
CMD ["run"]
