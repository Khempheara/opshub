# Infrastructure

**Organization → Infrastructure** is an inventory of what your organization runs: servers,
clusters, databases and domains. Servers can run the OpsHub agent to report CPU, memory and
disk usage. OpsHub checks the TLS certificate of every domain.

Every member can view infrastructure (`infra.view`). Developers and up can add, edit and delete
assets, issue agent tokens and run certificate checks (`infra.manage`). Every change is audited
(`asset.create`, `asset.update`, `asset.delete`, `asset.agent_token`).

## Assets

| Kind | Address | What OpsHub does with it |
|---|---|---|
| `server` | Host name or IP, e.g. `web-1.internal`, `10.0.0.12` | Health from the agent (below) |
| `cluster` | API server URL, e.g. `https://k8s.example.com:6443` | Inventory only |
| `database` | `host:port`, e.g. `db.internal:5432` | Inventory only |
| `domain` | Domain name, e.g. `shop.example.com`, plus a TLS port (default 443) | Certificate checks |

Names are unique per organization. Tags (up to 20, lowercase, e.g. `prod`, `region:sg`) and
metadata (up to 50 `key=value` pairs) help you group and filter. Edits use `If-Match`.

### Status

| Status | Meaning |
|---|---|
| Online | The agent sent a heartbeat in the last 90 seconds |
| Offline | The agent has reported before but not in the last 90 seconds |
| Waiting for agent | A token exists but no agent has used it yet |
| No agent | A server without an agent token |
| Valid / Expiring soon / Expired / Check failed / Not checked yet | Domains: the latest certificate check; "expiring" means within 30 days |
| Inventory | Clusters and databases |

## The agent

The agent is a mode of the runner binary: `opshub-runner agent`. It sends a heartbeat every
30 seconds with CPU, memory, disk and load, and needs no inbound connections. It reports
metrics on **Linux only** (it reads `/proc/stat`, `/proc/meminfo`, `/proc/loadavg` and
`statfs` of one path); on other systems it still sends heartbeats, without metrics.

1. On the server's page choose **Create agent token**. The token (`ohi_…`) is shown once.
   **Rotate token** issues a new one and revokes the old one immediately.
2. Run the agent, either with the binary:

   ```bash
   install -m 600 /dev/null /etc/opshub/agent-token
   printf '%s' 'ohi_…' > /etc/opshub/agent-token
   opshub-runner agent --url https://opshub.example.com --token-file /etc/opshub/agent-token
   ```

   or with the runner image, mounting the host's `/proc` and root filesystem read-only:

   ```bash
   docker run -d --name opshub-agent --restart unless-stopped \
     -v /proc:/host/proc:ro -v /:/host:ro \
     -e OPSHUB_AGENT_TOKEN=ohi_… \
     opshub-runner agent --url https://opshub.example.com \
     --proc /host/proc --disk /host --hostname "$(hostname)"
   ```

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--url` | `OPSHUB_URL` | | OpsHub base URL |
| `--token-file` | `OPSHUB_AGENT_TOKEN_FILE` | | File holding the token; refused if other users can read it. Without it the agent uses `OPSHUB_AGENT_TOKEN` |
| `--disk` | `OPSHUB_AGENT_DISK` | `/` | Path whose filesystem usage is reported |
| `--proc` | `OPSHUB_AGENT_PROC` | `/proc` | procfs to read (the host's, when running in a container) |
| `--hostname` | `OPSHUB_AGENT_HOSTNAME` | this machine's | Name to report (in a container, pass the host's) |

The agent runs as a non-root user and needs no capabilities. When OpsHub refuses its token
(`AGENT_TOKEN_INVALID`, e.g. after a rotation or deleting the asset) it exits with an error
instead of retrying.

## Metrics

Each heartbeat stores one sample (server time, so an agent's clock doesn't matter).
The charts show average and peak per interval, for ranges from 1 hour to 90 days:

- **Raw samples** are kept for the current and the previous month, in monthly partitions of
  `asset_metrics`. Queries that start within the last 30 days use them.
- **Hourly rollups** (average and peak) are kept for 400 days and answer older ranges.
- The API picks an interval that gives at most ~300 points (`step` can be set, e.g. `5m`,
  `1h`; at most 1000 points).

An hourly job rolls up the last three whole hours, creates the next months' partitions and
drops expired ones. Partition DDL runs in `opshub_maintain_metric_partitions()`, a
`SECURITY DEFINER` function owned by the migration role, so the application role stays
DML-only ([database.md](database.md#postgresql-roles-least-privilege)).

Alerts on these metrics (thresholds, notifications) arrive with monitoring in Module 9.

## Certificates

OpsHub connects to every domain asset's `address:tls_port`, reads the certificate chain and
verifies it against the system roots for that host name. It records subject, issuer, names,
validity and SHA-256 fingerprint. A failed check keeps the last known validity dates and
records a stable reason:

- `certificate is not valid for this name`
- `certificate expired or not yet valid`
- `certificate is not signed by a trusted authority`
- `connection failed: …`

Checks run every 15 minutes for domains that are due: a day after a successful check, an hour
after a failed one. **Check now** on a domain's page checks it immediately. Changing a
domain's address or port discards its certificate record.

**Infrastructure → Certificates** lists every domain's certificate, soonest expiry first, and
can show only those expiring within 7–90 days (failed checks are always included).

Probes go through the same SSRF guard as webhooks and deployments: domains that resolve to
private, loopback or link-local addresses are refused unless `OPSHUB_OUTBOUND_ALLOWED_CIDRS`
allows them.

## API

See [api.md §7](api.md#7-infrastructure-module-7). The agent authenticates with
`Authorization: Bearer ohi_…` on `POST /api/v1/agent/heartbeat`.
