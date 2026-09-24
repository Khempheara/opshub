# OpsHub

**All-in-One DevOps Platform**: projects, CI/CD pipelines, deployments, infrastructure,
monitoring, logs, secrets and audit behind one login, in **English and ខ្មែរ**.

Go · PostgreSQL 17 · React 19 + TypeScript · self-hosted

> **Status:** Module 1 (Auth & users) is complete. See [CHANGELOG.md](CHANGELOG.md) and the
> [delivery plan](docs/plan.md). Modules ship one at a time.

## សេចក្តីសង្ខេប (ខ្មែរ)

OpsHub គឺជាវេទិកា DevOps គ្រប់មុខងារក្នុងមួយ ដែលអ្នកអាចដំឡើងនៅលើម៉ាស៊ីនមេផ្ទាល់ខ្លួន។
វាប្រមូលផ្តុំគម្រោង CI/CD Pipeline ការ Deploy ហេដ្ឋារចនាសម្ព័ន្ធ ការត្រួតពិនិត្យ កំណត់ហេតុ Secret
និងកំណត់ហេតុសវនកម្ម នៅក្រោមការចូលគណនីតែមួយ។ ចំណុចប្រទាក់ទាំងមូលមានជាភាសាអង់គ្លេស និងខ្មែរ។

- **ចាប់ផ្តើម៖** `make dev` បន្ទាប់មក `make seed` រួចបើក http://localhost:3000
- **ភាសា៖** ប្តូររវាង EN | ខ្មែរ នៅរបារខាងលើ។ ភាសាដែលអ្នកជ្រើសរើសត្រូវបានរក្សាទុកក្នុងប្រវត្តិរូបរបស់អ្នក។
- **សុវត្ថិភាព៖** ពាក្យសម្ងាត់ argon2id, ការផ្ទៀងផ្ទាត់ពីរជំហាន (TOTP), Token ចូលប្រើរយៈពេល ១៥ នាទី,
  ការចាក់សោគណនីក្រោយការព្យាយាមខុសច្រើនដង និងកំណត់ហេតុសវនកម្មដែលមិនអាចកែប្រែបាន។
- **ឯកសារ៖** សូមមើលថត `docs/` (ជាភាសាអង់គ្លេស) និងសទ្ទានុក្រមពាក្យបច្ចេកទេសនៅ `docs/i18n.md`។

## Quick start

Requirements: Docker, Go 1.26+, Node 22+.

```bash
make dev    # creates .env with fresh keys, builds and starts the whole stack
make seed   # demo organization "Angkor Tech · អង្គរ តិច" with one user per role
```

| URL | What |
|---|---|
| http://localhost:3000 | OpsHub UI |
| http://localhost:8080/docs | API reference (Swagger UI; spec at `/api/openapi.yaml`) |
| http://localhost:8025 | Mailpit: every email the platform sends (verification, reset, …) |
| http://localhost:3001 | Grafana: "OpsHub API" dashboard (requests, p95 latency, errors) |
| http://localhost:9090 | Prometheus |

Demo accounts (all share the password `make seed` prints; set `OPSHUB_SEED_PASSWORD` to choose it):
`owner@demo.opshub.local` (Owner, Khmer UI), `admin@…` (Admin), `dev@…` (Developer, Khmer UI),
`viewer@…` (Viewer).

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
| `OPSHUB_MASTER_KEYS` | AES-256 key ring for data at rest (TOTP seeds; later secrets and credentials). Back it up separately from the database. |
| `OPSHUB_JWT_KEYS` | Ed25519 key ring for access tokens |
| `OPSHUB_ALLOW_SIGNUP`, `OPSHUB_BOOTSTRAP_ADMIN_EMAIL` | Who can register; the bootstrap address becomes platform admin |
| `OPSHUB_SMTP_*` | Outgoing email |
| `OPSHUB_SSO_*` | GitHub, Google and Keycloak sign-in (enabled when the client ID is set) |
| `OPSHUB_TRUSTED_PROXIES` | CIDRs of your reverse proxy/ingress, so audit logs record real client IPs |
| `OPSHUB_CORS_ALLOWED_ORIGINS` | Extra browser origins (empty = same-origin, recommended) |

Generate keys with `go run ./cmd/api keys generate`. To rotate, put the new key first and keep the
old one after it (`id2:…,id1:…`).

## Architecture in brief

- **One API binary** (`cmd/api`): REST under `/api/v1`, background jobs (River, in PostgreSQL),
  `/healthz`, `/readyz`, `/metrics`, `/docs`, and subcommands `migrate`, `seed`, `keys`.
- **Layers:** handler → service (business rules, authorization, audit) → repository (sqlc).
- **Errors are codes, not sentences:** `{"error":{"code","message","details"}}`; the UI translates
  the code into English or Khmer.
- **PostgreSQL is the only stateful dependency** (data, job queue, SSE fan-out). The API connects
  as a DML-only role; the audit log is insert-only.

Details: [architecture](docs/architecture.md) · [API endpoints](docs/api.md) ·
[database](docs/database.md) · [RBAC](docs/rbac.md) · [i18n & glossary](docs/i18n.md)

## Security model (Module 1)

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

## Production notes

- Serve over https and set `OPSHUB_PUBLIC_URL` accordingly (cookies become `Secure`).
- Run `opshub-api migrate up` as a deploy step with the migrator credentials and set
  `OPSHUB_MIGRATE_ON_START=false` for the API.
- Create the roles as in `deploy/compose/postgres/init-roles.sql` (with real passwords).
- Set `OPSHUB_TRUSTED_PROXIES` to your ingress range, and configure real SMTP with TLS.
- Keep `/metrics` on an internal network (the bundled nginx doesn't expose it).

## Runners and pipelines

The runner agent and the first pipeline arrive with Modules 4–5; this section will then explain how
to register a runner and run a pipeline.

## Roadmap

1. ✅ Auth & users · 2. RBAC · 3. Projects & repositories · 4. CI/CD pipelines · 5. Runner agent ·
6. Deployments · 7. Infrastructure · 8. Secrets · 9. Monitoring & alerts · 10. Logs ·
11. Audit log · 12. Dashboard & DORA metrics
