# Logs

**Organization → Logs** searches three kinds of lines in one place:

| Source | Where lines come from | Service name |
|---|---|---|
| Services | Your applications send them with an ingest token | The token's service, e.g. `shop/api` |
| Pipeline jobs | Copied in as a job writes its log | `<project>/<job>`, e.g. `payments-api/build` |
| Deployments | Copied in as a deployment writes its log | `<project>/deploy` |

| | Owner | Admin | Developer | Viewer |
|---|:-:|:-:|:-:|:-:|
| Search service lines (`logs.view`) | ✅ | ✅ | ✅ | ✅ |
| Search job and deployment lines | ✅ all projects | ✅ all projects | projects granted to them or their team | ✅ all projects |
| Create and revoke ingest tokens (`logs.manage`) | ✅ | ✅ | — | — |

Job and deployment lines follow the project rules of [rbac.md](rbac.md): Owners, Admins and
Viewers see every project, and Developers see the projects granted to them or their teams.
Creating and revoking tokens is audited (`log_token.create`, `log_token.revoke`). The secret
is never stored or audited, only its hash and prefix.

## Sending lines from a service

1. An Admin opens **Logs → Ingest tokens → New ingest token** and names the service
   (lowercase letters, digits and `. _ - /`, for example `shop/api`). A token sends lines for
   that service only.
2. The token (`ohl_…`) is shown once, with a `curl` command that sends a test line.
3. The service sends newline-delimited JSON:

```bash
curl -X POST https://opshub.example.com/api/v1/ingest/logs \
  -H "Authorization: Bearer ohl_…" \
  -H "Content-Type: application/x-ndjson" \
  --data-binary @- <<'EOF'
{"ts":"2026-09-26T06:12:01Z","level":"error","message":"payment gateway timeout","order":10233}
{"level":"info","msg":"order 10234 paid","attributes":{"currency":"KHR"}}
EOF
```

| Field | Accepted | If missing or unusable |
|---|---|---|
| `message` (or `msg`, `log`) | Any text; a number or object is kept as its JSON | Line rejected (`message_required`) |
| `ts` (or `timestamp`, `time`, `@timestamp`) | RFC 3339, or Unix seconds or milliseconds | Server time. Unparseable: rejected (`ts_format`). More than 7 days old: rejected (`too_old`). More than 5 minutes ahead: server time |
| `level` (or `severity`, `lvl`) | `debug`, `info`, `warn`, `error`; also `trace`, `warning`, `notice`, `err`, `fatal`, `critical`, `panic`, … | `info` |
| Anything else, and the fields of an `attributes` object | Kept as attributes (top-level fields win) | At most 50 keys and 8 KiB, else rejected (`attributes_max`) |

**Limits:**
- 1 MiB and 5,000 lines per request (`413 PAYLOAD_TOO_LARGE` above that).
- Messages longer than 8 KiB are clipped with "…".
- Lines that aren't JSON objects are rejected (`json_object`).

The answer lists what was stored, and the first 20 problems with 1-based line numbers:

```json
{ "accepted": 2, "rejected": 1, "errors": [{ "line": 3, "rule": "json_object" }] }
```

A revoked or unknown token gets `401 INGEST_TOKEN_INVALID`. Requests are rate-limited per
token like every other API call.

## Pipeline and deployment output

Each chunk a runner or deployment writes is split into lines, and colours are removed. Lines
printed in red count as `error`, and everything else as `info`. Attributes name the project and
the job and run number, or the deployment number and environment. The copy is made by
database triggers when a chunk is written. If copying fails, the job's own log is still
stored, and the failure is logged as a PostgreSQL warning.

## Searching

- **Time range:** the last 15 minutes to the last 7 days. The API allows any range up to
  31 days (`from`, `to`).
- **Text:**
  - Words, `"a phrase"` and `-excluded` words (PostgreSQL `websearch_to_tsquery` with the
    language-neutral `simple` configuration, which never stems).
  - Paths, URLs, hosts and `key=value` are split at `/ : = .`, so `cart` finds
    `GET /api/cart 200`.
  - Khmer is written without spaces between words, so a query containing Khmer script matches
    as a substring of the message instead: `បរាជ័យ` finds `ការទូទាត់បរាជ័យ`.
- **Filters:** minimum level, source and service. The service list shows names seen in the
  last day.
- **Paging:** newest first, 200 lines per page. **Load older lines** continues from the last
  line shown (keyset cursor on time and id).
- **Follow:** asks for lines newer than the newest one every 3 seconds. At most 2,000 lines
  are kept on the page, and dropping older ones ends paging back until you refresh.
- **Line details:** click a line to show its source, exact timestamp and attributes.

A line that arrives with a timestamp older than the newest line already shown doesn't appear
while following. Refresh to see it.

## Retention and storage

`log_entries` is partitioned by UTC day. An hourly job:
- creates the partitions for the ingest window (7 days back) and 2 days ahead;
- drops partitions older than `OPSHUB_LOG_RETENTION_DAYS` (default 30, 1–365).

Partitions are managed by `opshub_maintain_log_partitions`, a `SECURITY DEFINER` function, so
the application role stays DML-only ([database.md](database.md)). Deleting an organization
deletes its lines.

## Not yet

- The infra agent doesn't tail log files. Ship files with any tool that can POST NDJSON, such
  as Vector, Fluent Bit or a small script.
- There is no live stream over SSE (follow mode polls) and no alerting on log patterns.
