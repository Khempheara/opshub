# OpsHub

**All-in-One DevOps Platform** — projects, CI/CD pipelines, deployments, infrastructure, monitoring,
logs, secrets and audit behind one login, in **English and ខ្មែរ**.

Go · PostgreSQL 17 · React 19 + TypeScript · self-hosted

> **Status: Phase 0 plan — awaiting approval.** See [docs/plan.md](docs/plan.md). A foundation
> skeleton (API core, bilingual UI shell, CI) was also built ahead of approval; it is up for review
> along with the plan.

## Quick start

Requirements: Go 1.26+, Node 22+, Docker.

```bash
cp .env.example .env            # if port 5432 is taken locally, set OPSHUB_PG_PORT=55432
make dev-api                    # starts PostgreSQL, migrates, serves the API on :8080
make dev-web                    # in another terminal: UI on http://localhost:5173
```

Or everything in containers (UI on http://localhost:3000):

```bash
make up
```

| URL | What |
|---|---|
| `/` | Web UI |
| `/docs` | Swagger UI (spec at `/api/openapi.yaml`) |
| `/healthz`, `/readyz` | Liveness / readiness probes |
| `/metrics` | Prometheus metrics |

Run `make help` for all commands.

## Development workflow

| Task | Command |
|---|---|
| All tests (Go unit + testcontainers integration, Vitest, i18n check) | `make test` |
| E2E (Playwright) | `make e2e` |
| Lint (golangci-lint, ESLint, tsc) | `make lint` |
| Security (govulncheck, gosec) | `make security` |
| New migration | `make migrate-new name=add_projects` |
| Regenerate DB code after editing `db/queries` | `make sqlc` |
| Regenerate TS client after editing `api/openapi.yaml` | `make api-client` |

**Adding an endpoint:** describe it in `api/openapi.yaml` → `make api-client` → add SQL in
`db/queries` → `make sqlc` → implement handler → service (with `authz` + audit) → repository →
add error codes to `apperr.AllCodes` **and** `web/src/locales/{en,km}/errors.json`.

## Documentation

- [Delivery plan & open decisions](docs/plan.md)
- [API endpoints](docs/api.md)
- [Architecture](docs/architecture.md) — system overview, layering, conventions, security baseline
- [Database design](docs/database.md) — conventions and the full schema for every module
- [RBAC](docs/rbac.md) — roles and the permission matrix
- [i18n](docs/i18n.md) — English/Khmer rules, Khmer typography, glossary

## Roadmap

Modules are delivered one at a time in this order (details and Definition of Done in
[docs/plan.md](docs/plan.md)):

0. Plan (📝 awaiting approval) · 1. Auth & users · 2. RBAC · 3. Projects & repositories ·
4. CI/CD pipelines · 5. Runner agent · 6. Deployments · 7. Infrastructure · 8. Secrets ·
9. Monitoring & alerts · 10. Logs · 11. Audit log · 12. Dashboard & DORA metrics
