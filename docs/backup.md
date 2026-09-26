# Backups and restore

OpsHub keeps everything that matters in PostgreSQL. That covers users, projects, runs,
deployments, secrets (encrypted), audit log and logs. What PostgreSQL doesn't hold:

| What | Where | Back it up |
|---|---|---|
| Database | PostgreSQL | Nightly with the backup job below |
| Master keys (`OPSHUB_MASTER_KEYS`) | Your secret store / `.env` | **Separately, offline.** Without them, restored secrets, 2FA seeds, Git tokens and deploy credentials can't be decrypted |
| Artifacts, caches, source archives | `OPSHUB_BLOB_DIR` volume | Optional: they are rebuilt by the next pipeline runs |
| Age private key (below) | Offline (password manager, safe) | Keep two copies |

## How backups work

The backup image (`deploy/docker/backup.Dockerfile`, PostgreSQL 17 client tools) runs
`opshub-backup` (`deploy/backup/backup.sh`):

1. `pg_dump --format=custom` connects as **`opshub_backup`**. It is a read-only role
   (`pg_read_all_data`), so a backup can never change data.
2. The dump is streamed through **[age](https://age-encryption.org)** to one or more **public**
   keys. The private key never has to be on the server, and a stolen backup is useless without
   it.
3. The file is written under a temporary name, renamed when complete, and gets a `.sha256`
   checksum beside it.
4. **Retention:** 7 daily, 4 weekly (Sundays) and 3 monthly (the 1st) backups are kept.
   Weekly and monthly copies are hard links, so they take no extra space.
5. **Off-site copy (optional):** `rclone sync` mirrors the backup directory to S3-compatible
   storage (AWS S3, Cloudflare R2, MinIO, Backblaze B2 …), keeping the same set of files.
6. **`last-success`** records the time of the last good backup. Alert when it is older than a
   day.

A failed step exits non-zero and leaves no partial file.

### Set up

Create a key pair on **your** machine (not the server) and keep `opshub-backup.key` offline:

```bash
docker run --rm opshub-backup age-keygen -o /dev/stdout > opshub-backup.key
grep 'public key' opshub-backup.key      # age1… — this goes into OPSHUB_BACKUP_AGE_RECIPIENTS
```

Several recipients (space-separated) let any one of several people restore.

**Docker Compose:** add to `.env`, then start the opt-in service. It backs up every day at
`OPSHUB_BACKUP_AT` (UTC) into the `backups` volume.

```bash
OPSHUB_BACKUP_AGE_RECIPIENTS=age1…
OPSHUB_BACKUP_AT=02:00
# Off-site (optional): an rclone remote named "s3"
OPSHUB_BACKUP_RCLONE_REMOTE=s3:my-bucket/opshub
RCLONE_CONFIG_S3_TYPE=s3
RCLONE_CONFIG_S3_PROVIDER=AWS
RCLONE_CONFIG_S3_ENV_AUTH=true
```

```bash
docker compose --profile backup up -d backup
docker compose --profile backup run --rm backup opshub-backup   # one backup now
```

The `opshub_backup` role is created when the database volume is first initialized
(`deploy/compose/postgres/init-roles.sql`). For an older volume, create it once:

```sql
CREATE ROLE opshub_backup LOGIN PASSWORD '…' IN ROLE pg_read_all_data;
GRANT CONNECT ON DATABASE opshub TO opshub_backup;
```

**Kubernetes:** set `backup.enabled=true` and `backup.ageRecipients` in the Helm chart
([helm.md](helm.md)). A CronJob then writes to its own volume.

## Restore

Restore into an **empty** database, as a role that can create the schema (the migration owner).
The script checks the checksum, decrypts, runs `pg_restore`, and prints the schema version and
row counts:

```bash
# 1. An empty database (or a new server)
psql "$ADMIN_URL" -c "CREATE DATABASE opshub OWNER opshub_migrator"

# 2. Restore (the private key is mounted only for this step)
docker run --rm -v /path/to/backups:/backups:ro -v "$PWD/opshub-backup.key:/key:ro" \
  -e OPSHUB_RESTORE_DATABASE_URL='postgres://opshub_migrator:…@db:5432/opshub' \
  -e OPSHUB_BACKUP_AGE_IDENTITY=/key \
  opshub-backup opshub-restore /backups/daily/opshub-20260926T020000Z.dump.age

# 3. Start OpsHub with the SAME OPSHUB_MASTER_KEYS, and the same (or a newer) version
opshub-api migrate status
```

- **Refused:** a non-empty target unless `OPSHUB_RESTORE_FORCE=1` (existing tables are dropped
  first).
- **Point-in-time recovery:** for a recovery point under 24 hours, add WAL archiving (for
  example pgBackRest or a managed PostgreSQL). These dumps are the portable, provider-neutral
  layer.

## Restore drill (every quarter)

A backup you haven't restored is a hope, not a backup. Every quarter:

1. **Automated check:** `make backup-drill` (CI runs it on every change).
   - It backs up the running stack with a throwaway key and restores into a scratch database.
   - It compares the row counts of the core tables, which must be identical, and cleans up.
2. **Real backup:** restore last night's production backup into a scratch database or server,
   using the real private key from its offline storage. This proves the key is still where you
   think it is.
3. **Point OpsHub at it:** a test instance with the production master keys. Sign in, then:
   - open a project's secrets: the names show, and a pipeline job receives the values;
   - check the audit log and the dashboard.
4. **Write down:** how long the restore took (your recovery time) and the backup's age (your
   recovery point). Fix anything that surprised you.
5. **Delete** the scratch database.

| Date | Backup restored | Duration | Issues | By |
|---|---|---|---|---|
| | | | | |
