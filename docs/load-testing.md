# Load testing

`tests/load/opshub.js` is a [k6](https://k6.io) test with two scenarios at a constant arrival
rate:

| Scenario | Default rate | What it does |
|---|---|---|
| `browse` | 30 requests/s | The four demo users read the organization and project lists, runs, the dashboard (DORA and pipelines over 30 days), log search, monitors, their profile, and (Owner/Admin) the audit log |
| `ingest` | 5 batches/s | A service sends 50 log lines per request with an ingest token (created before the test and revoked after it) |

## Thresholds

The run fails when any of these is missed:

| Metric | Threshold |
|---|---|
| Failed requests (including rate-limited ones) | < 1 % |
| Everyday reads, p95 | < 200 ms (the API target) |
| Dashboard aggregations, p95 | < 500 ms |
| Log search, p95 | < 300 ms |
| Log ingest (50 lines), p95 | < 300 ms |
| Requests answered 429 | 0 |

## Running it

```bash
make dev && make seed
make load                                            # 1 minute
LOAD_DURATION=5m LOAD_BROWSE_RATE=40 make load       # longer or faster
```

`make load` runs k6 in Docker against `http://host.docker.internal:$OPSHUB_WEB_PORT`, going
through the web UI's nginx like a browser does. `LOAD_BASE_URL` points it elsewhere, for
example a staging instance with its own demo organization (`ORG_SLUG`, `SEED_PASSWORD`).

In CI, run **Actions → Load test → Run workflow**. It starts the compose stack, seeds it, runs
k6 with the durations and rates you choose, and keeps the summary as an artifact. It doesn't
run on every pull request.

## Rate limits

The API limits each client address to 3 × `OPSHUB_RATE_LIMIT_RPS` (default 60 requests/s)
before authentication, and each user or token to `OPSHUB_RATE_LIMIT_RPS` (20/s) after it.

k6 sends everything from one address and as four users, so the defaults stay below both limits,
and the test measures latency rather than rate limiting. To push harder:
- raise `OPSHUB_RATE_LIMIT_RPS` and `OPSHUB_RATE_LIMIT_BURST` for the test instance; or
- run k6 from several machines.

## Baseline

On the development stack (Docker Desktop, 8 GB, seeded demo data, 30 s), p95 latency was:

| Requests | p95 |
|---|---|
| Everyday reads | 7.7 ms |
| Dashboard | 8.2 ms |
| Log search | 9.6 ms |
| Ingest (50 lines) | 11.4 ms |

That was 0 failures at about 35 requests/s. Use these as a reference for regressions, not as
production capacity.
