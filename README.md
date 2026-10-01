# OpsHub

**All-in-One DevOps Platform**: projects, CI/CD pipelines, deployments, infrastructure,
monitoring, logs, secrets, audit and a DORA dashboard behind one login, in **English and ខ្មែរ**.

Go · PostgreSQL 17 · React 19 + TypeScript · self-hosted

> **Status:** all 12 planned modules are complete (see [Modules](#modules) below). What changed
> in each one is in [CHANGELOG.md](CHANGELOG.md); the decisions made along the way are in the
> [delivery plan](docs/plan.md).

## សេចក្តីសង្ខេប (ខ្មែរ)

OpsHub គឺជាវេទិកា DevOps គ្រប់មុខងារក្នុងមួយ ដែលអ្នកអាចដំឡើងនៅលើម៉ាស៊ីនមេផ្ទាល់ខ្លួន។
វាប្រមូលផ្តុំគម្រោង CI/CD Pipeline ការ Deploy ហេដ្ឋារចនាសម្ព័ន្ធ ការត្រួតពិនិត្យ កំណត់ហេតុ Secret
កំណត់ត្រាសវនកម្ម និងផ្ទាំងរង្វាស់ DORA នៅក្រោមការចូលគណនីតែមួយ។ ចំណុចប្រទាក់ទាំងមូលមានជាភាសាអង់គ្លេស និងខ្មែរ។

- **ចាប់ផ្តើម៖** `make dev` បន្ទាប់មក `make seed` រួចបើក http://localhost:3000
- **ដំឡើងលើម៉ាស៊ីនមេ (Docker)៖** `cd deploy/install && ./opshub install` — HTTPS ស្វ័យប្រវត្តិ
  ការបម្រុងទុកដែលបានអ៊ិនគ្រីបរៀងរាល់យប់ និងការធ្វើបច្ចុប្បន្នភាព (មើល `docs/install-docker.md`)។
- **ភាសា៖** ប្តូររវាង EN | ខ្មែរ នៅរបារខាងលើ។ ភាសាដែលអ្នកជ្រើសរើសត្រូវបានរក្សាទុកក្នុងប្រវត្តិរូបរបស់អ្នក។
- **សុវត្ថិភាព៖** ពាក្យសម្ងាត់ argon2id, ការផ្ទៀងផ្ទាត់ពីរជំហាន (TOTP), Token ចូលប្រើរយៈពេល ១៥ នាទី,
  ការចាក់សោគណនីក្រោយការព្យាយាមខុសច្រើនដង និងកំណត់ហេតុសវនកម្មដែលមិនអាចកែប្រែបាន។
- **ឯកសារ៖** សូមមើលថត `docs/` (ជាភាសាអង់គ្លេស) និងសទ្ទានុក្រមពាក្យបច្ចេកទេសនៅ `docs/i18n.md`។

## Install on a server

On one Linux server with Docker, the installer sets up HTTPS (Let's Encrypt), nightly encrypted
backups and monitoring, and later upgrades OpsHub:

```bash
git clone https://github.com/khempheara/opshub.git && cd opshub/deploy/install
./opshub install
```

Or, without the source, download `opshub-install-<version>.tar.gz` from a release (its images
come from ghcr.io). Everything else (first sign-in, runner, backups and restore, upgrades) is in
the [Docker install guide](docs/install-docker.md); for Kubernetes, see [helm.md](docs/helm.md).

## Quick start

Requirements: Docker, Go 1.26+, Node 22+.

```bash
make dev    # creates .env with fresh keys, builds and starts the whole stack
make seed   # demo organizations "Angkor Tech · អង្គរ តិច" and "Mekong Cloud · មេគង្គ ក្លោដ"
```

| URL | What |
|---|---|
| http://localhost:3000 | OpsHub UI |
| http://localhost:8080/docs | API reference (Swagger UI; spec at `/api/openapi.yaml`) |
| http://localhost:8025 | Mailpit: every email the platform sends (verification, reset, …) |
| http://localhost:3001 | Grafana: "OpsHub API" (requests, p95 latency, errors) and "OpsHub Platform" (background jobs, runners, alerts, log ingest, database pool) dashboards |
| http://localhost:9090 | Prometheus |

Demo accounts (all share the password `make seed` prints; set `OPSHUB_SEED_PASSWORD` to choose it):
`owner@demo.opshub.local` (Owner, Khmer UI), `admin@…` (Admin), `dev@…` (Developer, Khmer UI),
`viewer@…` (Viewer), and `secure@…` (Admin of Mekong Cloud, with two-factor sign-in: `make seed`
prints its authenticator key and recovery codes once).

Signing in opens **Angkor Tech**, the small demo the E2E tests use. Switch to **Mekong Cloud** in
the organization menu to see every page filled: runs in every status and trigger, SSH, Docker and
Kubernetes targets, firing, acknowledged and resolved alerts with timelines, silences, all channel
kinds, expiring and failed certificates, pending invitations, API tokens and 45 days of audit log.
Some states move on by themselves, as they would for real: the seeded agents and runners go
offline within minutes (the running run's job then fails as "runner lost"), and the Staging API
alert starts firing a day after seeding. To start over, reset the database
(`docker compose down -v`, then `make dev` and `make seed`).

Ports already in use? Set `OPSHUB_WEB_PORT`, `OPSHUB_API_PORT`, `OPSHUB_PG_PORT`, … in `.env`
(and `OPSHUB_COMPOSE_PUBLIC_URL` to match the web port so email links work).

> The database roles are created when the Postgres volume is first initialized. If you have a
> volume from an older version, run `make clean` (this deletes local data) and `make dev` again.

### Working on the code

```bash
make dev-deps   # only postgres + mailpit
make dev-api    # API on :8080 with your local toolchain
make dev-web    # Vite + HMR on http://localhost:5173 (proxies /api to :8080)
```

For `dev-web`, keep `OPSHUB_PUBLIC_URL=http://localhost:5173` in `.env` so email links and SSO
callbacks point at the Vite server.

| Task | Command |
|---|---|
| All tests (Go unit + testcontainers integration, Vitest, i18n) | `make test` |
| Service coverage gate (≥ 70 %) | `make coverage` |
| E2E: mocked UI + Khmer typography, and full stack (needs `make dev`, `make seed`) | `make e2e` |
| Lint (golangci-lint incl. gosec, ESLint, tsc) | `make lint` |
| Format | `make fmt` |
| Security (govulncheck, gosec, npm audit) | `make security` |
| Khmer translations complete | `make i18n-check` |
| Regenerate sqlc + TypeScript client | `make gen` |
| Migrations | `make migrate-up`, `make migrate-down` (dev only) |
| Build binary / images | `make build`, `make docker` |
| Backup restore drill (needs `make dev`) | `make backup-drill` |
| Helm chart lint + Kubernetes schema check, shellcheck | `make helm-lint` |
| k6 load test (needs `make dev`, `make seed`) | `make load` |

**Adding an endpoint:** describe it in `api/openapi.yaml` → SQL in `db/queries` → `make gen` →
service (authorization + audit) → handler → tests → new error codes in `internal/apperr` **and**
`web/src/locales/{en,km}/errors.json` → UI strings in both languages.

## Configuration

Everything is configured with environment variables; [.env.example](.env.example) documents each
one. The essentials:

| Variable | Purpose |
|---|---|
| `OPSHUB_PUBLIC_URL` | URL users open (email links, SSO callbacks, CSRF origin check). Must be https in production. |
| `OPSHUB_DATABASE_URL` / `OPSHUB_MIGRATE_DATABASE_URL` | App (DML-only) and schema-owner connections |
| `OPSHUB_MASTER_KEYS` | AES-256 key ring for data at rest (secrets, 2FA seeds, Git tokens, deploy credentials). Back it up separately from the database. |
| `OPSHUB_JWT_KEYS` | Ed25519 key ring for access tokens |
| `OPSHUB_ALLOW_SIGNUP`, `OPSHUB_BOOTSTRAP_ADMIN_EMAIL` | Who can register; the bootstrap address becomes platform admin |
| `OPSHUB_SMTP_*` | Outgoing email |
| `OPSHUB_SSO_*` | GitHub, Google and Keycloak sign-in (enabled when the client ID is set) |
| `OPSHUB_TRUSTED_PROXIES` | CIDRs of your reverse proxy/ingress, so audit logs record real client IPs |
| `OPSHUB_OUTBOUND_ALLOWED_CIDRS` | Internal networks OpsHub may call (e.g. a self-hosted GitLab on `10.0.0.0/8`); everything private is blocked otherwise |
| `OPSHUB_CORS_ALLOWED_ORIGINS` | Extra browser origins (empty = same-origin, recommended) |

Generate keys with `go run ./cmd/api keys generate`. To rotate, put the new key first and keep the
old one after it (`id2:…,id1:…`), run `opshub-api keys rotate`, then remove the old key
([details](docs/secrets.md#rotating-the-master-key)).

## Architecture in brief

- **One API binary** (`cmd/api`): REST under `/api/v1`, background jobs (River, in PostgreSQL),
  `/healthz`, `/readyz`, `/metrics`, `/docs`, and subcommands `migrate`, `seed`, `keys`.
- **Layers:** handler → service (business rules, authorization, audit) → repository (sqlc).
- **Errors are codes, not sentences:** `{"error":{"code","message","details"}}`; the UI translates
  the code into English or Khmer.
- **PostgreSQL is the only stateful service** (data, job queue, SSE fan-out); pipeline artifacts
  and caches live on a volume (`OPSHUB_BLOB_DIR`). The API connects as a DML-only role; the
  audit log is insert-only.

Details: [architecture](docs/architecture.md) · [API endpoints](docs/api.md) ·
[database](docs/database.md) · [RBAC](docs/rbac.md) · [i18n & glossary](docs/i18n.md) ·
[observability](docs/observability.md)

## Security model

- Passwords: argon2id; 12–128 characters; common passwords rejected; lockout after 5 failures
  (15 min, doubling); per-IP rate limits on sign-in and email endpoints.
- Sessions: 15-minute access JWT kept in memory; rotating refresh token in an httpOnly,
  `SameSite=Strict` cookie with reuse detection (a replayed token revokes the session); CSRF
  double-submit token on the cookie endpoints.
- 2FA: TOTP (seed encrypted with AES-256-GCM, each code accepted once) + single-use recovery codes.
- Email links carry one-time tokens in the URL fragment, so they never reach server logs.
- Personal API tokens (`ohp_…`, scoped `api:read` / `api:write`) can't manage passwords, 2FA,
  sessions or other tokens.
- Every security event is in the append-only audit log (who, what, when, IP, before/after).
- Permissions are checked in the service layer against one role matrix ([RBAC](docs/rbac.md)).
  Resources of other organizations, and projects you can't see, answer 404 — a test calls every
  tenant-scoped route with another tenant's IDs.
- Git access tokens and webhook secrets are encrypted at rest (AES-256-GCM) and never returned.
  Webhooks are verified with HMAC-SHA256 (GitHub) or a constant-time token compare (GitLab) and
  de-duplicated by delivery ID.
- Calls to user-supplied hosts go through an SSRF-safe client: the resolved IP is checked when
  connecting (private, loopback, link-local and metadata addresses are refused), and redirects
  and proxies are not followed.
- `Idempotency-Key` on create endpoints makes retries safe (24 h replay, per user).

## Production notes

- Serve over https and set `OPSHUB_PUBLIC_URL` accordingly (cookies become `Secure`).
- Run `opshub-api migrate up` as a deploy step with the migrator credentials and set
  `OPSHUB_MIGRATE_ON_START=false` for the API.
- Create the roles as in `deploy/compose/postgres/init-roles.sql` (with real passwords); the
  [Docker install](docs/install-docker.md) does this for you, with HTTPS, SMTP settings and
  backups.
- Set `OPSHUB_TRUSTED_PROXIES` to your ingress range, and configure real SMTP with TLS.
- Keep `/metrics` on an internal network (the bundled nginx doesn't expose it); metrics,
  dashboards and suggested alerts are in [observability.md](docs/observability.md).
- Back up the database nightly and practise restores ([backup.md](docs/backup.md)); keep
  `OPSHUB_MASTER_KEYS` backed up offline too.
- On Kubernetes, use the Helm chart in `deploy/helm/opshub` ([helm.md](docs/helm.md)).
- Load-test changes with `make load` ([load-testing.md](docs/load-testing.md)).
- Put `OPSHUB_BLOB_DIR` (artifacts and caches) on persistent storage writable by the API user
  and include it in backups if artifacts matter to you.
- Run runners on machines dedicated to CI: the agent controls that machine's Docker.
- Git webhooks are delivered to `OPSHUB_PUBLIC_URL/api/v1/webhooks/…`, so the Git host must be
  able to reach it. If OpsHub can't install a webhook itself, the Repository tab shows the URL and
  secret to add by hand.

## Runners and pipelines

Add a `.opshub.yml` to the connected repository ([pipeline reference](docs/pipelines.md)). Pushes,
tags, pull requests and schedules start runs; the Pipelines tab shows the job graph, live logs
and approval gates. Jobs execute on runners ([runner guide](docs/runners.md)): register one in
**Organization → Runners**, then start the `opshub-runner` agent on a machine with Docker. For
local development, paste the registration token into `.env` as
`OPSHUB_RUNNER_REGISTRATION_TOKEN` and run `make runner` (it uses this machine's Docker socket).

## Deployments

Add a deploy target in **Organization → Deploy targets** (an SSH host, a Docker host or a
Kubernetes cluster), run **Test connection** and trust its host key, then deploy an image from a
project's **Deployments** tab or from a pipeline job's `deploy:` block. Unhealthy releases are
reverted automatically; the current release rolls back in one click
([deployment guide](docs/deployments.md)). The demo data includes `demo-k8s`, a cluster that
doesn't exist: deployments to it fail safely as "target unreachable".

## Infrastructure

**Organization → Infrastructure** keeps an inventory of servers, clusters, databases and
domains. On a server, create an agent token and run `opshub-runner agent` (Linux) to chart CPU,
memory and disk; OpsHub checks every domain's TLS certificate and lists those about to expire
([infrastructure guide](docs/infrastructure.md)). The demo server `web-1` comes with a day of
sample metrics.

## Secrets

Add secrets in a project's **Secrets** tab (for all environments or one environment) and list
them under a job's `secrets:` in `.opshub.yml`. Values are encrypted, write-only and masked in
logs; pull-request runs never receive them ([secrets guide](docs/secrets.md)).

## Monitoring & alerts

**Organization → Monitoring** checks HTTP endpoints, TCP ports and TLS certificates, and alerts
on them and on your infrastructure (server usage, offline agents, expiring certificates). Alerts escalate through Telegram, Slack, email or webhooks until someone
acknowledges them; silences mute them during maintenance ([monitoring guide](docs/monitoring.md)).
The demo data includes an unreachable database monitor, so an alert fires a minute after
`make seed` (the email arrives in Mailpit).

## Logs

**Organization → Logs** searches what your services send and what pipeline jobs and
deployments print, in English or Khmer, with filters, paging and a follow mode. Admins create
an ingest token per service, which sends newline-delimited JSON to `POST /api/v1/ingest/logs`.
Lines are kept for `OPSHUB_LOG_RETENTION_DAYS` (default 30) ([logs guide](docs/logs.md)).

## Audit log

Every change and security event is recorded with who, when and from where. Owners and Admins
read it in **Organization → Audit log** as sentences in English or Khmer, filter it and export it
as CSV that opens correctly in Excel. Everyone sees their own sign-ins and account changes in
**Settings → Security** ([audit guide](docs/audit.md)).

## Dashboard

The organization's **Overview** shows how you deliver: the four DORA metrics (deployment
frequency, lead time, change failure rate, time to restore) with their performance level, alert
recovery, pipeline success rate and run durations, and a per-project table, for any project and
range up to a year ([dashboard guide](docs/dashboard.md)). The demo data includes 60 days of
history in the `checkout-web` project.

## Modules

All twelve modules of the plan are complete.

| # | Module | Guide |
|---|---|---|
| 1 | ✅ Auth & users | [Security model](#security-model) |
| 2 | ✅ RBAC | [rbac.md](docs/rbac.md) |
| 3 | ✅ Projects & repositories | [api.md §3](docs/api.md#3-projects-repositories-environments) |
| 4 | ✅ CI/CD pipelines | [pipelines.md](docs/pipelines.md) |
| 5 | ✅ Runner agent | [runners.md](docs/runners.md) |
| 6 | ✅ Deployments | [deployments.md](docs/deployments.md) |
| 7 | ✅ Infrastructure | [infrastructure.md](docs/infrastructure.md) |
| 8 | ✅ Secrets | [secrets.md](docs/secrets.md) |
| 9 | ✅ Monitoring & alerts | [monitoring.md](docs/monitoring.md) |
| 10 | ✅ Logs | [logs.md](docs/logs.md) |
| 11 | ✅ Audit log | [audit.md](docs/audit.md) |
| 12 | ✅ Dashboard, DORA metrics & operations | [dashboard.md](docs/dashboard.md) · [observability.md](docs/observability.md) · [backup.md](docs/backup.md) · [helm.md](docs/helm.md) · [load-testing.md](docs/load-testing.md) |
