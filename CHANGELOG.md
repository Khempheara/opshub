# Changelog

## Module 6 — Deployments

### Built

**Backend** (`internal/deploy`)
- Deploy targets per organization: SSH hosts, Docker hosts (over SSH, TCP+TLS, or — if the
  operator allows — the API host's socket) and Kubernetes clusters. Per-kind validation that
  reports every invalid field at once; credentials encrypted with the key ring (row id as
  associated data) and write-only; audited create/update/delete.
- Test connection: Docker/Kubernetes version and resources; SSH host key fingerprints, pinned
  with one click. Unpinned or changed host keys are refused before any credentials are sent.
- Deployments of a container image to an environment, run by a River worker (`deploys` queue):
  - SSH: the operator's command in host batches, with `$OPSHUB_VERSION` and friends.
  - Docker: replica-by-replica swap, keeping `<name>-previous` for an instant revert.
  - Kubernetes rolling: image patch and rollout watch.
  - Kubernetes blue/green: idle color created or updated, Service selector switched, switched
    back if unhealthy.
  - Kubernetes is reached through its REST API with the standard library: no client-go.
- HTTP health checks through the SSRF-safe client; automatic revert on failure; results, failure
  reason and `reverted` stored. One deployment per environment at a time. A crash marks the
  deployment `interrupted` instead of retrying.
- Protection rules: allowed roles for manual deploys and rollbacks; environments that require
  approvals only take pipeline deploys.
- Release history with filters and "current release" per environment; one-click rollback of the
  current release to the one before it (Idempotency-Key); live status and log over SSE.
- Pipelines: a job's `deploy:` block (new `version`, default `${DEPLOY_VERSION}`, `${VAR}`
  expansion) is performed by OpsHub when the job becomes ready, including after approvals;
  runners never claim deploy jobs; new rule `deploy_with_steps`; job reasons `deploy_failed`,
  `target_not_found`, `deploy_invalid`.
- Shared `internal/dockerapi` (the runner's Engine client, extended) and `safehttp` dialer for
  non-HTTP connections. `golang.org/x/crypto/ssh` comes from an existing dependency.
- Migration `000006_deployments`; 11 endpoints; 9 error codes (EN + KM); OpenAPI 0.7.0; demo
  seed: `demo-k8s` (unreachable on purpose) and a release history.

**Frontend**
- Organization → Deploy targets: list with last test result, per-kind create/edit form
  (write-only credentials), Test connection with "Trust this key", delete.
- Project → Deployments tab: live release per environment, filterable history, Deploy dialog
  (image validation, blue/green only for Kubernetes); deployment page with facts, health
  checks, live log (download) and a confirmed Roll back.
- New `deploy` translation namespace (EN + KM); generalized log viewer and SSE refresh that
  can't be outrun by a deployment finishing before the page loads.

### Quality

- Go: config and kubeconfig validation, an in-process SSH server (pinning, commands, revert),
  a fake Kubernetes API (rolling, stalled rollouts, blue/green switch and switch back), real
  Docker (replace, failed release restored, redeploy), and service tests for targets, deploys,
  protection, rollback, interruption and pipeline deploy jobs (incl. after approval). Tenant
  isolation over all 11 routes with a coverage guard, an idempotent deploy and the SSE stream
  over HTTP. Deploy 74.8 %; service coverage 76.0 %.
- Web: 82 Vitest tests (+10 target form); 41 Playwright tests (+5: history and a manual deploy,
  protected environment, rollback, viewer read-only, target management, Khmer layout).
- golangci-lint, gosec, govulncheck, ESLint, TypeScript, i18n check clean; Trivy 0
  HIGH/CRITICAL; migrations up/down/up; sqlc and orval deterministic; OpenAPI ↔ routes test.

### Next — Module 7: Infrastructure

Asset inventory, agent heartbeats, metrics charts and SSL certificate expiry tracking.

## Module 5 — Runner agent

### Built

**Backend**
- Runners (`internal/runners`): one-time registration tokens (`ohr_reg_…`, 1 h, optional extra
  labels) exchanged for runner tokens (`ohr_…`); list with online/offline/disabled status and
  running jobs; rename, relabel, max concurrency and disable with If-Match; delete (revokes the
  token, fails running jobs as `runner_lost`). Tokens are stored as SHA-256 hashes; every
  management action is audited.
- Runner API: heartbeat (every 10 s; answers which jobs to stop), long-poll job request (≤ 30 s)
  that respects labels and `max_concurrency` under concurrent requests (per-runner row lock), and
  per-job tokens (`ohj_…`) issued in the claim transaction and valid only while the job runs.
  Step reports, results, idempotent log chunks, source tarball proxied through OpsHub (Git
  credentials stay on the server), artifact upload, dependency artifacts from `needs` jobs,
  project-scoped cache get/put.
- Local blob store (`internal/blob`, `OPSHUB_BLOB_DIR`) for artifacts and caches with size limits;
  housekeeping every 30 s fails jobs of silent runners, removes expired artifacts and evicts caches
  beyond the project quota (LRU). Jobs a runner stops reporting also fail as `runner_lost`.
- GitHub/GitLab archive download follows redirects without forwarding the token to other hosts.
- Machine tokens pass through user authentication as anonymous and get their own rate-limit
  bucket.
- Migration `000005_runners`; 15 new endpoints (6 for people, 9 for runners); 7 new error codes
  (EN + KM); OpenAPI 0.6.0 (runner endpoints are excluded from the web client).

**Agent** (`cmd/runner`, `internal/runner` — no server imports, no Docker SDK)
- `opshub-runner register` / `run` (or self-registration from `OPSHUB_REGISTRATION_TOKEN`);
  config file mode 0600.
- Docker executor over the Engine API: image pull, a volume per job, one container per step
  (`/bin/sh -ec`), never privileged, `no-new-privileges`, CPU/memory/PID limits; checkout,
  dependency artifacts and cache restore; artifact and cache upload; timeouts and server-side
  cancel kill the container; crash leftovers are cleaned up on start.
- Log shipping batched per second / 64 KiB at line ends, masked, NUL-safe; graceful shutdown
  keeps heartbeats going while running jobs finish.
- Distroless non-root image (8.7 MB); compose profile `runner` and `make runner` for local
  development using the host Docker socket.

**Frontend**
- Organization → Runners (Developers and up): status, labels, capacity, version, last seen
  (refreshes every 10 s); register dialog showing the token once with ready-to-copy binary and
  Docker commands; edit, disable/enable and delete for Owners and Admins.
- Job panel: artifacts with size, expiry and download. Shared authenticated file download
  (also used for log downloads).
- New `runner` translation namespace (EN + KM).

### Quality

- Go: registration, auth, capacity under concurrent claims, long-poll, heartbeats (cancel list,
  orphaned jobs), deletion, housekeeping, artifacts, dependencies, cache and quota; tenant
  isolation and token checks over every new route with a route-coverage guard; end-to-end tests
  that run real jobs in Docker through the HTTP API with the agent's executor (checkout, steps,
  artifacts, cache, failing step, cancel). Runners 80.8 %, blob 80.0 %; service coverage 79.3 %.
- Web: 72 Vitest tests (+10 runner form rules); 36 Playwright tests (+1: register, online,
  edit, disable, delete, Khmer layout on phone and desktop).
- golangci-lint, gosec, govulncheck, ESLint, TypeScript, i18n check clean; Trivy 0
  HIGH/CRITICAL on the runner image; migrations up/down/up; sqlc and orval deterministic;
  OpenAPI ↔ routes drift test.

### Next — Module 6: Deployments

SSH, Docker and Kubernetes targets, rolling and blue/green strategies, health checks, rollback
and release history, driven by the pipeline's `deploy` blocks.

## Module 4 — CI/CD pipelines

### Built

**Backend**
- `.opshub.yml` parser (`internal/pipeline/spec`): stages, steps, `needs` (implicit: every job of
  earlier stages), `when` (on_success, on_failure, always, manual), environments, variables,
  runner labels, timeouts, artifacts, cache and deploy blocks. Every problem has a line, column,
  path and a stable rule code; cycles are reported with their path. The product spec's example
  parses as written.
- Triggers: push (branch filters), tags, pull requests (target-branch filters; opened, reopened,
  new commits), manual runs, and cron schedules (in-house 5-field parser, UTC, default branch).
- Engine: runs and jobs with attempts, a job graph advanced under a per-run lock, skip
  cascades, derived run status, cancel, retry job, re-run failed jobs, full re-run from the
  definition snapshot.
- Approval gates: `when: manual` and protected environments (required approvals, allowed
  branches, allowed roles "or higher"); the run's starter can't approve; one decision per
  person; a rejection fails the job.
- Webhook pushes and pull requests start runs through a River job that reads `.opshub.yml` at
  the commit; an invalid file creates a failed run listing the problems. Retried events don't
  duplicate runs.
- Runner-side lifecycle (claim with labels and `SKIP LOCKED`, steps, logs, finish) as a service
  exercised by tests; Module 5 adds the runner API and binary. Logs are chunked, idempotent,
  masked, and capped at 10 MiB per job. A minute tick fails timed-out jobs and jobs nobody picks
  up within 24 hours.
- Live updates: `internal/events` (pg_notify on commit + one LISTEN per API process) feeds SSE
  streams for run changes and log output (resumable with Last-Event-ID).
- Migration `000004_pipelines`; 12 new endpoints; 9 new error codes (EN + KM); OpenAPI 0.5.0.
- Seed: four demo runs (succeeded with an approved production deploy, failed, waiting for
  approval, queued) with colored logs; the demo Developer always gets access to the demo project.

**Frontend**
- Pipelines tab (now the project's first tab): run list with filters, "Run pipeline" (branch or
  commit, variables, Idempotency-Key) and "Check pipeline file" (problems with line numbers).
- Run page: stage columns with job cards, cancel / re-run / re-run failed, invalid-file problems,
  live status over SSE. Job panel: steps, attempts, approval card (why you can't approve),
  retry, and a terminal-style log viewer (ANSI colors, follow mode, download).
- `fetch`-based SSE client (the access token stays in memory; EventSource can't send headers).
- New `pipeline` translation namespace (EN + KM).

### Quality

- Go: parser (87.8 % coverage), cron, engine rules, lifecycle with a simulated runner, approvals
  and protection, cancel/retry/re-run, webhook- and schedule-triggered runs, reaper, events,
  tenant isolation over all 12 pipeline routes with a coverage guard, SSE through the full HTTP
  stack. Service coverage 79.2 %.
- Web: 62 Vitest tests (+ SSE parser, ANSI parser); 35 Playwright tests (+6 pipeline: failed job
  log, Viewer read-only, Admin approval, file checker, cancel, Khmer layout).
- golangci-lint, gosec, govulncheck, ESLint, i18n check clean; Trivy 0 HIGH/CRITICAL; migrations
  up/down/up; sqlc and orval deterministic; OpenAPI ↔ routes drift test.

### Next — Module 5: Runner agent

`cmd/runner` (register with a one-time token, long-poll for jobs, Docker executor with CPU/memory
limits and timeouts), runner API on top of the pipeline lifecycle, artifacts and cache.

## Module 3 — Projects, repositories, environments

### Built

**Backend**
- Projects: create (Developers and up; a Developer creator becomes project Admin), list what I
  can see (search), rename/describe/default branch with If-Match, delete with slug confirmation.
- Project access: effective role = highest of the inherited org role, a direct grant and team
  grants (`authz.EffectiveProjectRole`, pinned by tests). Grants for users or teams (Admin,
  Developer, Viewer); cleaned up when someone leaves the org or a team is deleted. Hidden
  projects answer 404.
- Repositories: connect GitHub or GitLab — cloud, GitHub Enterprise or self-managed GitLab — with
  an access token (encrypted at rest, never returned). OpsHub creates the webhook with a random
  secret; if it can't, the repository connects in manual mode and the secret is shown once.
  Test connection, replace, disconnect (removes OpsHub's webhook).
- Webhook receivers for GitHub (HMAC-SHA256) and GitLab (token, constant-time compare):
  redeliveries are recognized by delivery ID; bad signatures are recorded without payload and
  can't block the real delivery. Deliveries are listed for 30 days. (Pipelines start from them in
  Module 4.)
- Environments: development/staging/production, non-secret variables, protection rules
  (required approvals, allowed branches, allowed roles).
- `internal/safehttp`: SSRF-safe HTTP client (checks the resolved IP at dial time, no redirects,
  no proxies); `OPSHUB_OUTBOUND_ALLOWED_CIDRS` allows specific internal hosts.
- `internal/idempotency`: `Idempotency-Key` middleware (24 h replay per user, conflict on reuse
  with a different request or while in flight; 5xx not stored). Used on project and
  environment creation.
- Hourly housekeeping job purges expired idempotency keys and old webhook deliveries.
- Migration `000003_projects`; 12 new error codes (EN + KM); OpenAPI 0.4.0.
- A test now fails when a route is missing from `api/openapi.yaml` or vice versa (it found a
  YAML quoting issue from Module 1, fixed).
- Fixed: a team (or project) named only in Khmer failed validation; it now gets a generated URL
  name.
- Seed: project "Payments API · API ទូទាត់ប្រាក់" with development, staging and protected
  production; the Platform team gets Developer access. `make seed` adds it to existing databases.

**Frontend**
- Projects page (search, create with an Idempotency-Key per dialog) and a project area with
  Settings, Environments (variables editor, protection editor), Repository (connect form, manual
  webhook setup, deliveries with signature status) and Access (grant people or teams, change
  roles, inherited org roles read-only) tabs. Controls follow the project's `actions`.
- New `project` translation namespace (EN + KM).

### Quality

- Go: project service tests (visibility, roles, grants, environments, repository modes, SSRF,
  webhooks, audit without secrets); tenant isolation over all 18 project routes with a coverage
  guard; idempotency and webhooks over HTTP; fake GitHub/GitLab APIs. Coverage gate now includes
  the new packages: 77.7 %.
- Web: 55 Vitest tests; 29 Playwright tests (+4 full-stack: create project and protected
  environment, SSRF refusal, Viewer read-only, Khmer layout of project pages on mobile and desktop).
- golangci-lint, gosec, govulncheck, ESLint, i18n check clean; Trivy 0 HIGH/CRITICAL; migrations
  up/down/up tested; sqlc and orval output deterministic.

### Next — Module 4: CI/CD pipelines

`.opshub.yml` parser and DAG engine, pipeline runs started from webhooks and by hand, live logs
over SSE, manual approval gates that enforce environment protection rules.

## Module 2 — RBAC: members, invitations, teams

### Built

**Backend**
- `internal/authz`: one enforcement point for the organization permission matrix
  (`docs/rbac.md`), used by every service before reading or changing tenant data. Non-members get
  `404 ORG_NOT_FOUND`; members without the permission get `403 FORBIDDEN`. The matrix is pinned by
  a test.
- Organizations: rename with optimistic locking (ETag / If-Match), soft delete confirmed by slug
  (Owner only), transfer ownership, `GET /orgs/{id}/permissions` for UI guards.
- Members: list with role filter and name/email search, change role, remove, leave. Admins can
  grant at most Developer and can't touch Owners or other Admins; the last Owner can't be
  removed, demoted or leave (`LAST_OWNER`, checked under a row lock).
- Invitations: invite by email + role, 7-day single-use link (token in the URL fragment, only its
  hash stored), re-invite replaces the open one, revoke, preview, accept (the verified email
  must match). Invitation emails are in EN/KM. With `OPSHUB_ALLOW_SIGNUP=false`, an invitee can
  still register by passing their invitation token.
- Teams: create, rename/describe (If-Match), delete, add/remove members (org members only).
- Every change is audited (`member.*`, `team.*`, `org.*`, before/after for role changes and
  renames).
- Migration `000002_rbac`: `invitations`, team `description`/`version`, member role index.
- Fixed: `make seed` failed on a second run instead of skipping the existing demo organization.
- Seed adds the team "Platform Team · ក្រុមវេទិកា".

**Frontend**
- Pages: Members (role editing limited to what you may grant, remove/leave, search, role filter,
  pending invitations with revoke, invite dialog), Teams and Team detail, Organization settings
  (rename, transfer ownership, leave, delete with slug confirmation), and Accept invitation
  (keeps the invitation through sign-in or sign-up, and handles a wrong signed-in account).
- `usePermissions`, `<RequirePermission>` and sidebar entries for the organization pages; controls
  are hidden by permission (the API enforces them regardless).
- New `org` translation namespace (EN + KM) and 8 new error codes.

### Quality

- Go: authz matrix test; service tests for every rule above; `TestTenantIsolation` calls all 18
  tenant-scoped routes with another tenant's IDs (404 each), and a companion test fails when a
  new route isn't covered; role enforcement over HTTP; invitation sign-up with sign-up disabled.
  Service coverage 75.3 %.
- Web: 51 Vitest tests (+ permission helpers, pending invitation storage); 25 Playwright tests
  (+4 full-stack: invite → register → accept via Mailpit, last-owner guard and teams, viewer sees
  no management controls, Khmer layout of the org pages on mobile and desktop).
- golangci-lint, gosec, govulncheck, ESLint, i18n check clean; Trivy 0 HIGH/CRITICAL; migrations
  up/down/up tested; sqlc and orval output deterministic.

### Next — Module 3: Projects

Projects, repositories and environments; project roles resolved from org role, teams and direct
grants.

## Module 1 — Auth & users

### Built

**Backend**
- Registration with email verification; resend. Registering an existing email returns the same
  response and emails the owner instead (no account enumeration).
- Password sign-in with argon2id, NIST-style password policy (12–128 chars, common-password list),
  account lockout (5 failures → 15 min, doubling up to 24 h) and per-IP rate limits.
- Sessions: 15-minute Ed25519 access JWTs + rotating refresh tokens in an httpOnly, SameSite=Strict
  cookie with reuse detection; CSRF double-submit + Origin check on cookie endpoints.
- TOTP two-factor authentication: encrypted seed (AES-256-GCM key ring), replay protection,
  10 single-use recovery codes, regeneration, disable.
- Password reset by email (1-hour single-use link; signs out every session); change password
  (signs out other devices); security-notice emails.
- SSO: GitHub (OAuth 2.0) and OpenID Connect (Google, Keycloak) with PKCE, state and nonce;
  account linking by verified email; 2FA still required for SSO sign-ins.
- Profile & preferences (name, language, time zone, Khmer numerals) with optimistic locking
  (ETag / If-Match → 409 VERSION_CONFLICT).
- Signed-in devices (list, revoke) and personal API tokens (scopes, expiry, revoke).
- Organizations (minimal slice): create, list mine, get as member (non-members get 404).
- Emails rendered in the recipient's language (EN/KM) and sent by River background jobs, enqueued
  in the same transaction as the change.
- Cross-cutting: cursor pagination, CORS allow-list, per-user/IP rate limiting, audit log for every
  security event, least-privilege PostgreSQL roles, `opshub-api migrate|seed|keys|healthcheck`.

**Frontend**
- App layout: collapsible sidebar (drawer on mobile), org switcher, EN | ខ្មែរ switcher (saved to
  the profile), light/dark/system theme, user menu.
- Pages: sign-in, 2FA, register, verify email, forgot/reset password, SSO completion, onboarding
  (create organization), organization overview, settings (profile, security, API tokens,
  organizations). Loading, empty and error states; toasts; confirmation dialogs.
- Access token in memory only; single-flight refresh serialized across tabs (Web Locks);
  cross-tab sign-out.
- Khmer fallback formatting for browsers without Khmer locale data.

**Tooling**
- `make dev` starts api, web, postgres, mailpit, prometheus and grafana (with an API dashboard).
- CI: service coverage gate (≥ 70 %), full-stack Playwright job on docker compose.

### Quality

- Go: unit + integration tests against PostgreSQL 17 (testcontainers). Service coverage 76.8 %.
- Web: 41 Vitest tests; 21 Playwright tests (14 mocked incl. Khmer typography at 3 widths,
  7 full-stack: registration, profile language, 2FA, password reset, API tokens, Khmer layout of
  signed-in pages, tenant isolation).
- golangci-lint, gosec, govulncheck, ESLint (no hard-coded UI strings) clean; Trivy 0 HIGH/CRITICAL.

### Next — Module 2: RBAC

Permission service with the matrix from `docs/rbac.md`, members and invitations, teams, role
changes with last-owner protection, UI route guards per role, tenant-isolation test harness.
