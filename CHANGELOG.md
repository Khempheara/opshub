# Changelog

## Module 12b — Operations

The second part of Module 12 (decision M12-1): monitoring OpsHub itself, backups, Kubernetes
and load testing.

### Built

**Platform metrics and Grafana** ([observability.md](docs/observability.md))
- **New `/metrics` figures:**
  - background (River) jobs by queue and state, and jobs discarded in the last hour;
  - pipeline jobs queued or running;
  - runners online, offline and disabled;
  - running deployments, firing alerts by severity, log lines ingested (accepted and rejected);
  - the database pool (connections, waits, acquire time) and `opshub_platform_metrics_up`.
- **How they are collected:** read in one query at each scrape (3 s timeout), and identical on
  every replica.
- **"OpsHub Platform" Grafana dashboard** next to the API dashboard, plus suggested alert rules
  in the docs.

**Backups** ([backup.md](docs/backup.md))
- **`opshub-backup` image:** PostgreSQL 17 client tools, age and rclone; runs as uid 70; gosu
  removed.
- **`opshub-backup`:**
  - `pg_dump` as the new read-only `opshub_backup` role, always encrypted with **age** to
    public keys;
  - written atomically with a `.sha256` beside it;
  - 7 daily, 4 weekly and 3 monthly kept (hard links);
  - optional **rclone** sync to S3-compatible storage, and a `last-success` marker.
- **`opshub-restore`:** verifies the checksum, decrypts, restores into an empty database
  (`OPSHUB_RESTORE_FORCE` to overwrite), and reports the schema version and row counts.
- **Scheduling:** the opt-in compose `backup` service runs daily at `OPSHUB_BACKUP_AT`.
- **Drill:** `make backup-drill` (also in CI after the E2E suite) backs up, restores into a
  scratch database and compares row counts. Plus a quarterly drill runbook.

**Helm chart** (`deploy/helm/opshub`, [helm.md](docs/helm.md))
- **Workloads:** API with workers, web UI, a pre-install/pre-upgrade migration hook, and an
  optional backup CronJob with its volume.
- **Optional extras:** ServiceMonitor, NetworkPolicies, PodDisruptionBudgets.
- **Requirements:** external PostgreSQL, and secrets only from existing Secrets.
- **Security:** every pod non-root, read-only root filesystem, no privilege escalation, all
  capabilities dropped, `RuntimeDefault` seccomp.
- **Traffic:** the Ingress sends `/api` and `/docs` straight to the API so audit logs see
  client addresses.
- **Guard rails:**
  - more than one API replica requires a ReadWriteMany volume;
  - backups require age recipients;
  - a ReadWriteOnce volume updates with `Recreate`.
- **Web image:** the nginx upstream is now a template (`OPSHUB_API_UPSTREAM`, default
  `api:8080`), so the web UI runs under any service name.
- **Checks:** `make helm-lint` (helm lint --strict, kubeconform against Kubernetes 1.30,
  shellcheck) runs in CI. The chart was installed into a kind cluster: migrations, readiness,
  proxying, a backup job and an upgrade.

**Load testing** ([load-testing.md](docs/load-testing.md))
- **k6 test:** browsing by the four demo users and log ingest at a constant arrival rate, within
  the rate limits.
- **Thresholds:** failures < 1 %, reads p95 < 200 ms, dashboard p95 < 500 ms, search and ingest
  p95 < 300 ms, no 429s.
- **Running it:** `make load`, and a manually triggered **Load test** workflow.
- **Baseline:** p95 under 12 ms on the dev stack.

### Fixed

- **Silences started on the API server's clock** but were checked with the database's `now()`.
  With the API clock slightly ahead, a new silence could miss the next evaluation and let a
  notification through. This showed up as an intermittent failure of `TestSilences` under load.
  Immediate silences now start at the database's `now()`.
- **Dashboard charts:** the first and last date labels no longer get cut off at the edges.

### Quality

- **Go:**
  - Platform metrics against a real database (runners by status, alerts by severity, background
    jobs and discarded jobs, pool series, log counters), and `up=0` when the database can't
    answer.
  - The ingest observer.
  - The full race suite passed twice in a row. Service coverage 78.9 %.
- **Web:** 132 Vitest tests (+3 chart rendering: legend, accessible name, label anchors, stacked
  bars and tooltips) and 55 Playwright tests, passing twice in a row.
- **Operations:**
  - The backup drill passes (including about 10,000 log lines across partitions).
  - Retention keeps exactly N with checksums; a bad database URL fails with no partial file.
  - Missing or malformed age keys are refused.
- golangci-lint, gosec, govulncheck, ESLint, TypeScript, the i18n check and shellcheck are
  clean. Trivy finds 0 HIGH/CRITICAL in the api, web, runner and backup images.
- **New dependencies (backup image only):** `age` (encrypts each dump to public keys) and
  `rclone` (optional off-site copy). One indirect Go test module: `kylelemons/godebug`, used by
  Prometheus `testutil` for metric diffs.

## Module 12a — Dashboard & DORA metrics

The first of two parts of Module 12 (decision M12-1). Next, 12b: Grafana platform dashboard,
backup/restore job and runbook, Helm chart, k6 load tests.

### Built

**Backend** (`internal/dashboard`)
- **`GET /orgs/{id}/dashboard/pipelines`:**
  - Runs in the range, success rate (succeeded ÷ succeeded + failed) and median and p95
    duration.
  - A trend per day, or per week for ranges over 92 days, in the caller's time zone, with empty
    periods filled.
  - The busiest projects with their last run.
- **`GET /orgs/{id}/dashboard/dora`:**
  - **Deployment frequency.**
  - **Lead time for changes:** median and p95 from the commit to production.
  - **Change failure rate:** failed or rolled back later ÷ changes.
  - **Time to restore:** median, plus the number not restored yet.
  - Organization-wide **alert recovery**, a trend of changes that stayed and changes that failed,
    and per-project figures.
  - A change is a finished production deployment that isn't a rollback; `environment` picks one
    environment instead.
- **Scope:**
  - Computed on request (no materialized table): always current, with exact medians over up to
    366 days.
  - Covers the projects the caller can view; an ungranted project answers 404.
- **Commit times:**
  - Runs now record `committed_at` from the Git host (GitHub `commit.committer.date`, GitLab
    `committed_date`) for manual, scheduled, webhook and re-run runs.
  - A webhook run asks the host once more, best effort.
  - Without a commit time, lead time starts at the run.
- **Delivery:** migration `000012_dashboard` (column and indexes); 2 endpoints; OpenAPI 0.13.0.
- **Demo seed:** the **Checkout Web** project with 60 days of weekday runs and production
  deployments through staging, including failures, automatic reverts and a rollback (185 runs,
  143 deployments).

**Frontend**
- The organization **Overview** is now the dashboard:
  - The getting-started checklist stays until 2FA and a first project are done.
  - Project and range filters (7 days to 12 months); refreshes every minute.
  - Four DORA cards with their performance level (Elite/High/Medium/Low), a production-changes
    chart and alert recovery.
  - Pipeline cards, a runs chart, a run-duration chart (median and p95), and a projects table
    linking to each project.
- A small stacked-bar SVG chart. Durations now read in days when they are over 24 hours ("2 d
  3 h").
- New `dashboard` translation namespace (EN + KM).

### Quality

- **Go:**
  - Pipelines: rates, medians and p95, empty ranges, per-day trends and time zones, weekly
    buckets from Monday.
  - DORA: failed, reverted and rolled-back changes, lead time with and without commit time,
    restore time and open failures, staging excluded or chosen, alert recovery.
  - Developer and viewer visibility, validation, HTTP tenant isolation and route coverage.
  - Commit times parsed from GitHub and GitLab and stored for manual and webhook runs.
  - Coverage: dashboard 92.9 %; service coverage 78.9 %.
- **Web:** 129 Vitest tests (+8: levels, frequency units, project rows, periods, day durations)
  and 55 Playwright tests (+3):
  - Metrics and levels from the demo history, and the project and range filters.
  - A new organization's empty dashboard with getting started.
  - Khmer layout at phone and desktop widths, with and without data.
- **E2E fix:** specs now open the Payments API demo project by name instead of "the first
  project".
- golangci-lint, gosec, govulncheck, ESLint, TypeScript and the i18n check are clean. Trivy
  finds 0 HIGH/CRITICAL in the api, web and runner images. Migrations pass up/down/up; sqlc and
  orval are deterministic; the OpenAPI ↔ routes test passes. The full Playwright suite passed
  twice in a row.

### Next — Module 12b: Operations

Shipped in the next release above.

## Module 11 — Audit log

### Built

**Backend** (`internal/auditlog`; the recorder `internal/audit` is unchanged)
- **Organization audit log** (`audit.view`, Owners and Admins):
  - Newest first, with filters for area (the action before the dot), exact action, person,
    project, resource and time range, and keyset paging.
  - Each entry has the actor (person, API token, runner or OpsHub), project, IP, User-Agent,
    before/after values and metadata.
- **Sentences in English and Khmer:**
  - 89 action sentences (`audit.<action>`) in the Go bundle, rendered by the API in `?locale` or the
    Accept-Language language, so the page and the CSV read the same.
  - Names are resolved at read time: people, projects, and teams and assets mentioned by id.
  - Unknown actions fall back to "who: action (resource)".
- **Account activity** (`GET /me/activity`, signed-in sessions): each person's own sign-ins,
  failed sign-ins, lockouts, 2FA, password, token, session and linked sign-in events. These
  are never part of an organization's log.
- **CSV export** (`audit.export`):
  - Streams every matching entry in batches with all columns, including before/after/metadata
    JSON.
  - UTF-8 byte-order mark for Excel and Khmer; cells that look like formulas are escaped.
  - The export is recorded with its filter before the file is sent.
- **Retention:** kept forever (append-only, as before).
- **Delivery:** migration `000011_audit` (area, project, actor and account-activity indexes); 3
  endpoints; OpenAPI 0.12.0.

**Frontend**
- **Organization → Audit log** (sidebar, Owners and Admins):
  - Sentences with area icons, time, IP and action code.
  - Filters for area, person, project and time range, with load more.
  - Details for each entry: who, resource, project, device, a before/after table and extra
    values.
  - **Export CSV** button.
- **Settings → Security → Recent account activity** for everyone, with the device named from its
  User-Agent ("Chrome · macOS").
- New `audit` translation namespace (EN + KM).

### Quality

- **Go:**
  - Every sentence renders in both languages with no missing values, and a scan of the services
    fails when a recorded action has no sentence.
  - Sentence cases: roles, teams and assets by id, approvals, deployments by OpsHub, runner
    secret reads, API-token actors, deleted users, variants and the fallback.
  - Filters, validation, paging, permissions and other organizations.
  - Account activity stays personal.
  - CSV content: BOM, header, Khmer, formula escaping, JSON columns, the recorded export, and
    streaming across batches (1,203 rows).
  - HTTP: locale and Accept-Language, CSV headers, tenant isolation and route coverage.
  - Coverage: auditlog 94.0 %; service coverage 78.6 %.
- **Web:** 121 Vitest tests (+9: filters, areas, before/after changes, metadata, device names,
  file names) and 52 Playwright tests (+2):
  - An owner's team changes read as sentences and filter by area.
  - The export downloads a BOM-prefixed CSV and appears in the log.
  - Account activity shows sign-ins but not organization events.
  - Khmer layout at phone and desktop widths.
  - Developers get no audit log but see their own activity.
- **E2E fix:** a new `switchLanguage` helper waits for the profile to save the language before
  the next page load. This fixes the intermittent failures where a reload brought back the
  previous language (seen in the monitoring and audit specs).
- **Deployment rollback fix (Module 6):**
  - When a Docker release failed, restoring the previous container could fail silently if
    Docker hadn't yet released the removed container's port ("port is already allocated").
    This happened once on CI.
  - The rollback now retries the start for up to 15 s and writes every failed step to the
    deployment log. A unit test covers it against a fake Docker API.
- golangci-lint, gosec, govulncheck, ESLint, TypeScript and the i18n check are clean. Trivy
  finds 0 HIGH/CRITICAL in the api, web and runner images. Migrations pass up/down/up; sqlc and
  orval are deterministic; the OpenAPI ↔ routes test passes. The full Playwright suite passed
  twice in a row.

### Next — Module 12: Dashboard & DORA metrics

Pipeline success rate and duration trends, and deployment frequency, lead time, change failure
rate and time to restore.

## Module 10 — Logs

### Built

**Backend** (`internal/logs`)
- **One search over three sources:**
  - Service lines sent with ingest tokens.
  - Pipeline job and deployment output, copied in by database triggers as chunks are written.
    ANSI colours are removed, red lines are `error`, and attributes name the project with the
    job and run or the deployment and environment. A failed copy never fails the original
    write.
- **Ingest tokens:** `ohl_…`, one per organization and service name, managed by Owners and
  Admins (`logs.manage`).
  - Shown once and stored as a hash; the last use is recorded.
  - Creating and revoking are audited without the secret.
  - The token passes user authentication as a machine token and is rate-limited by its hash.
- **`POST /api/v1/ingest/logs`:** NDJSON, at most 1 MiB and 5,000 lines per request.
  - `message`/`msg`/`log` is required.
  - `ts` accepts RFC 3339 or Unix seconds or milliseconds. It may be at most 7 days old, and
    clocks more than 5 minutes ahead get the server's time.
  - Level aliases are accepted (warning, fatal, trace, …).
  - Other fields and an `attributes` object are kept as attributes (at most 50 keys and 8 KiB).
    Messages are clipped at 8 KiB.
  - Bad lines are skipped and reported by line number (first 20). Accepted lines are stored in
    one statement.
- **Search:**
  - Newest first over up to 31 days; minimum level, source and service filters; keyset
    cursors for older pages and for follow mode.
  - Job and deployment lines only for projects the caller can see.
  - Full text uses PostgreSQL's `simple` configuration with `/ : = .` split, so paths and
    `key=value` are searchable. Queries in Khmer script match as substrings, because Khmer has
    no spaces between words.
- **Storage:** `log_entries` is partitioned by UTC day. An hourly River job runs a third
  `SECURITY DEFINER` function, which creates the partitions for the ingest window and drops
  those older than `OPSHUB_LOG_RETENTION_DAYS` (default 30, 1–365).
- **Delivery:** migration `000010_logs`; 6 endpoints; 3 error codes (EN + KM); OpenAPI 0.11.0.
- **Demo seed:** a "Checkout service" ingest token and 13 recent lines in English and Khmer.

**Frontend**
- **Organization → Logs** (sidebar):
  - **Search:** text search, level, source and service filters, time range (15 min–7 days),
    follow mode that polls every 3 s, and load older lines.
  - Level colours, and each line opens to show its attributes.
- **Ingest tokens** tab for Owners and Admins: create (validated service name), a token dialog
  shown once with a `curl` example, last use, and revoke.
- New `logs` translation namespace (EN + KM).

### Quality

- **Go:**
  - NDJSON parsing: aliases, timestamps in every format, clock skew, clipping without splitting
    Khmer characters, NUL removal, attribute limits, and body and line limits.
  - Tokens: validation, permissions, audit without the secret, revocation, last use.
  - Search: full text, phrases, exclusions, paths, Khmer substrings, literal `%`, level,
    source, service and time filters, paging, follow cursors, other organizations.
  - Triggers: job and deployment lines, levels and attributes, project visibility for
    Developers, and a failed copy keeping the original chunk.
  - Partition maintenance and retention.
  - Tenant isolation over all 6 routes with coverage guards.
  - Coverage: logs 93.4 %; service coverage 78.1 %.
- **Web:** 112 Vitest tests (+7 search params, merging, attributes, curl example, token rules)
  and 50 Playwright tests (+2):
  - A token created in the UI sends lines; bad lines are reported.
  - Search by words, path parts and Khmer; level filter and attributes.
  - Follow mode shows a new line without reloading.
  - Revoking gives 401 and keeps the stored lines.
  - Khmer layout at phone and desktop widths; viewers search but can't open ingest tokens.
- **Checked by hand on the dev stack:** the seeded checkout lines in the Khmer UI, and the
  expanded attributes.
- golangci-lint, gosec, govulncheck, ESLint, TypeScript and the i18n check are clean. Trivy
  finds 0 HIGH/CRITICAL in the api, web and runner images. Migrations pass up/down/up; sqlc and
  orval are deterministic; the OpenAPI ↔ routes test passes.

### Next — Module 11: Audit log

Audit log viewer with filters and a CSV export that keeps Khmer readable in Excel.

## Module 9 — Monitoring & alerts

### Built

**Backend** (`internal/monitor`, `internal/alert`, `internal/notify`)
- **Uptime monitors:**
  - HTTP: expected status codes, keyword, `GET`/`HEAD`, no redirects followed.
  - TCP.
  - SSL: trust, name and expiry days, using the Module 7 prober moved to `internal/tlsprobe`.
  - Checks run every 30 s–1 h from the OpsHub server through the SSRF guard. A 15-second River
    job claims due checks with `SKIP LOCKED` on its own `monitoring` queue.
  - Results are kept in monthly partitions through a second `SECURITY DEFINER` function, plus
    hourly rollups kept 400 days. The API returns uptime and latency series, the latest checks
    and 24 h uptime.
- **Alert rules:**
  - Conditions: monitor down or slow, and from Module 7, server CPU/memory/disk, agent offline,
    and certificates expiring or failing.
  - A rule targets one subject or all of them, optionally only those with a label or tag.
  - Each rule has a "for" duration and a severity.
  - Rules are evaluated every 30 s with one alert per rule and subject (pending → firing →
    resolved). Resolution is automatic.
- **Escalation:**
  - Up to 5 timed steps until someone acknowledges; resolutions go to every channel that was
    notified.
  - Each alert has a timeline: fired, notified, notify_failed, escalated, acknowledged, silenced,
    resolved.
  - Disabling or deleting a rule resolves its alerts and keeps the history.
- **Silences:** rule, subject, label or severity matchers for up to 90 days. Silenced alerts
  still fire and show; they notify when the silence ends.
- **Notification channels:** Telegram, Slack, email (SMTP) and webhooks (optional
  HMAC-SHA256 signature).
  - Credentials are write-only and sealed by the key ring; `keys rotate` covers them.
  - Messages are in EN or KM: the channel's language, else each member recipient's, else the
    default.
  - Test messages are available. Deliveries retry up to 5 times, and every failed attempt is
    on the timeline.
  - A channel that rules use can't be deleted (`CHANNEL_IN_USE`).
- **Errors never reveal credentials or internal addresses.** DNS and dial errors are reduced to
  "host not found", "connection refused" and similar, for monitors, certificates and channels
  alike. This also fixes the Module 7 certificate errors.
- **Delivery:** migration `000009_monitoring`; 22 endpoints; 10 error codes and 6 validation
  rules (EN + KM); OpenAPI 0.10.0.
- **Demo seed:** 3 monitors, one of them unreachable on purpose, so an alert fires a minute
  after seeding and emails the on-call channel (Mailpit). Also 3 rules and an email channel.

**Frontend**
- **Organization → Monitoring** has five tabs:
  - **Monitors:** status, 24 h uptime, response time, filters.
  - **Monitor page:** uptime strip over the whole range, response-time chart from 1 h to 90 d,
    latest checks, pause/resume/edit/delete.
  - **Alerts:** firing first; the alert page has a timeline, Acknowledge and "Silence this".
  - **Alert rules:** condition, target and label, threshold, duration, severity, escalation
    steps with channels.
  - **Silences** and **Channels** (with Send test). Viewers see no channels.
- New `monitoring` translation namespace (EN + KM).
- The Module 7 line chart now takes any scale and unit.

### Quality

- **Go:**
  - Checks against fake HTTP, TCP and TLS servers (status, keyword, redirects, timeouts, SSRF
    block, expiry, wrong name).
  - Due-check claiming, results and rollups, partitions.
  - Rule validation for every kind.
  - Firing after the duration, escalation steps, acknowledge, silences before and after they
    end, infrastructure rules (stale metrics, agents that never reported), disable and delete.
  - Channels against fake Telegram, Slack and webhook servers (signature, languages,
    write-only credentials, errors without URLs), delivery retries and the final give-up.
  - Tenant isolation over all 22 routes with coverage guards.
  - Coverage: monitor 84.0 %, alert 79.4 %, notify 84.4 %; service coverage 77.6 %.
- **Web:** 105 Vitest tests (+11 form mapping) and 48 Playwright tests (+2):
  - A real always-down monitor fires an alert through the real workers.
  - The failing webhook shows on the timeline; acknowledge, silence and "end now".
  - The email test lands in Mailpit.
  - A channel in use can't be deleted.
  - Khmer layout on every page at phone and desktop widths; viewers are read-only.
- **Checked by hand on the dev stack:** checks against example.com, and the seeded unreachable
  database firing "Production down" and emailing both on-call addresses.
- golangci-lint, gosec, govulncheck, ESLint, TypeScript and the i18n check are clean. Trivy
  finds 0 HIGH/CRITICAL in the api, web and runner images. Migrations pass up/down/up
  (including the grant to `opshub_app`); sqlc and orval are deterministic; the OpenAPI ↔ routes
  test passes.

### Next — Module 10: Logs

Log ingest (NDJSON from agents and API tokens), full-text search and a retention job.

## Module 8 — Secrets

### Built

**Backend** (`internal/secret`, `internal/keyrotate`)
- Write-only project secrets, for all environments or one environment. Names are environment
  variable names (`OPSHUB_` reserved). Values up to 64 KiB. At most 500 per project.
- **Endpoints:** 7 (list, create, get, description with If-Match, rotate, history, delete).
  No response contains a value. Every change is audited without the value.
- **Permissions:**
  - Developers manage secrets only for unprotected environments.
  - A secret for all environments also reaches protected ones, so it counts as protected.
  - There is no reveal endpoint for anyone.
- **Envelope encryption:** a random AES-256-GCM data key per version, sealed by the master key
  ring. Both layers are bound to the secret and version.
- **Rotation and deletion:** rotation destroys older values, and deletion destroys all of them.
  The history keeps who changed a secret and when.
- **Pipelines:** a job lists `secrets: [NAME]`; an environment's secret wins over an
  all-environments one.
  - Names are checked when the job becomes ready (`secret_not_found`, and the log names what's
    missing).
  - Pull-request runs never get secrets (`secrets_not_allowed`).
  - Deploy jobs can't list secrets (`deploy_with_secrets`).
- **Runners:** a runner gets the values when it claims the job, inside the claim transaction,
  with one `secret.read` audit row per value.
  - If a secret was deleted meanwhile, the job fails with a notice.
  - The API masks stored log output too, using the job's masks sealed in its token row.
  - Each line of a multi-line value is masked.
- **Runner fix:** a secret now always wins over a variable of the same name. Before, the
  winner depended on sort order.
- **`opshub-api keys rotate`:** re-encrypts every key-ring column (secret data keys, 2FA seeds,
  Git tokens, webhook secrets, deploy credentials, job masks) with the active key. It is
  idempotent, resumable, safe while running, and prints counts per column. After it, an old
  master key can be removed.
- **Delivery:** migration `000008_secrets`; 3 error codes and 4 validation rules (EN + KM);
  OpenAPI 0.9.0. Also fixed two older spec descriptions that YAML had truncated at a comma.
- **Demo seed:** `SENTRY_DSN` (all environments) and `DATABASE_URL` (staging, production).

**Frontend**
- **Project → Secrets tab** (Developers and up):
  - Filter by scope.
  - Add dialog: names normalized as you type; Developers only see unprotected environments;
    hidden or multi-line value.
  - Rotate, description, history (who, when, destroyed, how to use it in `.opshub.yml`) and
    delete.
- New `secret` translation namespace (EN + KM).

### Quality

- **Go:**
  - Envelope and re-wrap crypto (tampering, AAD binding, swapped data keys).
  - Validation, scopes and the per-role permission rules.
  - Rotation and deletion destroy values; audit entries never contain values; the limit.
  - Gate and injection: precedence, pull requests, missing and deleted secrets, unreadable
    values.
  - Server-side masking through the real claim path.
  - Key rotation across pages, idempotency, and rows it can't read.
  - Tenant isolation over all 7 routes with a coverage guard, and the parser rules.
  - Coverage: secret 84.0 %, keyrotate 88.9 %; service coverage 77.0 %.
- **Web:** 94 Vitest tests (+6 secret form) and 46 Playwright tests (+3):
  - Create, rotate, edit and delete, and no value ever shown.
  - Developer restrictions in the Khmer UI, with Khmer layout at phone and desktop widths.
  - Viewers get no tab.
- **Checked by hand:** `keys rotate` on the dev database. It moved 14 values to a new key;
  a second run changed nothing; the new key alone read everything; rotating back worked.
- golangci-lint, gosec, govulncheck, ESLint, TypeScript and the i18n check are clean. Trivy
  finds 0 HIGH/CRITICAL in the api, web and runner images. Migrations pass up/down/up; sqlc
  and orval are deterministic; the OpenAPI ↔ routes test passes.

## Module 7 — Infrastructure

### Built

**Backend** (`internal/infra`)
- Asset inventory per organization: servers, clusters, databases and domains with per-kind
  address validation, tags, metadata and a TLS port for domains. Filters by kind, tag, status
  and text; If-Match edits; audited create/update/delete/token issue.
- Agent tokens (`ohi_…`, servers only, stored hashed; rotation revokes) and
  `POST /agent/heartbeat` (every 30 s): agent version, host, platform and a CPU/memory/disk/load
  sample. Status online/offline/waiting/no agent from the last heartbeat (90 s).
- Metrics in a month-partitioned `asset_metrics` table (server timestamps) plus hourly
  average/peak rollups. The API picks raw or hourly data and a step for ≤ 300 points
  (`step` settable, ≤ 1000). An hourly River job rolls up, creates partitions and applies
  retention: raw for the current and previous month, rollups for 400 days.
- Partition DDL through one `SECURITY DEFINER` function owned by the migration role, so
  `opshub_app` stays DML-only.
- TLS certificate checks of domains by OpsHub, through the SSRF guard: subject, issuer, names,
  validity, fingerprint and stable failure reasons. Checked every 15 min when due (daily, hourly
  after a failure) and on demand. Organization-wide list with an expiring-within filter.
- `opshub-runner agent`: Linux metrics from `/proc` and `statfs`, container mode with
  `--proc`/`--disk`/`--hostname`, token file permission check, stops on a revoked token.
- Migration `000007_infrastructure`; 11 endpoints; 4 error codes (EN + KM); OpenAPI 0.8.0.
  Demo seed: 5 assets, `web-1` with a day of sample metrics, `example.com`.

**Frontend**
- Organization → Infrastructure: asset table with status, usage bars and certificate expiry,
  filters, and an add/edit dialog.
- Asset page: details, agent card (token shown once with binary and Docker commands; rotate),
  CPU/memory/disk charts (1 h–90 d, average and peak, hover readout) and a certificate card
  with Check now.
- Certificates tab with expiry windows.
- New `infra` translation namespace (EN + KM). Fixed a stray scrollbar under the project tabs.

### Quality

- Go:
  - Validation, assets, agent tokens and heartbeats (which don't count as edits), metric
    series from raw and hourly data, partition maintenance and retention.
  - Certificate probes against a test CA: valid, expiring, expired, wrong name, untrusted,
    unreachable.
  - Tenant isolation over all 11 routes with a coverage guard, an HTTP flow, and `/proc`
    parsers for the agent.
  - Infra 85.3 %; service coverage 76.8 %.
- Web: 88 Vitest tests (+6 asset form); 43 Playwright tests (+2). They cover inventory CRUD,
  an agent token with a heartbeat and chart, rotation, a failing certificate check, filters,
  Khmer layout at phone and desktop widths, and viewer read-only.
- Checked by hand: the real agent in a Linux container reporting to the dev stack.
- golangci-lint, gosec, govulncheck, ESLint, TypeScript and the i18n check are clean. Trivy
  finds 0 HIGH/CRITICAL in the api, web and runner images. Migrations pass up/down/up
  (including the grant to `opshub_app`); sqlc and orval are deterministic; the OpenAPI ↔ routes
  test passes.

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
