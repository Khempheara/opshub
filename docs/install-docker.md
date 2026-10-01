# Install OpsHub with Docker

Two ways, both with Docker only:

| | One container (`docker run`) | Full stack (`./opshub install`) |
|---|---|---|
| Good for | Trying OpsHub, small teams, one server | Bigger teams and production you want to watch closely |
| What runs | Database, API, web app, HTTPS and backups in one container | Each part in its own container, plus Prometheus and Grafana |
| Install | One command | A script that asks a few questions |
| Upgrade | `docker pull`, then run the container again | `./opshub upgrade` (backs up first) |

For Kubernetes, use the [Helm chart](helm.md); for working on the code, `make dev` in the
repository.

## One container: `docker run`

### Try it

```bash
docker run -d \
  --name opshub \
  --restart unless-stopped \
  -p 8080:8080 \
  -v opshub-data:/data \
  ghcr.io/khempheara/opshub:latest
```

Open http://localhost:8080 and create an account. Without an email server, confirm the address
on the command line: `docker exec opshub opshub verify-email you@example.com`.

This is **try-out mode** (plain HTTP, OpsHub's development settings). With
`-e OPSHUB_DEMO=true` it also loads the demo organizations; their password is in
`docker logs opshub`. For real use, give it a domain:

### On a server, with HTTPS

```bash
docker run -d \
  --name opshub \
  --restart unless-stopped \
  -p 80:8080 \
  -p 443:8443 \
  -e OPSHUB_DOMAIN=ops.example.com \
  -e OPSHUB_ADMIN_EMAIL=you@example.com \
  -v opshub-data:/data \
  ghcr.io/khempheara/opshub:latest
```

- The domain must point at the server, and ports 80 and 443 must be reachable: Caddy gets a
  Let's Encrypt certificate for it.
- `OPSHUB_ADMIN_EMAIL` may always register and becomes platform admin; sign-up is closed to
  everyone else (they join by invitation).
- Behind your own HTTPS proxy instead, leave out the domain and the 80/443 ports and set
  `-e OPSHUB_PUBLIC_URL=https://ops.example.com`; point the proxy at port 8080.

### Settings

Add any of these with `-e NAME=value` (the full list of OpsHub settings is in
[configuration.md](configuration.md)):

| Setting | Meaning |
|---|---|
| `OPSHUB_DOMAIN` | Your server's DNS name: turns on HTTPS and production mode |
| `OPSHUB_ADMIN_EMAIL` | The first administrator (also the Let's Encrypt contact) |
| `OPSHUB_PUBLIC_URL` | The address people open, if it differs (a proxy, another port) |
| `OPSHUB_TLS=internal` | Caddy's own certificate authority instead of Let's Encrypt (LAN, tests) |
| `OPSHUB_ALLOW_SIGNUP=true` | Let anyone register |
| `OPSHUB_SMTP_HOST`, `_PORT`, `_USERNAME`, `_PASSWORD`, `_FROM`, `_TLS` | Email (verification, invitations, alerts) |
| `OPSHUB_DEFAULT_LOCALE=km` | Khmer by default for new accounts and emails |
| `OPSHUB_BACKUP_AGE_RECIPIENTS` | Your own [age](https://age-encryption.org) public key for backups |
| `OPSHUB_BACKUP_AT` | Backup time, UTC (default `02:00`) |
| `OPSHUB_SSO_GITHUB_CLIENT_ID`, … | Single sign-on ([configuration.md](configuration.md)) |
| `OPSHUB_DEMO=true` | Demo data (try-out mode only) |

To change a setting, remove the container and run it again with the new value; the data stays
in the volume.

### Everyday commands

| Task | Command |
|---|---|
| Logs | `docker logs -f opshub` |
| Health and the latest backups | `docker exec opshub opshub status` |
| Back up now | `docker exec opshub opshub backup` |
| Confirm an email address | `docker exec opshub opshub verify-email you@example.com` |
| Upgrade | `docker pull ghcr.io/khempheara/opshub:latest && docker rm -f opshub`, then the same `docker run` |
| Stop / start | `docker stop opshub` / `docker start opshub` |

### What's inside, and where the data is

One unprivileged user (uid 70) runs PostgreSQL 17, the API, nginx (the web app), Caddy (only
with a domain) and the nightly backup; if any of them stops, the container stops and Docker
restarts it. Everything is in the `/data` volume:

| Path | What |
|---|---|
| `/data/postgres` | The database (reachable only inside the container, with passwords) |
| `/data/secrets.env` | Database passwords and encryption keys, generated on the first start. **Keep a copy offline**: backups can't be decrypted into a working OpsHub without these keys |
| `/data/backups` | Nightly encrypted backups (7 daily, 4 weekly, 3 monthly) |
| `/data/backup-private-key.txt` | The backup key, if you didn't give your own: copy it off the server, then delete it |
| `/data/blobs`, `/data/caddy` | Pipeline artifacts and caches; HTTPS certificates |

Copy backups off the server too, e.g. `docker cp opshub:/data/backups ./opshub-backups`.

### Restore a backup

Put the backup and its private key into `/data/restore`, then restart:

```bash
docker cp opshub-20261001T020000Z.dump.age opshub:/data/restore/
docker cp backup-private-key.txt opshub:/data/restore/key.txt
docker restart opshub
```

OpsHub restores before it starts, replacing the current data, then deletes the key from
`/data/restore`.

**On a new server**, the encryption keys must be the old server's. Take `OPSHUB_MASTER_KEYS` and
`OPSHUB_JWT_KEYS` from the old `/data/secrets.env`, add them to the first `docker run` on the new
server (`-e OPSHUB_MASTER_KEYS=… -e OPSHUB_JWT_KEYS=…`; after that they're kept in the volume and
can be left out), then restore as above.

### Add a runner

Pipelines run on runners. In OpsHub, open **Runners → Register runner** and copy the token. On
the machine that should run the jobs (ideally not the OpsHub server: jobs control its Docker):

```bash
docker run -d \
  --name opshub-runner \
  --restart unless-stopped \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v opshub-runner:/var/lib/opshub-runner \
  -e OPSHUB_URL=https://ops.example.com \
  -e OPSHUB_REGISTRATION_TOKEN=<token> \
  -e OPSHUB_RUNNER_NAME=build-1 \
  ghcr.io/khempheara/opshub-runner:latest
```

More in the [runner guide](runners.md).

## Install the full stack

The `./opshub` script runs each part in its own container (PostgreSQL, API, web, Caddy, backups,
Prometheus and Grafana, an optional runner), and manages upgrades, backups and restores for you.

### What you need

- A Linux server with **2 CPUs, 4 GB RAM and 40 GB disk** to start (more disk if you keep many
  logs, artifacts or backups). x86-64 or ARM64.
- **Docker Engine 24+** with the **Compose plugin 2.24+** (`docker compose version`). Install:
  <https://docs.docker.com/engine/install/>.
- For Let's Encrypt: a **DNS name** (e.g. `ops.example.com`) pointing at the server, and ports
  **80 and 443** open to the internet. Without public DNS (a LAN, a test), use `--tls internal`;
  behind your own proxy or load balancer, `--tls off`.
- An **email server** (SMTP) for verification links, invitations, password resets and alerts.
  You can add it later; see [First sign-in](#first-sign-in).

### Install

#### From a release (no source code on the server)

Each release has an install bundle that pulls ready-made images from `ghcr.io`:

```bash
curl -LO https://github.com/khempheara/opshub/releases/download/v1.0.0/opshub-install-v1.0.0.tar.gz
tar xzf opshub-install-v1.0.0.tar.gz && cd opshub-install-v1.0.0
./opshub install
```

While the repository is private, the server must sign in to the registry first, with a GitHub
token that has `read:packages`: `docker login ghcr.io -u <github user>`. (Alternatively,
make the packages public on GitHub.)

#### From the source

```bash
git clone https://github.com/khempheara/opshub.git && cd opshub/deploy/install
./opshub install
```

This builds the images on the server (a few minutes the first time) instead of pulling them.

#### The questions

`./opshub install` asks for what it needs; every answer can also be given as an option, so an
install can run unattended (`./opshub install --help`):

```bash
./opshub install --yes --domain ops.example.com --admin-email you@example.com \
  --smtp-host smtp.example.com --smtp-user ops@example.com --smtp-from "OpsHub <ops@example.com>"
# The SMTP password comes from OPSHUB_SMTP_PASSWORD (kept out of the shell history) or is asked.
```

| Question | Default | Meaning |
|---|---|---|
| HTTPS | `letsencrypt` | `internal`: Caddy's own certificate authority (browsers warn until you trust it). `off`: no Caddy; your proxy serves `--public-url` and forwards to `127.0.0.1:8080` |
| Domain | — | The server's DNS name |
| Administrator email | — | This address may always register and becomes **platform admin** |
| Sign-up | no | With `no`, others join only when invited to an organization |
| Language | en | `en` or `km`: the default for new accounts and emails |
| Email server | — | Leave empty to set it up later |
| Backup key | generate | An [age](https://age-encryption.org) public key (`age1…`) that encrypts backups, or `generate` |

The script then:

1. writes the settings to `.env` next to it (mode `600`): random database and Grafana
   passwords, and new encryption and token-signing keys;
2. builds or pulls the images, starts the stack and waits until every part is healthy;
3. prints the address and the next steps.

Running `install` again never overwrites an existing `.env`.

> **Keep a copy of `.env` somewhere safe.** `OPSHUB_MASTER_KEYS` in it decrypts the secrets,
> deploy credentials and 2FA seeds stored in the database: a backup is useless without it.

### What runs

| Service | Reachable at | Notes |
|---|---|---|
| caddy | ports 80 and 443 | HTTPS, HTTP→HTTPS redirect, HSTS. `/metrics` isn't served |
| web | `127.0.0.1:8080` | The UI; it forwards `/api` to the API |
| api | internal only | API and background workers; runs database migrations when it starts |
| postgres | internal only | Data in the `pgdata` volume |
| backup | — | Nightly encrypted dump into the `backups` volume |
| prometheus, grafana | `127.0.0.1:9090`, `127.0.0.1:3001` | Leave out with `--no-monitoring` |
| runner | — | Only after `./opshub runner` |

Every container runs with `no-new-privileges`; all but PostgreSQL and the runner also with a
read-only file system and no Linux capabilities (Caddy keeps one: binding ports 80 and 443). The API connects to the database as a role
that can only read and write rows (see [database roles](database.md)). Logs are rotated (5 ×
20 MB per container).

### First sign-in

1. Open `https://<your domain>/register` and create the account with the administrator email.
2. Confirm the address with the link in the email. **No email set up yet?** Confirm it on the
   server instead:

   ```bash
   ./opshub verify-email you@example.com
   ```

   (This is recorded in the audit log as done by OpsHub.)
3. Create your organization, invite your team, and connect a repository.

To set up or change email later, edit the `OPSHUB_SMTP_*` lines in `.env` and run
`./opshub restart`. Any other setting works the same way (see `.env.example` in the
repository for all of them, e.g. single sign-on).

### A runner on the same server

Pipelines need a runner. In OpsHub open **Runners → Register runner**, copy the token, then:

```bash
./opshub runner <registration token>
```

The runner starts each job as a container on this server's Docker. Access to the Docker socket
is equivalent to root on the server, so only run pipelines you trust here; for anything else,
put runners on separate machines ([runner guide](runners.md)).

### Monitoring

Prometheus and Grafana listen only on the server itself. From your computer:

```bash
ssh -L 3001:127.0.0.1:3001 you@ops.example.com
# then open http://localhost:3001 (user admin, password: OPSHUB_GRAFANA_PASSWORD in .env)
```

The dashboards and suggested alerts are described in [observability.md](observability.md).

### Backups

The `backup` service dumps the database every night at 02:00 UTC (`OPSHUB_BACKUP_AT`), encrypts
it for the backup key, and keeps 7 daily, 4 weekly and 3 monthly copies in the `backups` volume
([how it works](backup.md)).

- If the installer generated the key, it wrote the **private** key to `backup-private-key.txt`.
  Move it off the server (a password manager or a safe), then delete it there: the server only
  needs the public key, and an attacker on the server then can't read old backups.
- `./opshub backup` takes a backup now.
- Copy backups off the server too: set `OPSHUB_BACKUP_RCLONE_REMOTE` and the rclone settings
  for S3-compatible storage ([backup.md](backup.md#set-up)).

#### Restore

```bash
docker compose cp backup:/backups/daily/opshub-20261001T020000Z.dump.age .   # or a copy from elsewhere
./opshub restore opshub-20261001T020000Z.dump.age /path/to/backup-private-key.txt --force
```

`restore` stops OpsHub, replaces the database with the backup (`--force` is needed when the
database already has data) and starts OpsHub again.

On a **new server**, use the old server's settings so the encryption keys match: unpack the
install bundle (or clone the source), copy the old `.env` into the install folder, run
`./opshub restart` (not `install`), then `./opshub restore … --force` (OpsHub has created its
empty tables by then).

Practise a restore every quarter ([restore drill](backup.md#restore-drill-every-quarter)).

### Upgrade

```bash
./opshub upgrade v1.1.0        # release installs: the version to move to
git pull && ./opshub upgrade   # source installs: rebuild from the updated checkout
```

`upgrade` takes a backup first, then pulls or builds the new version and restarts; database
migrations run automatically. If the new version doesn't become healthy, the message says how
to go back (`./opshub upgrade <previous version>`; restore the backup if a migration changed
data).

### Everyday commands

| Command | Does |
|---|---|
| `./opshub status` | Lists the containers and their health |
| `./opshub logs [service]` | Follows the logs (all, or e.g. `api`) |
| `./opshub restart` | Applies changes to `.env` |
| `./opshub backup` | Backs up now |
| `./opshub uninstall` | Stops OpsHub; data stays |
| `./opshub uninstall --delete-data` | Removes OpsHub **and all its data** (asks first) |

### Your own proxy (`--tls off`)

The web container listens on `127.0.0.1:8080` (`--http-bind`, `--http-port`). Point your proxy
at it with TLS in front, and pass `--public-url https://…`. OpsHub believes the proxy's
`X-Forwarded-For` (for the audit log and rate limits) only from `OPSHUB_REAL_IP_FROM`: the
installer sets the Docker network's gateway, right for a proxy on the same server; for a proxy
elsewhere, set its address and run `./opshub restart`. Keep `/metrics` off the public side.

### Troubleshooting

- **Let's Encrypt fails:** check that the DNS name resolves to this server and that ports 80
  and 443 are open (`./opshub logs caddy`).
- **A port is taken:** `--http-port`, `--https-port`, `--http-public-port`,
  `--prometheus-port`, `--grafana-port`; the Docker network: `--network-prefix`.
- **Settings in your shell environment** (`OPSHUB_*`) are ignored by `./opshub`: only `.env`
  counts.
- **Runner can't reach Docker:** run `./opshub runner <token>` again; it detects the socket's
  group each time.
