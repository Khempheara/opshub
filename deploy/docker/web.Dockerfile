# syntax=docker/dockerfile:1.7
FROM node:24-alpine AS build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
COPY api/openapi.yaml /src/api/openapi.yaml
RUN npm run build

# Unprivileged nginx (runs as uid 101, listens on 8080).
FROM nginxinc/nginx-unprivileged:1.29-alpine
# Pick up Alpine security fixes newer than the base image; curl is not needed at runtime.
USER root
RUN apk upgrade --no-cache && (apk del --no-cache curl || true)
USER 101
COPY deploy/docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/web/dist /usr/share/nginx/html
EXPOSE 8080
