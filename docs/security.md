# Security model

How OpsHub protects accounts, data and the servers it talks to. The mapping to the OWASP Top 10
is in [architecture.md](architecture.md#7-security-owasp-top-10-mapping); roles and permissions
are in [rbac.md](rbac.md).

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
- Permissions are checked in the service layer against one role matrix ([RBAC](rbac.md)).
  Resources of other organizations, and projects you can't see, answer 404 — a test calls every
  tenant-scoped route with another tenant's IDs.
- Git access tokens and webhook secrets are encrypted at rest (AES-256-GCM) and never returned.
  Webhooks are verified with HMAC-SHA256 (GitHub) or a constant-time token compare (GitLab) and
  de-duplicated by delivery ID.
- Calls to user-supplied hosts go through an SSRF-safe client: the resolved IP is checked when
  connecting (private, loopback, link-local and metadata addresses are refused), and redirects
  and proxies are not followed.
- `Idempotency-Key` on create endpoints makes retries safe (24 h replay, per user).

## Running OpsHub

- Containers run as unprivileged users; the database is reachable only inside the stack (or the
  container), and the API connects as a role that can only read and write rows.
- Backups are encrypted with [age](https://age-encryption.org) public keys before they're
  written; the private key belongs off the server ([backup.md](backup.md)).
- `OPSHUB_MASTER_KEYS` decrypts the secrets in the database: keep a copy offline, separately
  from the backups ([configuration](configuration.md)).
