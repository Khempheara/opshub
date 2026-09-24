# syntax=docker/dockerfile:1.7
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opshub-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/opshub-api /opshub-api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/opshub-api"]
