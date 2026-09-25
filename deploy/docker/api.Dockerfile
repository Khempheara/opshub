# syntax=docker/dockerfile:1.7
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opshub-api ./cmd/api
# Blob storage for artifacts and caches (OPSHUB_BLOB_DIR), owned by the non-root user.
RUN mkdir -p /out/data/blobs

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/opshub-api /opshub-api
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/opshub-api"]
