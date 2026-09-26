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

Pipeline syntax and behavior: [pipelines.md](pipelines.md).

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| POST | `/projects/{projectId}/pipeline/validate` | JWT | `project.view` | Lint a `.opshub.yml` body → `{valid, problems[line, column, path, rule, param], definition}` |
| GET | `/projects/{projectId}/runs` | JWT | `run.view` | `?status&trigger&ref`, cursor pagination, newest first |
| POST | `/projects/{projectId}/runs` | JWT | `pipeline.trigger` | Manual run `{ref, variables}` — reads `.opshub.yml` at the commit **(IK)**; `REF_NOT_FOUND`, `PIPELINE_FILE_NOT_FOUND`, `PIPELINE_INVALID` |
| GET | `/runs/{runId}` | JWT | `run.view` | Run + stages + current attempt of every job with steps |
| POST | `/runs/{runId}/cancel` | JWT | `run.cancel` | Cancels unfinished jobs; `RUN_NOT_CANCELABLE` when finished |
| POST | `/runs/{runId}/rerun` | JWT | `pipeline.trigger` | New run from the same snapshot (201), or `{failed_only: true}` in place (200) **(IK)** |
| GET | `/runs/{runId}/events` | JWT | `run.view` | **SSE** `update` on connect and on every run/job change; streams end after 5 min |
| GET | `/jobs/{jobId}` | JWT | `run.view` | Steps, attempts, approval gate (`can_decide`, `denied_reason`) |
| POST | `/jobs/{jobId}/retry` | JWT | `pipeline.trigger` | New attempt + the jobs after it **(IK)** |
| GET | `/jobs/{jobId}/logs` | JWT | `run.view` | `?after_seq&limit` → `{items, next_seq, complete}`; `Accept: text/plain` downloads the whole log |
| GET | `/jobs/{jobId}/logs/stream` | JWT | `run.view` | **SSE** `log` (id = seq) … `end`; resume with `Last-Event-ID` |
| POST | `/jobs/{jobId}/approvals` | JWT | `approval.decide` | `{decision, comment}`; protection rules enforced; approver ≠ run starter; `APPROVAL_NOT_ALLOWED` with `details.reason` |

Webhook pushes, tag pushes and pull requests start runs through a background job after the
delivery is recorded (see §3). Artifacts are listed and downloaded through §5.

## 5. Runners **(✓ Module 5)**

Runner agents and their API; see [runners.md](runners.md). Runner and job tokens are not
sessions: other endpoints treat them as anonymous.

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/orgs/{orgId}/runners` | JWT | `runner.view` | Status (online < 30 s since heartbeat / offline / disabled), labels, version, running jobs |
| POST | `/orgs/{orgId}/runner-registration-tokens` | JWT | `runner.manage` | One-time token `ohr_reg_…` (shown once, 1 h) with optional extra labels |
| PATCH | `/runners/{runnerId}` | JWT | `runner.manage` | Name, labels, max concurrency, disabled **(IM)** |
| DELETE | `/runners/{runnerId}` | JWT | `runner.manage` | Revokes the token; its running jobs fail `runner_lost` |
| GET | `/jobs/{jobId}/artifacts` | JWT | `run.view` | A job's unexpired artifact archives |
| GET | `/artifacts/{artifactId}/download` | JWT | `run.view` | gzip-compressed tar; `X-Content-SHA256` |
| POST | `/runner/register` | registration token (body) | | → runner id + runner token `ohr_…` (shown once); 10/min per IP |
| POST | `/runner/heartbeat` | runner token | | Every 10 s; reports running jobs → `cancel_job_ids`. Assigned jobs it doesn't report fail `runner_lost` after 30 s |
| POST | `/runner/jobs/request` | runner token | | Long-poll ≤ 30 s → job, spec, variables, job token `ohj_…` (204 when none or at capacity) |
| PATCH | `/runner/jobs/{jobId}` | job token | | `{step: {index, status, exit_code}}` or `{complete: {success, exit_code, reason}}` |
| POST | `/runner/jobs/{jobId}/logs` | job token | | `{seq, content}` ≤ 256 KiB; re-sent seqs are ignored |
| GET | `/runner/jobs/{jobId}/source` | job token | | Tarball of the run's commit through OpsHub (404 `REPOSITORY_NOT_FOUND` → empty workspace) |
| POST | `/runner/jobs/{jobId}/artifacts` | job token | | One gzip tar per job (`OPSHUB_ARTIFACT_MAX_BYTES`, default 100 MiB) |
| GET | `/runner/jobs/{jobId}/dependencies[/{artifactId}]` | job token | | Artifacts of the jobs in `needs` (latest successful attempt) |
| GET / PUT | `/runner/jobs/{jobId}/cache/{key}` | job token | | Project-scoped cache archives; LRU eviction beyond the project quota |

Job tokens are valid only while their job runs (at most its timeout + 10 minutes); calls for
a finished or canceled job answer `409 JOB_NOT_RUNNING`, which tells the runner to stop.

## 6. Deployments **(✓ Module 6)**

See [deployments.md](deployments.md). Credentials are write-only: answers list only the names
of the credential fields that are set.

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET / POST | `/orgs/{orgId}/deploy-targets` | JWT | `target.view` / `target.manage` | SSH / Docker / Kubernetes targets; `config` and `credentials` by `kind` |
| GET / PATCH / DELETE | `/deploy-targets/{targetId}` | JWT | `target.view` / `target.manage` | PATCH replaces description and config, and credentials when given **(IM)**; name and kind are fixed; DELETE refused while a deployment runs (`DEPLOY_TARGET_IN_USE`) |
| POST | `/deploy-targets/{targetId}/test` | JWT | `target.manage` | Connectivity checks; SSH host key fingerprints to pin (no credentials sent to unpinned hosts) |
| GET | `/projects/{projectId}/deployments` | JWT | `deployment.view` | Release history; filter `environment_id`, `status`, `from`, `to`, `current=true` |
| POST | `/environments/{environmentId}/deployments` | JWT | `deployment.create` | `{target_id, version, strategy}` → 202 **(IK)**; protection: allowed roles, no approvals (`ENVIRONMENT_PROTECTED`); one at a time (`DEPLOYMENT_IN_PROGRESS`) |
| GET | `/deployments/{deploymentId}` | JWT | `deployment.view` | Status, health checks, previous version, run/job link |
| GET | `/deployments/{deploymentId}/logs/stream` | JWT | `deployment.view` | **SSE**: `status`, `log` (resumable), `end` |
| POST | `/deployments/{deploymentId}/rollback` | JWT | `deployment.rollback` | The environment's current release → the one before it, 202 **(IK)** (`NOTHING_TO_ROLL_BACK`) |

## 7. Infrastructure **(✓ Module 7)**

See [infrastructure.md](infrastructure.md).

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET / POST | `/orgs/{orgId}/assets` | JWT | `infra.view` / `infra.manage` | Filter `kind`, `tag`, `status`, `q` (name or address); answers include status, agent, latest metrics and certificate summary |
| GET / PATCH / DELETE | `/assets/{assetId}` | JWT | `infra.view` / `infra.manage` | PATCH **(IM)**; kind is fixed; changing a domain's address or port discards its certificate |
| GET | `/assets/{assetId}/metrics` | JWT | `infra.view` | `?from&to&step` → average and peak CPU/memory/disk per step; raw samples (≤ 30 days back) or hourly rollups; ≤ 1000 points |
| POST | `/assets/{assetId}/agent-token` | JWT | `infra.manage` | Servers only (`AGENT_NOT_SUPPORTED`); issues or rotates the `ohi_…` token, shown once |
| GET | `/assets/{assetId}/certificate` | JWT | `infra.view` | `{certificate: null \| {…}}` for domains |
| POST | `/assets/{assetId}/certificate/check` | JWT | `infra.manage` | Probe now; a failed probe is a 200 with `error` set |
| GET | `/orgs/{orgId}/certificates` | JWT | `infra.view` | Every domain's certificate, soonest expiry first; `?expiring_within=30d` keeps those expiring within the window or failing |
| POST | `/agent/heartbeat` | Agent (`ohi_…`) | | Version, hostname, os/arch and an optional metrics sample → `{interval_seconds}` |

## 8. Secrets **(✓ Module 8)**

See [secrets.md](secrets.md). Values are write-only: no response contains one.

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/projects/{projectId}/secrets` | JWT | `secret.list` | Names and metadata; `?environment_id=<id>` or `none` (all-environments secrets only); `can_manage` per secret |
| POST | `/projects/{projectId}/secrets` | JWT | `secret.create` | `{name, environment_id?, description, value}`; Developers only for unprotected environments; ≤ 500 per project (`SECRET_LIMIT_REACHED`) |
| GET | `/secrets/{secretId}` | JWT | `secret.list` | Metadata, current version |
| PATCH | `/secrets/{secretId}` | JWT | `secret.update` | Description only **(IM)** |
| POST | `/secrets/{secretId}/versions` | JWT | `secret.rotate` | New value → next version; older values destroyed |
| GET | `/secrets/{secretId}/versions` | JWT | `secret.list` | Who set each version and when; when its value was destroyed |
| DELETE | `/secrets/{secretId}` | JWT | `secret.delete` | Soft delete; every value destroyed |

Runners receive a job's `secrets:` decrypted in `POST /runner/jobs/request`, audited as
`secret.read`. Master-key (KEK) rotation is an operator CLI: `opshub-api keys rotate` re-wraps
secret data keys and re-encrypts every other value stored under the key ring.

## 9. Monitoring & alerts **(✓ Module 9)**

See [monitoring.md](monitoring.md).

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET / POST | `/orgs/{orgId}/monitors` | JWT | `monitor.view` / `monitor.manage` | HTTP / TCP / SSL checks; filter `label`, `q`, `status`; list includes 24 h uptime |
| GET / PATCH / DELETE | `/monitors/{monitorId}` | JWT | `monitor.view` / `monitor.manage` | Pause via `enabled=false` **(IM)**; kind is fixed |
| GET | `/monitors/{monitorId}/results` | JWT | `monitor.view` | `?from&to&step` uptime % and latency per step (raw ≤ 30 days back, then hourly), latest 20 checks |
| GET / POST | `/orgs/{orgId}/alert-rules` | JWT | `monitor.view` / `monitor.manage` | Condition kind, target or label, threshold, metric, `for_seconds`, severity, escalation steps |
| GET / PATCH / DELETE | `/alert-rules/{ruleId}` | JWT | `monitor.view` / `monitor.manage` | **(IM)**; disabling or deleting resolves its alerts |
| GET | `/orgs/{orgId}/alerts` | JWT | `monitor.view` | Firing first, then resolved; filter `status`, `severity`, `before` |
| GET | `/alerts/{alertId}` | JWT | `monitor.view` | With its timeline (fired, notified, notify_failed, escalated, acknowledged, silenced, resolved) |
| POST | `/alerts/{alertId}/acknowledge` | JWT | `alert.ack` | Stops escalation (`ALERT_NOT_FIRING` otherwise) |
| GET / POST | `/orgs/{orgId}/silences` | JWT | `monitor.view` / `monitor.manage` | Rule / subject / label / severity matchers, ≤ 90 days; `?include_expired=true` |
| DELETE | `/silences/{silenceId}` | JWT | `monitor.manage` | End now |
| GET / POST | `/orgs/{orgId}/notification-channels` | JWT | `channel.view` / `channel.manage` | Telegram, Slack, Email, Webhook (credentials write-only) |
| PATCH / DELETE | `/notification-channels/{channelId}` | JWT | `channel.manage` | **(IM)**; empty secrets keep the stored ones; DELETE refused while rules use it (`CHANNEL_IN_USE`) |
| POST | `/notification-channels/{channelId}/test` | JWT | `channel.manage` | Sends a test message in the channel or recipient locale → `{ok, error}` |

## 10. Logs **(✓ Module 10)**

See [logs.md](logs.md).

| Method | Path | Auth | Action | Description |
|---|---|---|---|---|
| GET | `/orgs/{orgId}/logs` | JWT | `logs.view` | Newest first. `from`, `to` (≤ 31 days, default last hour), `q` (full text; Khmer script matches as a substring), `source`, `service`, `level` (minimum), `before` / `after` cursors, `limit` ≤ 500. Job and deployment lines only for projects the caller sees |
| GET | `/orgs/{orgId}/logs/services` | JWT | `logs.view` | Service names seen in the last day |
| GET / POST | `/orgs/{orgId}/log-ingest-tokens` | JWT | `logs.manage` | One token (`ohl_…`) per service name; shown once |
| DELETE | `/log-ingest-tokens/{tokenId}` | JWT | `logs.manage` | Revoke |
| POST | `/ingest/logs` | Ingest token | | NDJSON, ≤ 1 MiB and 5,000 lines → `{accepted, rejected, errors[≤20]}` |

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
`SECRET_NOT_FOUND`, `SECRET_NAME_TAKEN`, `SSRF_BLOCKED`, `JOB_NOT_RUNNING`,
`RUNNER_NOT_FOUND`, `RUNNER_TOKEN_INVALID`, `REGISTRATION_TOKEN_INVALID`, `RUNNER_DISABLED`,
`JOB_TOKEN_INVALID`, `ARTIFACT_NOT_FOUND`, `CACHE_NOT_FOUND`, `DEPLOY_TARGET_NOT_FOUND`,
`DEPLOY_TARGET_NAME_TAKEN`, `DEPLOY_TARGET_IN_USE`, `DEPLOYMENT_NOT_FOUND`, `DEPLOYMENT_IN_PROGRESS`,
`STRATEGY_NOT_SUPPORTED`, `DEPLOY_NOT_ALLOWED`, `ASSET_NOT_FOUND`, `ASSET_NAME_TAKEN`,
`AGENT_TOKEN_INVALID`, `AGENT_NOT_SUPPORTED`, `SECRET_LIMIT_REACHED`, `MONITOR_NOT_FOUND`, `MONITOR_NAME_TAKEN`, `ALERT_RULE_NOT_FOUND`,
`ALERT_RULE_NAME_TAKEN`, `ALERT_NOT_FOUND`, `ALERT_NOT_FIRING`, `SILENCE_NOT_FOUND`, `CHANNEL_NOT_FOUND`,
`CHANNEL_NAME_TAKEN`, `CHANNEL_IN_USE`.
Each has EN + KM entries in `web/src/locales/*/errors.json` (enforced by a Go test).
