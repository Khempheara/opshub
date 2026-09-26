# Kubernetes (Helm)

`deploy/helm/opshub` installs:
- **API**, with its background workers;
- **web UI**;
- a **migration** hook that runs before every install and upgrade;
- optionally, the **backup** CronJob, a ServiceMonitor and NetworkPolicies.

Every pod runs as non-root, with a read-only root filesystem, no privilege escalation, all
capabilities dropped, and the `RuntimeDefault` seccomp profile.

## Requirements

- **Kubernetes 1.27+,** an ingress controller and cert-manager (or a TLS secret).
- **PostgreSQL 17, external:** a managed database (RDS, Cloud SQL, Azure) or an operator such as
  [CloudNativePG](https://cloudnative-pg.io). The chart doesn't run PostgreSQL itself, so
  upgrades, failover and backups of the database stay with the tool made for them.
- **The images** (`opshub-api`, `opshub-web`, `opshub-backup`) in a registry your cluster can
  pull from: `make docker VERSION=0.13.0`, then push.

## Database roles

The same least-privilege layout as [database.md](database.md) ("PostgreSQL roles"):

```sql
CREATE ROLE opshub_migrator LOGIN PASSWORD '…';     -- owns the schema, runs migrations
CREATE ROLE opshub_app LOGIN PASSWORD '…';          -- the API: DML only
CREATE ROLE opshub_backup LOGIN PASSWORD '…' IN ROLE pg_read_all_data;
CREATE DATABASE opshub OWNER opshub_migrator;
\connect opshub
GRANT USAGE ON SCHEMA public TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO opshub_app;
ALTER DEFAULT PRIVILEGES FOR ROLE opshub_migrator IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO opshub_app;
```

With CloudNativePG, create the roles with the cluster's `managed.roles`, and use
`sslmode=verify-full` URLs.

## Secrets

Secrets never go into values:

```bash
docker run --rm opshub-api keys generate     # OPSHUB_MASTER_KEYS=… and OPSHUB_JWT_KEYS=…
kubectl -n opshub create secret generic opshub \
  --from-literal=OPSHUB_DATABASE_URL='postgres://opshub_app:…@db:5432/opshub?sslmode=verify-full' \
  --from-literal=OPSHUB_MIGRATE_DATABASE_URL='postgres://opshub_migrator:…@db:5432/opshub?sslmode=verify-full' \
  --from-literal=OPSHUB_MASTER_KEYS='…' --from-literal=OPSHUB_JWT_KEYS='…' \
  --from-literal=OPSHUB_SMTP_PASSWORD='…'
kubectl -n opshub create secret generic opshub-backup \
  --from-literal=OPSHUB_BACKUP_DATABASE_URL='postgres://opshub_backup:…@db:5432/opshub?sslmode=verify-full'
```

Back up `OPSHUB_MASTER_KEYS` offline as well ([backup.md](backup.md)). Use External Secrets or
Sealed Secrets if you keep manifests in Git.

## Install

```yaml
# values-prod.yaml
image: { registry: ghcr.io/your-org, tag: "0.13.0" }
publicURL: https://opshub.example.com
ingress:
  className: nginx
  host: opshub.example.com
  annotations: { cert-manager.io/cluster-issuer: letsencrypt }
api:
  trustedProxies: "10.0.0.0/8"          # the ingress controller's pod network
config:
  OPSHUB_SMTP_HOST: smtp.example.com
  OPSHUB_SMTP_PORT: "587"
  OPSHUB_BOOTSTRAP_ADMIN_EMAIL: admin@example.com
backup:
  enabled: true
  ageRecipients: "age1…"
  rcloneRemote: "s3:my-bucket/opshub"   # plus RCLONE_CONFIG_S3_* in the opshub-backup secret
metrics: { serviceMonitor: { enabled: true } }
networkPolicy:
  enabled: true
  ingressNamespaceSelector: { kubernetes.io/metadata.name: ingress-nginx }
  metricsNamespaceSelector: { kubernetes.io/metadata.name: monitoring }
```

```bash
helm upgrade --install opshub deploy/helm/opshub -n opshub -f values-prod.yaml
```

- **Upgrades:** each install and upgrade runs `opshub-api migrate up` as a hook Job before the
  new pods start. The API is started with `OPSHUB_MIGRATE_ON_START=false`.
- **Traffic:** the Ingress sends `/api` and `/docs` straight to the API, so audit logs see the
  client's address through the controller's `X-Forwarded-For`, and everything else to the web
  UI. `/metrics` is only reachable inside the cluster.
- **Volumes:**
  - Artifacts and caches live on the API's volume (`api.persistence`). It is kept on
    `helm uninstall`.
  - With `api.replicas` > 1 that volume must be `ReadWriteMany` (the chart refuses otherwise).
    With `ReadWriteOnce` the API updates with `Recreate`.
- **`helm --wait` and backups:** storage classes that bind on first use (`WaitForFirstConsumer`,
  common on cloud providers) leave the backup volume Pending until the first backup runs, so
  `--wait` waits for it. Either:
  - install without `--wait`; or
  - start the first backup right away:
    `kubectl -n opshub create job first-backup --from=cronjob/opshub-backup`.
- **Runners** aren't part of the chart. Run them where jobs should run ([runners.md](runners.md)).

## Values

| Key | Default | |
|---|---|---|
| `image.registry` / `image.tag` | `""` / appVersion | Image location |
| `publicURL` | `https://opshub.example.com` | Links and emails |
| `existingSecret` | `opshub` | Secret with the database URLs and keys |
| `config` | `{}` | Other `OPSHUB_*` settings (non-secret) |
| `api.replicas` | `1` | > 1 needs `ReadWriteMany` |
| `api.trustedProxies` | `""` | Ingress controller CIDRs |
| `api.persistence.*` | 20Gi RWO | Artifacts, caches |
| `web.replicas` | `2` | |
| `migrations.enabled` | `true` | Pre-install/upgrade migration Job |
| `ingress.*` | enabled, TLS | |
| `metrics.serviceMonitor.enabled` | `false` | Prometheus Operator |
| `networkPolicy.enabled` | `false` | Only web and the ingress controller reach the API |
| `backup.enabled` | `false` | CronJob; needs `backup.ageRecipients` |
| `backup.schedule` / `timeZone` | `0 2 * * *` / UTC | |
| `backup.retention` | 7 / 4 / 3 | Daily / weekly / monthly |
| `backup.rcloneRemote` | `""` | Off-site copy |

## Checking the chart

`make helm-lint` runs three checks (CI runs it too):
- `helm lint --strict`;
- validates the manifests, with every option on, against the Kubernetes 1.30 schemas
  (kubeconform);
- shellchecks the backup scripts.

The chart was also installed into a kind cluster during development:
- the migration hook ran;
- the API became ready and the web UI proxied to it;
- a backup job completed on the read-only filesystem;
- an upgrade ran the migration hook again.
