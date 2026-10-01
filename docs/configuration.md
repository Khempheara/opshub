# Configuration and running in production

The [Docker install](install-docker.md) sets all of this up for you. This page is for running
OpsHub some other way, or for changing what the installer chose.

## Settings

Everything is configured with environment variables; [.env.example](../.env.example) documents each
one. The essentials:

| Variable | Purpose |
|---|---|
| `OPSHUB_PUBLIC_URL` | URL users open (email links, SSO callbacks, CSRF origin check). Must be https in production. |
| `OPSHUB_DATABASE_URL` / `OPSHUB_MIGRATE_DATABASE_URL` | App (DML-only) and schema-owner connections |
| `OPSHUB_MASTER_KEYS` | AES-256 key ring for data at rest (secrets, 2FA seeds, Git tokens, deploy credentials). Back it up separately from the database. |
| `OPSHUB_JWT_KEYS` | Ed25519 key ring for access tokens |
| `OPSHUB_ALLOW_SIGNUP`, `OPSHUB_BOOTSTRAP_ADMIN_EMAIL` | Who can register; the bootstrap address becomes platform admin |
| `OPSHUB_SMTP_*` | Outgoing email |
| `OPSHUB_SSO_*` | GitHub, Google and Keycloak sign-in (enabled when the client ID is set) |
| `OPSHUB_TRUSTED_PROXIES` | CIDRs of your reverse proxy/ingress, so audit logs record real client IPs |
| `OPSHUB_OUTBOUND_ALLOWED_CIDRS` | Internal networks OpsHub may call (e.g. a self-hosted GitLab on `10.0.0.0/8`); everything private is blocked otherwise |
| `OPSHUB_CORS_ALLOWED_ORIGINS` | Extra browser origins (empty = same-origin, recommended) |

Generate keys with `opshub-api keys generate` (or `go run ./cmd/api keys generate` in the source). To rotate, put the new key first and keep the
old one after it (`id2:…,id1:…`), run `opshub-api keys rotate`, then remove the old key
([details](secrets.md#rotating-the-master-key)).

## Production notes

- Serve over https and set `OPSHUB_PUBLIC_URL` accordingly (cookies become `Secure`).
- Run `opshub-api migrate up` as a deploy step with the migrator credentials and set
  `OPSHUB_MIGRATE_ON_START=false` for the API.
- Create the roles as in `deploy/compose/postgres/init-roles.sql` (with real passwords); the
  [Docker install](install-docker.md) does this for you, with HTTPS, SMTP settings and
  backups.
- Set `OPSHUB_TRUSTED_PROXIES` to your ingress range, and configure real SMTP with TLS.
- Keep `/metrics` on an internal network (the bundled nginx doesn't expose it); metrics,
  dashboards and suggested alerts are in [observability.md](observability.md).
- Back up the database nightly and practise restores ([backup.md](backup.md)); keep
  `OPSHUB_MASTER_KEYS` backed up offline too.
- On Kubernetes, use the Helm chart in `deploy/helm/opshub` ([helm.md](helm.md)).
- Load-test changes with `make load` ([load-testing.md](load-testing.md)).
- Put `OPSHUB_BLOB_DIR` (artifacts and caches) on persistent storage writable by the API user
  and include it in backups if artifacts matter to you.
- Run runners on machines dedicated to CI: the agent controls that machine's Docker.
- Git webhooks are delivered to `OPSHUB_PUBLIC_URL/api/v1/webhooks/…`, so the Git host must be
  able to reach it. If OpsHub can't install a webhook itself, the Repository tab shows the URL and
  secret to add by hand.

## Architecture in brief

- **One API binary** (`cmd/api`): REST under `/api/v1`, background jobs (River, in PostgreSQL),
  `/healthz`, `/readyz`, `/metrics`, `/docs`, and subcommands `migrate`, `seed`, `keys`.
- **Layers:** handler → service (business rules, authorization, audit) → repository (sqlc).
- **Errors are codes, not sentences:** `{"error":{"code","message","details"}}`; the UI translates
  the code into English or Khmer.
- **PostgreSQL is the only stateful service** (data, job queue, SSE fan-out); pipeline artifacts
  and caches live on a volume (`OPSHUB_BLOB_DIR`). The API connects as a DML-only role; the
  audit log is insert-only.

Details: [architecture](architecture.md) · [API endpoints](api.md) ·
[database](database.md) · [RBAC](rbac.md) · [i18n & glossary](i18n.md) ·
[observability](observability.md)
