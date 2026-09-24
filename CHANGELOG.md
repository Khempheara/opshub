# Changelog

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
