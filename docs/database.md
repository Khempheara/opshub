# Database design

PostgreSQL 17. Migrations live in `db/migrations` (golang-migrate, every `up` has a `down`) and are
embedded in the API binary. Queries live in `db/queries` and are compiled to Go by sqlc
(`make sqlc`); CI fails if generated code is stale.

## Conventions

| Rule | Detail |
|---|---|
| Primary keys | `uuid DEFAULT uuid_generate_v7()` (time-ordered; PG 17 has no native v7, see migration 000001) |
| Timestamps | `created_at`, `updated_at` as `timestamptz` (UTC); `updated_at` maintained by the `set_updated_at` trigger |
| Soft delete | `deleted_at timestamptz` on user-facing entities (users, orgs, teams, projects, environments, targets). Unique indexes are partial: `WHERE deleted_at IS NULL` |
| Flexible config | `jsonb` (org settings, pipeline definitions, target configs, alert rule params) |
| Enums | Postgres enums for small, stable sets (`member_role`); `text` + `CHECK` where values may grow |
| Case-insensitive text | `citext` for emails |
| Secrets at rest | Never plaintext: `bytea` ciphertext + key id (envelope encryption) |
| Foreign keys | Always declared, with an index on the referencing column |
| Append-only | `audit_log` rejects UPDATE/DELETE/TRUNCATE via trigger |
| Optimistic locking | Editable entities carry `version int NOT NULL DEFAULT 1`; updates use `WHERE id = $1 AND version = $2` and `SET version = version + 1`; 0 rows → `409 VERSION_CONFLICT` |
| Tenancy | Every tenant-owned table has `organization_id NOT NULL` (denormalized onto child tables such as jobs, deployments and secrets, so every query can filter by it directly) |

## Entity-relationship model

Implemented tables are marked **(✓ 000001)**. Everything else is the target design; each module adds
its tables in its own migration when it is built.

```mermaid
erDiagram
  users ||--o{ organization_members : ""
  users ||--o{ refresh_tokens : ""
  users ||--o{ api_tokens : ""
  users ||--o{ user_identities : "OIDC"
  users ||--o{ email_tokens : ""
  users ||--o{ user_recovery_codes : "2FA"
  organizations ||--o{ organization_members : ""
  organizations ||--o{ teams : ""
  organizations ||--o{ invitations : ""
  organizations ||--o{ idempotency_keys : ""
  teams ||--o{ team_members : ""
  organizations ||--o{ projects : ""
  projects ||--o{ project_members : ""
  projects ||--o| repositories : ""
  projects ||--o{ environments : ""
  environments ||--o{ protection_rules : ""
  projects ||--o{ pipelines : ""
  pipelines ||--o{ pipeline_runs : ""
  pipeline_runs ||--o{ jobs : ""
  jobs ||--o{ steps : ""
  jobs ||--o{ artifacts : ""
  jobs }o--o| runners : "assigned"
  jobs ||--o{ approvals : "manual gates"
  organizations ||--o{ runners : ""
  organizations ||--o{ deploy_targets : ""
  environments ||--o{ deployments : ""
  deployments }o--|| deploy_targets : ""
  deployments }o--o| pipeline_runs : "triggered by"
  deployments ||--o| deployments : "rollback of"
  projects ||--o{ secrets : ""
  secrets ||--o{ secret_versions : ""
  organizations ||--o{ infra_assets : ""
  infra_assets ||--o{ asset_metrics : "heartbeat"
  organizations ||--o{ monitors : ""
  monitors ||--o{ monitor_results : ""
  organizations ||--o{ alert_rules : ""
  alert_rules ||--o{ alerts : ""
  alerts }o--o{ notification_channels : ""
  organizations ||--o{ log_entries : ""
  organizations ||--o{ audit_log : ""
```

## Tables by module

### 1. Auth & users **(✓ 000001)**

| Table | Key columns | Notes |
|---|---|---|
| `users` | `email citext`, `password_hash`, `email_verified_at`, `locale ('en'\|'km')`, `timezone`, `khmer_numerals`, `totp_secret_enc`, `totp_enabled_at`, `is_platform_admin`, `disabled_at`, `version` | `password_hash` NULL for SSO-only users. Locale/timezone drive UI and notification language |
| `user_identities` | `provider`, `subject`, `user_id` | OIDC links; `UNIQUE (provider, subject)` |
| `sessions` | `user_id`, `auth_method (password\|oidc)`, `ip`, `user_agent`, `expires_at` (absolute, 30 days), `last_used_at`, `revoked_at`, `revoke_reason` | One signed-in device; the access JWT carries its id (`sid`). Revoking it ends the whole refresh-token family |
| `refresh_tokens` | `session_id`, `parent_id`, `token_hash bytea UNIQUE`, `expires_at` (14 days, sliding, capped by the session), `used_at` | Rotation: each refresh marks the token used and issues a child. Presenting a used token = **reuse** → the session is revoked |
| `mfa_challenges` | `user_id`, `token_hash`, `expires_at` (5 min), `used_at`, `attempts` | Bridges password step → TOTP step |
| lockout columns on `users` | `failed_login_count`, `lockout_level`, `locked_until` | 5 failures → 15 min lock, doubling per repeat (max 24 h); reset on success |
| `api_tokens` | `token_prefix`, `token_hash`, `scopes text[]`, `expires_at`, `last_used_at`, `revoked_at` | Format `ohp_<prefix>_<secret>` |
| `email_tokens` | `purpose (verify_email\|reset_password)`, `token_hash`, `expires_at`, `used_at` | Single use |
| `user_recovery_codes` | `code_hash`, `used_at` | TOTP backup codes |

### 2. Organizations, teams, RBAC **(✓ 000001 org/team tables, ✓ 000002 invitations)**

| Table | Key columns | Notes |
|---|---|---|
| `organizations` | `slug`, `name`, `settings jsonb` | Tenant boundary. Every tenant-owned table carries `organization_id` |
| `organization_members` | `(organization_id, user_id)`, `role member_role` | `owner\|admin\|developer\|viewer` |
| `teams`, `team_members` | `teams.description` (≤ 500), `teams.version` | Grouping for project access grants. `version` for optimistic locking (000002) |
| `invitations` | `organization_id`, `email citext`, `role`, `token_hash bytea UNIQUE`, `invited_by`, `expires_at` (7 days), `accepted_at`, `accepted_by`, `revoked_at` | At most one open invitation per `(organization_id, email)` (partial unique index). Only the token hash is stored |

### 3. Projects & repositories **(✓ 000003)**

| Table | Key columns | Notes |
|---|---|---|
| `projects` | `organization_id`, `slug` (unique per org among live projects), `name`, `description`, `default_branch`, `version`, `created_by`, `deleted_at` | Soft delete |
| `project_members` | `project_id`, `user_id` **or** `team_id` (exactly one), `role` (never `owner`) | Direct and team grants; removed when the user leaves the org or the team is deleted. See docs/rbac.md for resolution |
| `repositories` | `id` (app-generated, used as AES-GCM AAD), `project_id UNIQUE`, `provider (github\|gitlab)`, `base_url` (self-hosted, https only), `full_name`, `external_id`, `web_url`, `clone_url`, `default_branch`, `access_token_enc`, `webhook_secret_enc`, `webhook_mode (automatic\|manual)`, `webhook_id`, `last_delivery_at` | Token + webhook secret encrypted; `webhook_id` set iff OpsHub created the hook |
| `webhook_deliveries` | `repository_id`, `delivery_id`, `event`, `ref`, `commit_sha`, `signature_valid`, `payload jsonb` (valid and ≤ 1 MB only), `received_at` | Unique `(repository_id, delivery_id)` among **valid** deliveries, so forged requests can't block a real one. Purged after 30 days |
| `environments` | `project_id`, `name` (slug-like, unique per project among live ones), `kind (development\|staging\|production)`, `variables jsonb` (object, non-secret), `version`, `deleted_at` | ≤ 20 per project, ≤ 100 variables |
| `protection_rules` | `environment_id` PK, `required_approvals 0–10`, `allowed_branches text[]` (globs; empty = any), `allowed_roles member_role[]` | 1:1 with a protected environment |
| `idempotency_keys` | `user_id`, `key`, `request_hash`, `response_status`, `response_body`, `expires_at` (24 h) | Unique `(user_id, key)`; purged hourly |

### 4. Pipelines **(✓ 000004)**

One pipeline per project, defined in `.opshub.yml`; there is no `pipelines` table.

| Table | Key columns | Notes |
|---|---|---|
| `projects.last_run_number` | | Incremented under a row lock to number runs per project |
| `pipeline_runs` | `project_id`, `number` (`UNIQUE` per project), `status (queued\|running\|waiting\|succeeded\|failed\|canceled)`, `trigger (push\|pull_request\|tag\|manual\|schedule)`, `ref`, `commit_sha`, `title`, `actor_name`, `created_by`, `rerun_of`, `definition jsonb` (snapshot), `problems jsonb` (invalid file), `variables jsonb`, `started_at`, `finished_at` | Status is derived from the jobs by the engine |
| `pipeline_jobs` | `run_id`, `organization_id`, `name`, `stage`, `stage_index`, `needs text[]` (resolved), `condition`, `environment`, `environment_id`, `runs_on text[]`, `spec jsonb`, `status (created\|waiting_approval\|queued\|running\|succeeded\|failed\|canceled\|skipped)`, `attempt`, `runner_id`, `timeout_seconds`, `exit_code`, `failure_reason`, `log_bytes` | One row per attempt, `UNIQUE (run_id, name, attempt)`; the highest attempt is current. Dispatch index `(organization_id, queued_at) WHERE status = 'queued'` |
| `job_steps` | `(job_id, index)`, `name`, `command`, `status`, `exit_code`, timings | |
| `job_log_chunks` | `(job_id, seq)`, `content` | Runner-chosen `seq` makes uploads idempotent; masked before insert; ≤ 10 MiB per job |
| `job_approvals` | `job_id`, `user_id`, `decision`, `comment` | `UNIQUE (job_id, user_id)` |
| `pipeline_schedules` | `project_id`, `cron`, `next_run_at`, `last_run_at` | Synced from the default branch's file; a minute tick enqueues due ones |

### 5. Runners **(✓ 000005)**

| Table | Key columns | Notes |
|---|---|---|
| `runners` | `organization_id`, `name`, `labels text[]`, `token_hash` (`UNIQUE`), `token_prefix`, `version`, `os`, `arch`, `max_concurrency (1–64)`, `last_seen_at`, `disabled_at`, `row_version` | Registered with a one-time token; `pipeline_jobs.runner_id` references it (`ON DELETE SET NULL`) |
| `runner_registration_tokens` | `organization_id`, `token_hash`, `labels`, `expires_at`, `used_at`, `runner_id` | One-time, 1 h; purged 7 days after expiry |
| `job_tokens` | `job_id` (PK), `token_hash`, `expires_at` (job timeout + 10 min) | Issued in the claim transaction; scopes runner calls to one job |
| `artifacts` | `job_id` (`UNIQUE`), `project_id`, `organization_id`, `name`, `size_bytes`, `sha256`, `storage_key`, `expires_at` | One gzip tar per job in the local blob store (D5); expired rows and blobs are removed by housekeeping |
| `cache_entries` | `(project_id, key)` `UNIQUE`, `storage_key`, `size_bytes`, `sha256`, `last_used_at` | Least recently used entries beyond the project quota are evicted |

Tokens are stored as SHA-256 hashes only. Blobs live under `OPSHUB_BLOB_DIR`
(`artifacts/<project>/<job>-<random>.tar.gz`, `cache/<project>/<random>`); a replaced cache
entry's old blob is deleted after the new one is recorded.

### 6. Deployments **(✓ 000006)**

| Table | Key columns | Notes |
|---|---|---|
| `deploy_targets` | `organization_id`, `name` (`UNIQUE` per org), `kind (ssh\|docker\|kubernetes)`, `description`, `config jsonb`, `credentials_enc`, `last_test_at`, `last_test_ok`, `version` | Credentials: AES-GCM with the master key ring, the row id as associated data; never returned |
| `deployments` | `project_id`, `environment_id`, `number` (`UNIQUE` per project), `target_id` (`SET NULL`), `target_name`, `target_kind`, `version`, `previous_version`, `strategy (rolling\|blue_green)`, `status (pending\|running\|succeeded\|failed)`, `failure_reason`, `reverted`, `health jsonb`, `run_id`, `job_id`, `rollback_of_id`, `created_by`, timings | Full release history; one active deployment per environment (partial unique index); a rollback is a new row pointing at the one it replaces |
| `deployment_log_chunks` | `(deployment_id, seq)`, `content` | ≤ 5 MiB per deployment |
| `environments.current_deployment_id` | | The release the environment runs now |
| `projects.last_deployment_number` | | Numbers deployments per project |

### 7. Infrastructure inventory **(✓ 000007)**

| Table | Key columns | Notes |
|---|---|---|
| `infra_assets` | `organization_id`, `kind (server\|cluster\|database\|domain)`, `name` (`UNIQUE` per org), `address`, `description`, `tags text[]` (≤ 20), `metadata jsonb`, `tls_port`, `agent_token_hash` (`UNIQUE`), `agent_token_prefix`, `agent_version/hostname/os/arch`, `last_heartbeat_at`, `last_metrics jsonb`, `version` | `updated_at` changes only with `version` (edits, token rotation), not with heartbeats |
| `asset_metrics` | `(asset_id, ts)`, `cpu_pct`, `mem_pct`, `disk_pct`, `load1` | `PARTITION BY RANGE (ts)`, one partition per month (`asset_metrics_YYYYMM`); current + previous month kept |
| `asset_metrics_hourly` | `(asset_id, hour)`, average and peak per metric | Rollups kept 400 days |
| `ssl_certificates` | `asset_id` (domain, PK), `host`, `port`, `subject`, `issuer`, `dns_names`, `serial`, `fingerprint`, `not_before`, `not_after`, `error`, `last_checked_at`, `next_check_at` | Last known dates survive a failed check |

`opshub_maintain_metric_partitions(keep_months)` creates this month's and the next two months'
partitions and drops older ones. It is `SECURITY DEFINER` (owned by `opshub_migrator`,
`search_path` pinned, `EXECUTE` revoked from `PUBLIC` and granted to `opshub_app`), so the hourly
job can maintain partitions while the application role keeps no DDL rights.

### 8. Secrets

| Table | Key columns | Notes |
|---|---|---|
| `secrets` | `project_id`, `environment_id NULL` (= all envs), `name`, `current_version`, `deleted_at` | `UNIQUE (project_id, environment_id, name)` |
| `secret_versions` | `secret_id`, `version`, `ciphertext bytea`, `nonce bytea`, `dek_enc bytea`, `kek_id text`, `created_by` | Envelope encryption: random DEK per version (AES-256-GCM), DEK wrapped by the master key (KEK). Rotation = new version and/or re-wrap DEKs under a new KEK |

Every secret **read** (runner fetch or reveal) writes an `audit_log` row with action `secret.read`.

### 9. Monitoring & alerts

| Table | Key columns | Notes |
|---|---|---|
| `monitors` | `organization_id`, `kind (http\|tcp\|ssl)`, `target`, `interval_seconds`, `timeout_ms`, `expected jsonb` | Scheduled by River periodic jobs |
| `monitor_results` | `monitor_id`, `ts`, `up bool`, `latency_ms`, `error` | Partitioned by month |
| `alert_rules` | `organization_id`, `source (monitor\|metric)`, `condition jsonb`, `threshold`, `for_seconds`, `severity`, `escalation jsonb` | |
| `alerts` | `rule_id`, `status (firing\|resolved)`, `started_at`, `resolved_at`, `silenced_until`, `acknowledged_by` | Also feeds MTTR |
| `silences` | `organization_id`, `matchers jsonb`, `starts_at`, `ends_at`, `created_by` | |
| `notification_channels` | `organization_id`, `kind (telegram\|slack\|email\|webhook)`, `config_enc`, `locale NULL` | Channel locale overrides recipient locale |

### 10. Logs

| Table | Key columns | Notes |
|---|---|---|
| `log_entries` | `organization_id`, `source (service\|deployment\|job)`, `source_id`, `level`, `ts`, `message`, `attributes jsonb`, `search tsvector GENERATED` | Range-partitioned by day; GIN index on `search`; retention job drops partitions older than the configured window (default 30 days) |

Full-text search uses the `simple` configuration (language-agnostic, so Khmer and English content are
both indexed as tokens; log content is never translated).

### 11. Audit log **(✓ 000001)**

`audit_log(organization_id, actor_user_id, actor_type, action, resource_type, resource_id, ip, user_agent, before jsonb, after jsonb, metadata jsonb, created_at)`
— append-only, indexed by `(organization_id, created_at DESC)`, exported as CSV with keyset pagination.

### Job queue (River)

River's own tables (`river_job`, `river_leader`, `river_queue`, …) are created by River's migrator,
run from our migration pipeline as a pinned step (`cmd/api migrate` applies ours, then River's) so
schema changes stay versioned and reversible.

### 12. Dashboard & DORA metrics

Computed from existing tables (no new source of truth), materialized daily by a River job into
`dora_daily(organization_id, project_id, day, deployments, lead_time_p50_s, change_failures, mttr_s)`:

| Metric | Source |
|---|---|
| Deployment frequency | `deployments` succeeded to production environments per day |
| Lead time for changes | `deployments.finished_at` − commit time of `pipeline_runs.commit_sha` |
| Change failure rate | production deployments that were rolled back or followed by a firing alert / failed status ÷ total |
| MTTR | `alerts.resolved_at − started_at`, and failed-deploy → next-successful-deploy intervals |

## PostgreSQL roles (least privilege)

| Role | Privileges | Used by |
|---|---|---|
| `opshub_migrator` | Owns schema; DDL | `cmd/api migrate` (Helm pre-upgrade Job / `make migrate-up`) |
| `opshub_app` | `SELECT, INSERT, UPDATE, DELETE` on app tables; `INSERT, SELECT` only on `audit_log`; no `TRUNCATE`, no DDL (metric partitions via one `SECURITY DEFINER` function, §7) | API + workers |
| `opshub_backup` | `pg_read_all_data` | Backup job |

In local dev a single superuser is used for convenience; `docker-compose.yml` creates the three roles to
mirror production.

## Seed data

`make seed` (`cmd/api seed`, idempotent) creates the demo org **Angkor Tech / អង្គរ តិច**:
users `owner@demo.opshub.local`, `admin@…`, `dev@…`, `viewer@…` (one per role; one with `locale=km`),
project **Payments API / API ទូទាត់ប្រាក់** with dev/staging/production environments (production
protected), a sample `.opshub.yml`, one successful and one failed run, deployments incl. a rollback,
infra assets, monitors and a firing alert. Descriptions are stored in both languages
(`"description": {"en": "…", "km": "…"}` in seed files; the UI shows the active language). Demo
passwords are printed once and only in development.

## Backup & restore

**Strategy:** nightly logical backups with `pg_dump --format=custom` (compressed, parallel restore),
retained 7 daily / 4 weekly / 3 monthly; backups encrypted at rest (age or storage-side encryption) and
copied off-host. For RPO below 24 h, enable WAL archiving/PITR (e.g. pgBackRest or a managed
Postgres). The secrets master key (KEK) is **not** in the database and must be backed up separately.
Without it, restored secrets cannot be decrypted.

```bash
# Backup (custom format, schema + data)
pg_dump --format=custom --no-owner --file=opshub-$(date -u +%Y%m%dT%H%M%SZ).dump "$OPSHUB_BACKUP_DATABASE_URL"

# Restore into an empty database, then verify
pg_restore --clean --if-exists --no-owner --jobs=4 --dbname="$TARGET_DATABASE_URL" opshub-<ts>.dump
opshub-api migrate status   # schema version must match the binary
```

Deliverables with the module work: `deploy/backup/backup.sh` + `restore.sh`, a `backup` service in
docker-compose (cron schedule), a Helm `CronJob` example, and a quarterly restore-drill runbook.
