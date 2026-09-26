# Monitoring & alerts

**Organization → Monitoring** has five tabs:

| Tab | What it holds |
|---|---|
| Monitors | Uptime checks that OpsHub runs |
| Alerts | Alerts that fired, with their timeline |
| Alert rules | When to alert, and who to tell |
| Silences | Pauses for notifications during maintenance |
| Channels | Where messages go: Telegram, Slack, email or a webhook |

| | Owner | Admin | Developer | Viewer |
|---|:-:|:-:|:-:|:-:|
| See monitors, alerts, rules, silences (`monitor.view`) | ✅ | ✅ | ✅ | ✅ |
| Manage monitors, rules, silences (`monitor.manage`) | ✅ | ✅ | ✅ | — |
| Acknowledge alerts (`alert.ack`) | ✅ | ✅ | ✅ | — |
| See channels (`channel.view`) | ✅ | ✅ | ✅ | — |
| Manage and test channels (`channel.manage`) | ✅ | ✅ | — | — |

Every change is audited:
- `monitor.*`
- `alert_rule.*`
- `alert.ack`
- `silence.create` and `silence.expire`
- `channel.*`

## Monitors

| Type | Target | Up when |
|---|---|---|
| HTTP | A URL, e.g. `https://api.example.com/health` | The status is expected (by default any 2xx or 3xx; redirects aren't followed) and, if set, the body contains the keyword. Method `GET` or `HEAD`. |
| TCP | `host:port`, e.g. `db.internal:5432` | A connection opens. |
| SSL | `host` or `host:port` (default 443) | The certificate is valid and trusted for the host, and expires in more than the given days (default 14). |

- **Timing:** checks run every 30 s to 1 h (default 60 s) with a timeout of 1–30 s, shorter than the interval.
- **Where from:** the OpsHub server runs the checks through the same SSRF guard as webhooks and deployments. Private, loopback and link-local addresses need `OPSHUB_OUTBOUND_ALLOWED_CIDRS`.
- **Errors:** they are short and never reveal internal addresses, e.g. `unexpected status 503`, `keyword not found`, `connection failed: host not found`, `timed out`.
- **Labels** (e.g. `prod`, `db`) let rules and silences match groups of monitors.
- **Pausing:** `enabled = false` pauses a monitor. Changing the target or settings forgets the last state and checks again right away.

**History.** Each check is kept for the current and the previous month, in monthly partitions. Hourly rollups are kept for 400 days. The monitor page shows:
- uptime per interval, as a strip;
- response time, as average and peak;
- the last 20 checks.

Ranges run from 1 hour to 90 days.

## Alert rules

A rule watches one monitor or asset, or all of them, optionally only those with a label (monitors) or tag (assets):

| Condition | Fires when |
|---|---|
| Monitor is down | The monitor's last check failed. |
| Monitor is slow | The last response took longer than the limit (ms). |
| Server usage is high | A server's CPU, memory or disk is above the limit (%) in its latest heartbeat. Stale numbers from an offline agent don't count. |
| Server agent is offline | An agent that has reported stopped sending heartbeats for 90 s. A server whose agent never reported is waiting, not offline. |
| Certificate expires or fails | A domain's certificate (Module 7 checks) expires within the given days, or its check failed. |

- **Evaluation:** rules run every 30 seconds. One alert is kept per rule and subject (e.g. one per server).
- **Delay:** a condition must hold for the rule's **for** duration (0 = at once) before the alert fires. A condition that clears earlier leaves nothing behind.
- **Resolving:** a firing alert resolves by itself when the condition clears.
- **Disabling or deleting:** either one resolves the rule's alerts. Deleting keeps them as history.
- **Severities:** `info`, `warning` and `critical`.

### Escalation

A rule has up to 5 steps. Each step is a delay in minutes after the alert fired, plus the channels to notify:

```text
Step 1 · after 0 min  → #ops (Slack)
Step 2 · after 15 min → on-call email
Step 3 · after 30 min → pager webhook
```

- **Acknowledging** stops later steps. The alert keeps firing until its condition clears.
- **Resolution:** it is sent to every channel that received the firing message.
- **No steps:** a rule without steps only shows its alerts in OpsHub.

## Silences

A silence mutes notifications for the alerts it matches, for up to 90 days.
- **Matching:** every filter that is set must match: rule, subject (monitor or asset), label or tag, and severity. At least one filter is required.
- **What a silence does:** silenced alerts still fire, show in OpsHub and record `silenced` on their timeline.
- **When it ends:** if the alert is still firing, the pending notification goes out within about a minute.
- **From an alert:** Silence on an alert's page mutes that rule for that subject.

## Channels

| Channel | Settings | Credentials (write-only) |
|---|---|---|
| Telegram | Chat ID (`-100…` or `@channel`) | Bot token from @BotFather (add the bot to the chat) |
| Slack | — | Incoming webhook URL (`https://…`; Slack-compatible services work too) |
| Email | Up to 20 addresses | — (sent through `OPSHUB_SMTP_*`) |
| Webhook | URL | Optional signing secret |

- **Credentials:** they are encrypted with the master key ring and never returned. Updating a channel keeps them unless new ones are given. `opshub-api keys rotate` re-encrypts them.
- **Language:** messages are written in the channel's language (English or Khmer) when one is set. Otherwise each email address that belongs to an organization member gets that member's language, and everything else uses `OPSHUB_DEFAULT_LOCALE`.
- **Send test** sends a sample message and records whether it worked. Errors never include credentials or URLs.
- **Deleting:** a channel still used by a rule can't be deleted (`CHANNEL_IN_USE`, with the rules' names).

Deliveries are River jobs on the `monitoring` queue. Each is tried up to 5 times, and every failed attempt is recorded on the alert's timeline.

### Webhook payload

```json
{
  "event": "alert.firing",
  "text": "🔴 [Critical] Alert firing: Prod down\npayments-db is down: timed out\nhttps://ops.example.com/o/acme/monitoring/alerts/…",
  "organization": { "id": "…", "slug": "acme", "name": "Acme" },
  "alert": {
    "id": "…", "rule": "Prod down", "rule_id": "…", "kind": "monitor_down", "severity": "critical",
    "status": "firing", "subject_type": "monitor", "subject_id": "…", "subject": "payments-db",
    "labels": ["prod", "db"], "details": { "error": "timed out" },
    "started_at": "2026-09-26T04:07:32Z", "resolved_at": null, "url": "https://…"
  }
}
```

- **Events:** `alert.firing`, `alert.resolved` and `test`.
- **Headers:** `X-OpsHub-Event`, and `X-OpsHub-Delivery`, a unique ID per request.
- **Signature:** with a signing secret, `X-OpsHub-Signature: sha256=<hex HMAC-SHA256 of the body>`.
- **Success:** the request succeeds on any 2xx answer.

## API

See [api.md §9](api.md#9-monitoring--alerts-module-9).
