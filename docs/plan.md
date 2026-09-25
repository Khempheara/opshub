# Phase 0 plan: delivery plan & decisions

**Status:** approved (defaults D1–D9 accepted). Module 1 complete — see [CHANGELOG.md](../CHANGELOG.md).

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

## Decisions made during delivery

| # | Module | Decision |
|---|---|---|
| M2-1 | 2 | Invitees can register with their invitation token even when `OPSHUB_ALLOW_SIGNUP=false` |
| M3-1 | 3 | Git hosts: github.com, gitlab.com, GitHub Enterprise and self-managed GitLab (https `base_url`); internal addresses need `OPSHUB_OUTBOUND_ALLOWED_CIDRS` |
| M3-2 | 3 | Webhooks: OpsHub creates the hook with the access token; if it can't, the repository connects in manual mode and the UI shows the URL + secret once |
| M3-3 | 3 | Until pipelines exist (Module 4), valid webhooks are verified, de-duplicated and recorded only |
| M4-1 | 4 | Pipelines ship before runners: the runner-side job lifecycle is a service exercised by tests; Module 5 adds its HTTP endpoints and the runner binary |
| M4-2 | 4 | Cron triggers use an in-house 5-field cron parser (no dependency) |
| M4-3 | 4 | Artifacts and cache (blob store, upload/download) arrive with runners in Module 5; Module 4 validates and stores their definitions |
| M4-4 | 4 | One pipeline per project (`.opshub.yml`); runs snapshot the definition. An invalid file on push creates a failed run listing the problems; a manual run with an invalid file is refused (`PIPELINE_INVALID`) |
| M5-1 | 5 | Runners get source through OpsHub (`/runner/jobs/{id}/source` proxies the provider's tarball of the run's commit), so Git credentials never leave the server; no repository → empty workspace |
| M5-2 | 5 | Docker executor: one named volume per job at `/workspace`, one container per step (`/bin/sh -ec`), never privileged, `no-new-privileges`, optional CPU/memory limits, PID limit; the agent talks to the Engine API over the unix socket with the standard library (per D8) |
| M5-3 | 5 | Local development runner is an opt-in compose profile (`make runner`) using the host Docker socket; documented as root-equivalent on the host |
| M5-4 | 5 | Heartbeats every 10 s; a runner silent for 30 s is offline and its jobs fail `runner_lost`; jobs assigned to a runner that it doesn't report fail the same way (agent restarts, lost assignments) |
| M5-5 | 5 | One artifact archive (gzip tar) per job, default 7 days; artifacts restore into jobs that `need` the producer; cache failures never fail a job; secrets/masks fields are in the job payload but empty until Module 8 |
| M6-1 | 6 | A deployment's version is a container image. Kubernetes and Docker targets set that image; SSH targets run the operator's command with `$OPSHUB_VERSION` |
| M6-2 | 6 | Deployments run in an OpsHub River worker (credentials never leave the server); targets on private networks need `OPSHUB_OUTBOUND_ALLOWED_CIDRS`. A worker crash marks the deployment `interrupted` rather than retrying a non-idempotent change |
| M6-3 | 6 | Blue/green on Kubernetes only (two Deployments, Service selector switch); SSH and Docker targets roll host by host / replica by replica with automatic revert |
| M6-4 | 6 | Kubernetes through its REST API with the standard library instead of client-go (D8 allowed client-go); kubeconfigs with a token or embedded client certificate, no exec/auth-provider plugins |
| M6-5 | 6 | Pipeline `deploy:` jobs are performed by OpsHub, not runners: `deploy.version` (default `${DEPLOY_VERSION}`) with `${VAR}` expansion; a deploy job can't have steps (`deploy_with_steps`) |
| M6-6 | 6 | Manual deploys to environments that require approvals are refused (use a pipeline, which has the approval gate); allowed roles still apply. Rollback needs an allowed role but no approvals |
| M6-7 | 6 | SSH host keys (SSH targets and Docker over SSH) are pinned after "Test connection"; unpinned or changed keys are refused before any credentials are sent |
| M6-8 | 6 | Docker targets on the API host's own socket only when the operator sets `OPSHUB_DEPLOY_LOCAL_DOCKER=true` |
| M7-1 | 7 | The infrastructure agent is a mode of the runner binary (`opshub-runner agent`) with its own token type (`ohi_…`, one per server, rotation revokes); it reports metrics on Linux only |
| M7-2 | 7 | OpsHub probes domain certificates itself (daily, hourly after a failure, "Check now"), through the SSRF guard; there is no agent-side certificate discovery |
| M7-3 | 7 | Module 7 shows metrics and certificate expiry in the UI only; thresholds and notifications arrive with monitoring & alerts in Module 9 |
| M7-4 | 7 | Metric partitions are managed by a `SECURITY DEFINER` function owned by the migration role, so the application role stays DML-only |
| M7-5 | 7 | Retention: raw samples for the current and previous month (charts read them up to 30 days back), hourly average/peak rollups for 400 days; heartbeat timestamps are the server's |
| M8-1 | 8 | Jobs receive only the secrets they list (`secrets:` in `.opshub.yml`); an environment's secret wins over an all-environments one of the same name; names are checked when the job becomes ready (`secret_not_found`) |
| M8-2 | 8 | Pull-request runs never receive secrets (`secrets_not_allowed`): their pipeline file comes from the pull request |
| M8-3 | 8 | Rotation destroys older values; history keeps who and when. There is no reveal endpoint (rbac.md over the api.md draft) |
| M8-4 | 8 | Developers change secrets of unprotected environments only; all-environments secrets count as protected |
| M8-5 | 8 | `opshub-api keys rotate` re-encrypts all key-ring data (secret DEKs, 2FA seeds, Git tokens, webhook secrets, deploy credentials, job masks), so an old master key can be removed |
| M8-6 | 8 | The API masks stored job output too, with the job's values sealed in its token row; a secret wins over a variable of the same name in the job environment |
