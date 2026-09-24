# OpsHub developer commands. Run `make help` for the list.
SHELL := /bin/bash
.DEFAULT_GOAL := help

SQLC_VERSION   ?= 1.31.1
MIGRATE_VERSION ?= v4.19.1
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DATABASE_URL   ?= $(OPSHUB_DATABASE_URL)

-include .env
export

##@ Development
.PHONY: dev-db
dev-db: ## Start PostgreSQL in Docker
	docker compose up -d --wait postgres

.PHONY: dev-api
dev-api: dev-db ## Run the API server (auto-migrates)
	go run -ldflags "-X main.version=$(VERSION)" ./cmd/api

.PHONY: dev-web
dev-web: ## Run the Vite dev server (proxies /api to :8080)
	cd web && npm run dev

.PHONY: up
up: ## Build and run the whole stack in containers (http://localhost:3000)
	docker compose --profile app up -d --build --wait

.PHONY: down
down: ## Stop all containers
	docker compose --profile app down

##@ Code generation
.PHONY: generate
generate: sqlc api-client ## Run all code generators

.PHONY: sqlc
sqlc: ## Generate Go data access code from db/queries
	docker run --rm -v "$(CURDIR)":/src -w /src sqlc/sqlc:$(SQLC_VERSION) generate

.PHONY: api-client
api-client: ## Generate the TypeScript API client from api/openapi.yaml
	cd web && npm run api:generate

##@ Database
.PHONY: migrate-new
migrate-new: ## Create a migration pair: make migrate-new name=add_projects
	@test -n "$(name)" || (echo "usage: make migrate-new name=<snake_case>"; exit 1)
	docker run --rm -v "$(CURDIR)/db/migrations":/migrations migrate/migrate:$(MIGRATE_VERSION) \
		create -ext sql -dir /migrations -seq -digits 6 $(name)

.PHONY: migrate-up
migrate-up: ## Apply all migrations
	docker run --rm --network host -v "$(CURDIR)/db/migrations":/migrations migrate/migrate:$(MIGRATE_VERSION) \
		-path /migrations -database "$(DATABASE_URL)" up

.PHONY: migrate-down
migrate-down: ## Roll back the last migration
	docker run --rm --network host -v "$(CURDIR)/db/migrations":/migrations migrate/migrate:$(MIGRATE_VERSION) \
		-path /migrations -database "$(DATABASE_URL)" down 1

##@ Quality
.PHONY: test
test: test-go test-web ## Run all tests

.PHONY: test-go
test-go: ## Go unit + integration tests (integration needs Docker)
	go test -race -count=1 ./...

.PHONY: test-go-short
test-go-short: ## Go unit tests only
	go test -race -short ./...

.PHONY: test-web
test-web: ## Frontend unit tests + i18n key check
	cd web && npm run i18n:check && npm test

.PHONY: e2e
e2e: ## Playwright end-to-end tests
	cd web && npx playwright test

.PHONY: lint
lint: ## Lint Go and TypeScript
	golangci-lint run ./...
	cd web && npm run lint && npm run typecheck

.PHONY: security
security: ## govulncheck + gosec
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/securego/gosec/v2/cmd/gosec@latest -exclude-generated ./...

##@ Build
.PHONY: build
build: ## Build the API binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/opshub-api ./cmd/api

.PHONY: images
images: ## Build Docker images
	docker build -f deploy/docker/api.Dockerfile --build-arg VERSION=$(VERSION) -t opshub-api:$(VERSION) .
	docker build -f deploy/docker/web.Dockerfile -t opshub-web:$(VERSION) .

.PHONY: help
help:
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
