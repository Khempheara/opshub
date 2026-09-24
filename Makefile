# OpsHub developer commands. `make help` lists them.
SHELL := /bin/bash
.DEFAULT_GOAL := help

SQLC_VERSION   ?= 1.31.1
GOLANGCI_IMAGE ?= golangci/golangci-lint:latest
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMPOSE        := docker compose

-include .env
export

##@ Development
.env: ## Create .env from .env.example with freshly generated keys
	@grep -vE '^OPSHUB_(MASTER|JWT)_KEYS=' .env.example > .env
	@go run ./cmd/api keys generate >> .env
	@echo "Created .env with new OPSHUB_MASTER_KEYS / OPSHUB_JWT_KEYS (appended at the end)"

.PHONY: dev
dev: .env ## Build and start the whole stack (web, api, postgres, mailpit, prometheus, grafana)
	$(COMPOSE) up -d --build --wait
	@echo
	@echo "  OpsHub      http://localhost:$${OPSHUB_WEB_PORT:-3000}"
	@echo "  API docs    http://localhost:$${OPSHUB_API_PORT:-8080}/docs"
	@echo "  Mailpit     http://localhost:$${OPSHUB_MAILPIT_PORT:-8025}"
	@echo "  Grafana     http://localhost:$${OPSHUB_GRAFANA_PORT:-3001}"
	@echo "  Prometheus  http://localhost:$${OPSHUB_PROMETHEUS_PORT:-9090}"
	@echo "  Demo data:  make seed"

.PHONY: dev-api
dev-api: .env ## Run the API natively (hot path for backend work; needs `make dev-deps`)
	go run -ldflags "-X main.version=$(VERSION)" ./cmd/api

.PHONY: dev-web
dev-web: ## Run the Vite dev server with HMR (proxies /api to OPSHUB_API_URL, default :8080)
	cd web && npm run dev

.PHONY: dev-deps
dev-deps: .env ## Start only postgres + mailpit (for dev-api / dev-web)
	$(COMPOSE) up -d --wait postgres mailpit

.PHONY: down
down: ## Stop the stack (keeps data; `make clean` removes volumes)
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete its volumes (all local data)
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Follow API logs
	$(COMPOSE) logs -f api

##@ Database
.PHONY: migrate-up
migrate-up: .env ## Apply all migrations (OpsHub + River)
	go run ./cmd/api migrate up

.PHONY: migrate-down
migrate-down: .env ## Roll back ALL migrations (development only; destroys data)
	go run ./cmd/api migrate down

.PHONY: seed
seed: .env ## Create the demo organization and users (EN + KM demo text)
	go run ./cmd/api seed

##@ Code generation
.PHONY: gen
gen: ## Regenerate sqlc (Go data access) and orval (TypeScript client)
	docker run --rm -v "$(CURDIR)":/src -w /src sqlc/sqlc:$(SQLC_VERSION) generate
	cd web && npm run api:generate

##@ Quality
.PHONY: test
test: ## Go unit + integration tests (Docker), frontend unit tests, i18n check
	go test -race -count=1 ./...
	cd web && npm run i18n:check && npm test

.PHONY: coverage
coverage: ## Fail if service-package coverage is below 70%
	./scripts/coverage.sh

.PHONY: e2e
e2e: ## Playwright: mocked UI tests + full-stack tests against `make dev` (+ `make seed`)
	cd web && E2E_BASE_URL=http://localhost:$${OPSHUB_WEB_PORT:-3000} E2E_MAILPIT_URL=http://localhost:$${OPSHUB_MAILPIT_PORT:-8025} \
		E2E_SEED_PASSWORD=$${OPSHUB_SEED_PASSWORD:-angkor-wat-sunrise-2026} npx playwright test

.PHONY: lint
lint: ## golangci-lint (incl. gosec), ESLint, TypeScript
	docker run --rm -v "$(CURDIR)":/app -v "$$(go env GOMODCACHE)":/go/pkg/mod -w /app $(GOLANGCI_IMAGE) golangci-lint run ./...
	cd web && npm run lint && npm run typecheck

.PHONY: fmt
fmt: ## Format Go (gofmt + goimports) and fix ESLint-fixable issues
	docker run --rm -v "$(CURDIR)":/app -v "$$(go env GOMODCACHE)":/go/pkg/mod -w /app $(GOLANGCI_IMAGE) golangci-lint fmt ./...
	cd web && npx eslint . --fix

.PHONY: i18n-check
i18n-check: ## Fail if Khmer is missing keys that exist in English
	cd web && npm run i18n:check
	go test -count=1 -run 'TestLocaleParity|TestAllCodesTranslated|TestRenderAllTemplatesInBothLocales' ./internal/i18n ./internal/apperr ./internal/mail

.PHONY: security
security: ## govulncheck + gosec + npm audit
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/securego/gosec/v2/cmd/gosec@latest -quiet -exclude-generated ./...
	cd web && npm audit --audit-level=high

##@ Build
.PHONY: build
build: ## Build the API binary (bin/opshub-api) and the web bundle (web/dist)
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/opshub-api ./cmd/api
	cd web && npm run build

.PHONY: docker
docker: ## Build the Docker images
	docker build -f deploy/docker/api.Dockerfile --build-arg VERSION=$(VERSION) -t opshub-api:$(VERSION) .
	docker build -f deploy/docker/web.Dockerfile -t opshub-web:$(VERSION) .

.PHONY: help
help:
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_.-]+:.*##/ { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
