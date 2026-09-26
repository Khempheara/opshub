# Audit log

OpsHub records every change and every security event in `audit_log`, in the same database
transaction as the change it describes. A change is never saved without its entry. The table is
append-only:
- triggers refuse `UPDATE`, `DELETE` and `TRUNCATE`;
- the application role (`opshub_app`) can only `INSERT` and `SELECT` it.

Entries are kept forever. Archive them with your database backups.

## Who sees what

| | Owner | Admin | Developer | Viewer |
|---|:-:|:-:|:-:|:-:|
| Read the organization's audit log (`audit.view`) | ✅ | ✅ | — | — |
| Export it as CSV (`audit.export`) | ✅ | ✅ | — | — |
| See their own account activity | ✅ | ✅ | ✅ | ✅ |

- **Organization → Audit log** shows everything that happened in the organization: members,
  teams, projects, pipelines, runners, deployments, infrastructure, secrets, monitoring, logs and
  exports of the audit log itself.
- **Settings → Security → Recent account activity** shows each person their own account events:
  - sign-ins and failed sign-ins, lockouts and sign-outs;
  - two-factor changes and recovery codes;
  - password changes and resets;
  - personal API tokens, signed-out sessions and linked sign-ins.

  These entries belong to the person, not to an organization, so they never appear in an
  organization's log.

## Reading entries

Each entry reads as a sentence in the viewer's language (English or Khmer). For example:
- "Dara Kim rotated the secret DB_PASSWORD in Payments API (version 2)"
- "Dara Kim បានប្តូរតម្លៃ Secret DB_PASSWORD ក្នុង Payments API (កំណែ 2)"

Below the sentence are the time, the IP address and the raw action code (`secret.rotate`). Click an
entry for its details:
- **Who** did it: a person, a person's API token, a runner, or OpsHub itself (for example a
  deployment finishing).
- The **resource**, its **project**, the **device** (from the User-Agent), and the exact time.
- **Changes:** the values before and after, key by key. A created resource shows only after-values,
  and a deleted one only before-values.
- **More details:** other recorded values (for example the filter of an export).

Names are resolved when you read the log: a renamed project shows its current name. Entries of
deleted people read "A deleted user", and the raw values keep what was recorded.

Secrets, tokens, passwords and credentials are never recorded, only names, prefixes and versions.

## Filters

- **Area:** organization & members, projects, pipelines, runners, deployments, infrastructure,
  secrets, monitoring, logs, or the audit log itself.
- **Person:** a member of the organization.
- **Project**
- **Time range:** 24 hours to 90 days, or all time.

Pages of 50 entries, newest first, with **Load older entries**.

The API also filters by exact action (`action=secret.read`), by resource (`resource_type`,
`resource_id`) and by any `from`/`to` range. Areas and actions can be repeated or
comma-separated. See [api.md §11](api.md).

## CSV export

**Export CSV** downloads every entry that matches the current filters:
- The file is `audit-log-<org>-<yyyymmdd>.csv`, UTF-8 with a byte-order mark, so Excel and
  LibreOffice show Khmer correctly.
- Columns:

  | Column | Content |
  |---|---|
  | `time` | RFC 3339, UTC |
  | `actor_type`, `actor_name`, `actor_email` | Who |
  | `action` | The raw code |
  | `summary` | The sentence, in the language of the request |
  | `resource_type`, `resource_id` | The resource |
  | `project_id`, `project_name` | The project |
  | `ip`, `user_agent` | Where from |
  | `before`, `after`, `metadata` | JSON |

- Cells that start with `=`, `+`, `-`, `@`, a tab or a carriage return get a leading apostrophe,
  so a spreadsheet never runs them as formulas.
- The export is recorded (`audit.export`, with its filter) **before** the file is sent, so there
  is no unaudited export. The file is streamed in batches, so large logs don't use much memory.

## Adding audited actions

Record new actions with `audit.Record` inside the change's transaction, named
`<resource>.<verb>` (see [rbac.md](rbac.md)). Then add their sentence to
`internal/i18n/locales/en.json` and `km.json` as `audit.<action>`. The sentences are Go
templates:
- `{{.Actor}}`, `{{.Target}}` (the resource's name) and `{{.Project}}` are always available.
- Some actions get more, such as `{{.Role}}`, `{{.Version}}` or `{{.Number}}`; see
  `internal/auditlog/describe.go`.

Two tests keep the sentences complete:
- `TestEveryRecordedActionHasASentence` scans the services for recorded actions and fails when
  one has no sentence.
- `TestEverySentenceRenders` renders every sentence in both languages.

An action without a sentence still shows, as `<who>: <action> (<resource>)`.
