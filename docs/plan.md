# Phase 0 plan: delivery plan & decisions

**Status:** awaiting approval. No further code will be written until this plan and the decisions below
are confirmed.

| Deliverable | Where |
|---|---|
| Architecture overview, system context & component diagrams | [architecture.md §1–2](architecture.md) |
| Pipeline execution, deployment/rollback, auth sequences | [architecture.md §3](architecture.md#3-key-sequences) |
| Folder tree | [architecture.md §4](architecture.md#4-folder-tree) |
| API conventions (pagination, idempotency, locking, tenancy, rate limits, CORS) | [architecture.md §6](architecture.md#6-cross-cutting-api-conventions) |
| Security (OWASP mapping), NFR plan, Future Work | [architecture.md §7–9](architecture.md#7-security-owasp-top-10-mapping) |
| Full API endpoint list | [api.md](api.md) |
| ERD, all tables, DB roles, seed, backup & restore | [database.md](database.md) |
| RBAC permission matrix | [rbac.md](rbac.md) |
| i18n plan & glossary | [i18n.md](i18n.md) |

## Code written ahead of approval

The original instructions arrived in two parts. The first part did not include "Phase 0 = plan only",
so a foundation skeleton was implemented before the plan was approved. It is fully tested but must be
**adjusted** to this plan:

| Exists | Adjustment needed |
|---|---|
| API core: config, slog, chi, error envelope, validation, i18n negotiation, `/healthz` `/readyz` `/metrics` `/docs`, OTel, trusted-proxy client IP, UUID request IDs | Add CORS allow-list, rate limiting, per-route timeouts |
| Migration 000001 (users, orgs, teams, tokens, audit log) + sqlc | Replace `sessions` with `refresh_tokens`, add lockout columns, `version` columns, `mfa_challenges`, `invitations`, `idempotency_keys`; create least-privilege roles |
| Web shell: React 19, Tailwind v4, shadcn, i18next EN/KM, Khmer fonts & typography, formatters, orval client, Vitest + Playwright | Replace with the sidebar layout, org switcher, dark/light mode and auth-aware fetcher |
| docker-compose (postgres, api, web), Makefile, Dockerfiles, CI | Add runner, prometheus, grafana, mailpit; rename Makefile targets to `dev test lint fmt gen migrate-up migrate-down seed build docker i18n-check` |

Option A (recommended): keep it and adjust it in Module 1. Option B: discard it and start fresh after approval.

## Module delivery order

Each module ships as: migrations → sqlc queries → repository → service → handler → tests → OpenAPI →
regenerated client → frontend pages → EN + KM translations → changelog.

| # | Module | Also delivers |
|---|---|---|
| 1 | Auth & users | Compose stack complete (incl. mailpit), `make dev`, rate limiting, CORS, River + email jobs, login/2FA/profile pages, app layout (sidebar, org switcher, theme, language) |
| 2 | RBAC | `authz` package, permission matrix tests, route guards, org/team/member settings pages, tenant-isolation test harness |
| 3 | Projects & repositories | GitHub/GitLab providers, webhooks (HMAC), environments & protection rules, idempotency middleware |
| 4 | CI/CD pipelines | `.opshub.yml` parser + DAG engine, runs UI with live terminal log viewer (SSE), approvals |
| 5 | Runner agent | `cmd/runner`, Docker executor with limits, secret masking, runner registration docs, runner in compose |
| 6 | Deployments | SSH / Docker / Kubernetes targets, rolling + blue/green, health checks, rollback, release history |
| 7 | Infrastructure | Assets, agent heartbeat, metrics charts, SSL expiry tracking |
| 8 | Secrets | Envelope encryption, versions, rotation, audited reads, KEK rotation CLI |
| 9 | Monitoring & alerts | Uptime checks, alert rules, silences, escalation, Telegram/Slack/Email/Webhook |
| 10 | Logs | Ingest, full-text search, retention job |
| 11 | Audit log | UI, filters, CSV export |
| 12 | Dashboard | Pipeline stats, DORA metrics, Grafana dashboard for platform metrics, backup job, Helm chart, k6 load tests |

## Definition of done (every module)

Compiles; golangci-lint, ESLint and tsc pass; all tests pass (service coverage ≥ 70 %); migrations up
and down work; OpenAPI updated and client regenerated; every new string in `en` + `km` with the Khmer
layout checked (Playwright at 3 widths plus screenshots); permissions enforced and tested; audit events
recorded; zero HIGH/CRITICAL findings; short changelog.

## Decisions needed before Module 1

| # | Question | Proposed default |
|---|---|---|
| D1 | Keep the pre-built foundation code (Option A) or discard it (Option B)? | A |
| D2 | How is the first platform admin created? | Env bootstrap: `OPSHUB_BOOTSTRAP_ADMIN_EMAIL`; that user becomes platform admin on registration. Public sign-up can be disabled with `OPSHUB_ALLOW_SIGNUP=false` |
| D3 | The "Monorepo layout" section of the spec was cut off in both messages. Was there a required layout? | Use the tree in [architecture.md §4](architecture.md#4-folder-tree) |
| D4 | Infra heartbeat agent: separate tiny binary (`cmd/agent`) or a mode of the runner binary (`opshub-runner agent`)? | Mode of the runner binary: one binary to ship and update |
| D5 | Artifact/cache storage for MVP | Local filesystem volume behind a `BlobStore` interface; S3-compatible storage in Future Work. Note: several API replicas would then need a shared volume (RWX) |
| D6 | OIDC SSO providers: configured by the operator (env vars) or per organization in the UI? | Operator-level via env for MVP; per-org SSO in Future Work |
| D7 | JWT signing | Ed25519 (EdDSA) key from env/secret, with `kid` for rotation |
| D8 | Kubernetes deploys need `client-go` (large but the standard, well-maintained client) | Accept `client-go`; SSH via `golang.org/x/crypto/ssh`; Docker via the Engine HTTP API directly (no moby SDK) |
| D9 | Who can self-register? | Anyone can register, but can only see orgs they create or are invited to |
