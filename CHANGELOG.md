# Changelog

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
