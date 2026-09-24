# API endpoint list (`/api/v1`)

Conventions (details in [architecture.md §6](architecture.md#6-cross-cutting-api-conventions)):
all lists are cursor-paginated (`limit`, `cursor`, `sort`, filters); **(IK)** = honours `Idempotency-Key`;
**(IM)** = requires `If-Match` (optimistic locking); **SSE** = `text/event-stream`.

Nested routes are used to **create/list** under a parent. Item routes are flat (`/projects/{id}`); the
service resolves the owning organization from the item and authorizes against it.

**Auth column:** `—` public · `JWT` user access token or personal API token (`Bearer ohp_…`) ·
`Cookie+CSRF` refresh cookie plus `X-CSRF-Token` · `Runner` per-runner token · `Job` short-lived
per-job token · `Agent` per-asset agent token · `HMAC` webhook signature.
**Action column:** the RBAC action checked (see [rbac.md](rbac.md)).

## System

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/healthz` | — | | Liveness |
| GET | `/readyz` | — | | Readiness (DB) |
| GET | `/metrics` | — (network-restricted) | | Prometheus |
| GET | `/api/openapi.yaml`, `/docs` | — | | Spec, Swagger UI |
| GET | `/api/v1/meta` | — | | Version, locales, defaults, enabled SSO providers |

## 1. Auth & users

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| POST | `/auth/register` | — | | Create account (sends verification email in the user's locale) |
| POST | `/auth/verify-email` | — | | Confirm with token |
| POST | `/auth/verify-email/resend` | — | | Resend (rate limited; same response whether or not the email exists) |
| POST | `/auth/login` | — | | Password login → access JWT + refresh cookie, or `mfa_required` |
| POST | `/auth/login/2fa` | — | | Complete login with TOTP or recovery code |
| POST | `/auth/refresh` | Cookie+CSRF | | Rotate refresh token → new access JWT |
| POST | `/auth/logout` | Cookie+CSRF | | Revoke refresh family, clear cookies |
| POST | `/auth/password/forgot` | — | | Send reset email (no account enumeration) |
| POST | `/auth/password/reset` | — | | Set new password with token; revokes all sessions |
| GET | `/auth/sso/{provider}/start` | — | | Redirect to the provider (github, google, keycloak; state + PKCE + nonce) |
| GET | `/auth/sso/{provider}/callback` | — | | Link/login; sets cookies, redirects to the SPA |
| GET | `/me` | JWT | | Profile, locale, timezone, khmer_numerals, orgs + roles |
| PATCH | `/me` | JWT | | Update display name, locale, timezone, khmer_numerals **(IM)** |
| POST | `/me/password` | JWT | | Change password (requires current) |
| POST | `/me/2fa/setup` | JWT | | Generate TOTP secret + otpauth URI (QR) |
| POST | `/me/2fa/enable` | JWT | | Confirm code → returns recovery codes once |
| POST | `/me/2fa/disable` | JWT | | Requires password + code |
| POST | `/me/2fa/recovery-codes` | JWT | | Regenerate recovery codes |
| GET | `/me/sessions` | JWT | | Active refresh-token families (device, IP, last used) |
| DELETE | `/me/sessions/{id}` | JWT | | Revoke a session |
| GET | `/me/tokens` | JWT | | Personal API tokens (prefix, scopes, last used) |
| POST | `/me/tokens` | JWT | | Create token; secret shown once |
| DELETE | `/me/tokens/{id}` | JWT | | Revoke |
| GET | `/me/2fa` | JWT | | 2FA status + remaining recovery codes |
| GET | `/me/identities` · DELETE `/me/identities/{id}` | JWT | | Linked SSO identities |

## 2. Organizations, members, teams, RBAC

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/orgs` | JWT | | Orgs I belong to (org switcher) |
| POST | `/orgs` | JWT | | Create org (creator becomes Owner) |
| GET | `/orgs/{orgId}` | JWT | `org.view` | |
| PATCH | `/orgs/{orgId}` | JWT | `org.update` | Rename **(IM)** (settings such as log retention arrive with later modules) |
| DELETE | `/orgs/{orgId}?confirm={slug}` | JWT | `org.delete` | Soft delete; `confirm` must equal the slug (`CONFIRMATION_MISMATCH`) |
| POST | `/orgs/{orgId}/transfer-ownership` | JWT | `org.transfer` | Target becomes Owner, caller becomes Admin |
| GET | `/orgs/{orgId}/permissions` | JWT | | My role and allowed actions (UI route guards) |
| GET | `/orgs/{orgId}/members` | JWT | `member.view` | `?role=`, `?q=` (name/email), cursor pagination |
| PATCH | `/orgs/{orgId}/members/{userId}` | JWT | `member.update_role` | Change role (Admin limits, last-owner guard) |
| DELETE | `/orgs/{orgId}/members/{userId}` | JWT | `member.remove` | Remove; any member may remove themselves (leave) |
| GET / POST | `/orgs/{orgId}/invitations` | JWT | `member.invite` | Open invitations / invite by email + role. Email in the invitee's profile language if they have an account, else the inviter's request language. Re-inviting replaces the open invitation |
| DELETE | `/invitations/{invitationId}` | JWT | `member.invite` | Revoke |
| POST | `/invitations/preview` | JWT | | Org name, role, inviter and invited email for a token |
| POST | `/invitations/accept` | JWT | | Accept with token; the signed-in user's verified email must match (`INVITATION_EMAIL_MISMATCH`) |
| GET / POST | `/orgs/{orgId}/teams` | JWT | `team.view` / `team.manage` | |
| GET / PATCH / DELETE | `/teams/{teamId}` | JWT | `team.view` / `team.manage` | GET includes members; **(IM)** on PATCH |
| PUT / DELETE | `/teams/{teamId}/members/{userId}` | JWT | `team.manage` | The user must be an organization member |

Invitation links carry the token in the URL fragment (`/invitations/accept#token=…`). When
`OPSHUB_ALLOW_SIGNUP=false`, `POST /auth/register` still accepts an invitee who passes that token as
`invitation_token` for the same email address.

## 3. Projects, repositories, environments

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET / POST | `/orgs/{orgId}/projects` | JWT | org member / `project.create` | Projects I can see (`?q=`); create **(IK)**. A Developer creator gets a direct Admin grant |
| GET / PATCH / DELETE | `/projects/{projectId}` | JWT | `project.view` / `project.update` / `project.delete` | GET includes my `role` and allowed `actions`; PATCH **(IM)**; DELETE `?confirm={slug}` removes the repository and its webhook |
| GET | `/projects/{projectId}/members` | JWT | `project.view` | Direct grants, team grants, and org Owners/Admins (`source: organization`) |
| PUT / DELETE | `/projects/{projectId}/members/{principal}` | JWT | `project.manage_members` | principal = `user:<id>` (org member) or `team:<id>`; role admin/developer/viewer |
| GET | `/projects/{projectId}/repository` | JWT | `project.view` | `404 REPOSITORY_NOT_FOUND` when none is connected |
| PUT | `/projects/{projectId}/repository` | JWT | `repo.connect` | {provider, base_url?, full_name, access_token} → validates with the Git host, creates a webhook with a random secret, or returns manual-setup URL + secret (shown once) |
| DELETE | `/projects/{projectId}/repository` | JWT | `repo.connect` | Deletes the token; removes OpsHub's webhook (best effort) |
| POST | `/projects/{projectId}/repository/test` | JWT | `repo.connect` | Re-checks the token; refreshes the default branch |
| GET | `/projects/{projectId}/repository/deliveries` | JWT | `repo.connect` | Recent webhook deliveries (30 days), newest first |
| POST | `/webhooks/github/{repositoryId}` | HMAC | | `X-Hub-Signature-256`; 202 accepted, 200 duplicate, 401 bad signature |
| POST | `/webhooks/gitlab/{repositoryId}` | Token | | `X-Gitlab-Token` (constant-time compare) |
| GET / POST | `/projects/{projectId}/environments` | JWT | `project.view` / `environment.manage` | Create **(IK)**; ≤ 20 per project |
| GET / PATCH / DELETE | `/environments/{environmentId}` | JWT | `project.view` / `environment.manage` | Kind, non-secret variables, protection rule (null = unprotected) **(IM)**; name is fixed |

## 4. Pipelines, runs, jobs

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| POST | `/projects/{projectId}/pipeline/validate` | JWT | `project.view` | Lint a `.opshub.yml` body (errors with line numbers) |
| GET | `/projects/{projectId}/runs` | JWT | `run.view` | Filter status, ref, trigger, actor, from/to |
| POST | `/projects/{projectId}/runs` | JWT | `pipeline.trigger` | Manual run {ref, variables} **(IK)** |
| GET | `/runs/{runId}` | JWT | `run.view` | Run + job graph |
| POST | `/runs/{runId}/cancel` | JWT | `run.cancel` | |
| POST | `/runs/{runId}/rerun` | JWT | `pipeline.trigger` | Whole run, or `{failed_only: true}` **(IK)** |
| GET | `/runs/{runId}/events` | JWT | `run.view` | **SSE** run/job status changes |
| GET | `/jobs/{jobId}` | JWT | `run.view` | Steps, timings, runner |
| POST | `/jobs/{jobId}/retry` | JWT | `pipeline.trigger` | New attempt **(IK)** |
| GET | `/jobs/{jobId}/logs` | JWT | `run.view` | Chunks `?after_seq=` (download as text with `Accept: text/plain`) |
| GET | `/jobs/{jobId}/logs/stream` | JWT | `run.view` | **SSE** live log (supports `Last-Event-ID` resume) |
| POST | `/jobs/{jobId}/approvals` | JWT | `approval.decide` | {decision, comment}; protection rules enforced; approver ≠ triggerer |
| GET | `/jobs/{jobId}/artifacts` | JWT | `run.view` | |
| GET | `/artifacts/{artifactId}/download` | JWT | `run.view` | Streams the blob |

## 5. Runners

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/orgs/{orgId}/runners` | JWT | `runner.view` | Status, labels, version, last seen, current jobs |
| POST | `/orgs/{orgId}/runner-registration-tokens` | JWT | `runner.manage` | One-time token (shown once, 1 h TTL) |
| PATCH / DELETE | `/runners/{runnerId}` | JWT | `runner.manage` | Labels, disable, delete **(IM)** |
| POST | `/runner/register` | registration token | | → runner ID + runner token |
| POST | `/runner/heartbeat` | Runner | | Liveness, capacity; response carries cancel requests |
| POST | `/runner/jobs/request` | Runner | | Long-poll (≤ 30 s) → job spec + Job token + secrets |
| PATCH | `/runner/jobs/{jobId}` | Job | | Job/step status, exit codes |
| POST | `/runner/jobs/{jobId}/logs` | Job | | Append masked log chunk {seq, content} (256 KiB max) |
| POST | `/runner/jobs/{jobId}/artifacts` | Job | | Upload (multipart/stream, size limit per org) |
| GET | `/runner/jobs/{jobId}/dependencies/{artifactId}` | Job | | Download artifacts from `needs` jobs |
| GET / PUT | `/runner/cache/{key}` | Job | | Pipeline cache blobs scoped to project |

## 6. Deployments

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET / POST | `/orgs/{orgId}/deploy-targets` | JWT | `target.view` / `target.manage` | SSH / Docker / Kubernetes (credentials write-only) |
| GET / PATCH / DELETE | `/deploy-targets/{targetId}` | JWT | `target.manage` | **(IM)** |
| POST | `/deploy-targets/{targetId}/test` | JWT | `target.manage` | Connectivity check |
| GET | `/projects/{projectId}/deployments` | JWT | `deployment.view` | Release history; filter env, status, from/to |
| POST | `/environments/{envId}/deployments` | JWT | `deployment.create` | {version/artifact, target, strategy} **(IK)** |
| GET | `/deployments/{deploymentId}` | JWT | `deployment.view` | Status, health check results, diff to previous |
| GET | `/deployments/{deploymentId}/logs/stream` | JWT | `deployment.view` | **SSE** |
| POST | `/deployments/{deploymentId}/rollback` | JWT | `deployment.rollback` | Redeploy previous successful release **(IK)** |

## 7. Infrastructure

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET / POST | `/orgs/{orgId}/assets` | JWT | `infra.view` / `infra.manage` | Filter kind, tag, status |
| GET / PATCH / DELETE | `/assets/{assetId}` | JWT | `infra.manage` | **(IM)** |
| GET | `/assets/{assetId}/metrics` | JWT | `infra.view` | `?from&to&step` CPU/RAM/disk series |
| POST | `/assets/{assetId}/agent-token` | JWT | `infra.manage` | Issue/rotate agent token (shown once) |
| GET | `/orgs/{orgId}/certificates` | JWT | `infra.view` | SSL expiry list `?expiring_within=30d` |
| POST | `/agent/heartbeat` | Agent | | Metrics sample |

## 8. Secrets

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/projects/{projectId}/secrets` | JWT | `secret.list` | Names + metadata only; filter environment |
| POST | `/projects/{projectId}/secrets` | JWT | `secret.create` | {name, environment_id?, value}; value never returned |
| GET | `/secrets/{secretId}` | JWT | `secret.list` | Metadata, current version |
| PATCH | `/secrets/{secretId}` | JWT | `secret.update` | Description only **(IM)** |
| POST | `/secrets/{secretId}/versions` | JWT | `secret.rotate` | New value → new version |
| GET | `/secrets/{secretId}/versions` | JWT | `secret.list` | Version metadata (who, when) |
| DELETE | `/secrets/{secretId}` | JWT | `secret.delete` | Soft delete |

Master-key (KEK) rotation is an operator CLI: `opshub-api keys rotate` (re-wraps all DEKs).

## 9. Monitoring & alerts

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET / POST | `/orgs/{orgId}/monitors` | JWT | `monitor.view` / `monitor.manage` | HTTP / TCP / SSL checks |
| GET / PATCH / DELETE | `/monitors/{monitorId}` | JWT | `monitor.manage` | Pause via `enabled=false` **(IM)** |
| GET | `/monitors/{monitorId}/results` | JWT | `monitor.view` | Uptime %, latency series |
| GET / POST | `/orgs/{orgId}/alert-rules` | JWT | `monitor.view` / `monitor.manage` | Threshold, duration, severity, escalation |
| GET / PATCH / DELETE | `/alert-rules/{ruleId}` | JWT | `monitor.manage` | **(IM)** |
| GET | `/orgs/{orgId}/alerts` | JWT | `monitor.view` | Filter status, severity |
| GET | `/alerts/{alertId}` | JWT | `monitor.view` | Timeline, notifications sent |
| POST | `/alerts/{alertId}/acknowledge` | JWT | `alert.ack` | Stops escalation |
| GET / POST | `/orgs/{orgId}/silences` | JWT | `monitor.view` / `monitor.manage` | |
| DELETE | `/silences/{silenceId}` | JWT | `monitor.manage` | Expire now |
| GET / POST | `/orgs/{orgId}/notification-channels` | JWT | `channel.view` / `channel.manage` | Telegram, Slack, Email, Webhook (secrets write-only) |
| PATCH / DELETE | `/notification-channels/{channelId}` | JWT | `channel.manage` | **(IM)** |
| POST | `/notification-channels/{channelId}/test` | JWT | `channel.manage` | Send a test message in the channel/recipient locale |

## 10. Logs

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/orgs/{orgId}/logs` | JWT | `logs.view` | `?q=` full-text, `source`, `source_id`, `level`, `from`, `to` |
| POST | `/ingest/logs` | Agent or API token (`logs:write` scope) | | Batch ingest (NDJSON, 1 MiB) |

## 11. Audit log

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/orgs/{orgId}/audit-log` | JWT | `audit.view` | Filter actor, action, resource type/id, from/to |
| GET | `/orgs/{orgId}/audit-log/export` | JWT | `audit.export` | CSV stream (UTF-8 with BOM so Excel shows Khmer correctly); the export itself is audited |

## 12. Dashboard

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/orgs/{orgId}/dashboard/pipelines` | JWT | `project.view` | Success rate, duration trend (p50/p95), `?project_id&from&to` |
| GET | `/orgs/{orgId}/dashboard/dora` | JWT | `project.view` | Deployment frequency, lead time, change failure rate, MTTR `?project_id&environment&from&to` |

## Error codes (planned, by module)

Generic: `INTERNAL`, `BAD_REQUEST`, `VALIDATION_FAILED`, `UNAUTHENTICATED`, `FORBIDDEN`, `NOT_FOUND`,
`ROUTE_NOT_FOUND`, `METHOD_NOT_ALLOWED`, `CONFLICT`, `PAYLOAD_TOO_LARGE`, `RATE_LIMITED`,
`SERVICE_UNAVAILABLE`, `VERSION_CONFLICT`, `PRECONDITION_REQUIRED`, `IDEMPOTENCY_KEY_REUSED`.

Module-specific examples: `INVALID_CREDENTIALS`, `ACCOUNT_LOCKED`, `EMAIL_NOT_VERIFIED`,
`MFA_REQUIRED`, `MFA_INVALID_CODE`, `REFRESH_TOKEN_REUSED`, `PASSWORD_TOO_WEAK`, `PASSWORD_BREACHED`,
`LAST_OWNER`, `ALREADY_MEMBER`, `MEMBER_NOT_FOUND`, `ROLE_NOT_ALLOWED`, `INVITATION_NOT_FOUND`,
`INVITATION_EMAIL_MISMATCH`, `TEAM_NOT_FOUND`, `CONFIRMATION_MISMATCH`, `ORG_NOT_FOUND`, `PROJECT_NOT_FOUND`, `SLUG_TAKEN`, `WEBHOOK_SIGNATURE_INVALID`,
`ENVIRONMENT_NOT_FOUND`, `ENVIRONMENT_NAME_TAKEN`,
`ENVIRONMENT_LIMIT_REACHED`, `REPOSITORY_NOT_FOUND`, `GIT_REPO_NOT_FOUND`, `GIT_ACCESS_DENIED`,
`GIT_PROVIDER_UNREACHABLE`, `IDEMPOTENCY_KEY_IN_PROGRESS`,
`PIPELINE_INVALID`, `PIPELINE_NOT_FOUND`, `RUN_NOT_CANCELABLE`, `APPROVAL_NOT_ALLOWED`,
`ENVIRONMENT_PROTECTED`, `DEPLOYMENT_NOT_FOUND`, `NOTHING_TO_ROLL_BACK`, `TARGET_UNREACHABLE`,
`SECRET_NOT_FOUND`, `SECRET_NAME_TAKEN`, `RUNNER_TOKEN_INVALID`, `SSRF_BLOCKED`.
Each has EN + KM entries in `web/src/locales/*/errors.json` (enforced by a Go test).
