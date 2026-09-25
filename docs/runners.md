# Runners

A runner is a machine with Docker that runs pipeline jobs. It runs the `opshub-runner` agent,
which polls OpsHub for jobs over HTTPS. Runners never need inbound connections, Git credentials
or database access: everything goes through the [runner API](api.md#5-runners-module-5).

## Register a runner

1. **Organization → Runners → Register runner** (Owners and Admins; Developers can view).
   Optionally add labels that this runner should carry (e.g. `gpu`).
2. The token (`ohr_reg_…`) is shown once, works for one runner and expires after an hour.
3. On the runner machine, either use the binary:

   ```bash
   opshub-runner register --url https://opshub.example.com --token ohr_reg_… --name build-1 --labels linux,docker
   opshub-runner run
   ```

   or the container image (`make docker` builds `opshub-runner`):

   ```bash
   docker run -d --name opshub-runner --restart unless-stopped \
     --group-add "$(stat -c %g /var/run/docker.sock)" \
     -v /var/run/docker.sock:/var/run/docker.sock \
     -v opshub-runner:/var/lib/opshub-runner \
     -e OPSHUB_URL=https://opshub.example.com \
     -e OPSHUB_REGISTRATION_TOKEN=ohr_reg_… \
     opshub-runner
   ```

   On first start the container registers itself and keeps the runner token in its volume;
   the registration token is not needed afterwards.

**Local development:** put the token in `.env` as `OPSHUB_RUNNER_REGISTRATION_TOKEN` and run
`make runner` (compose profile `runner`, using this machine's Docker socket;
`OPSHUB_DOCKER_GID` is the socket's group, 0 on Docker Desktop).

## Configuration

`register` writes a JSON file (mode 0600; `run` refuses files other users can read). The default
path is `/var/lib/opshub-runner/config.json`, or `OPSHUB_RUNNER_CONFIG`.

| Field | Default | |
|---|---|---|
| `url`, `runner_id`, `token`, `name`, `labels` | from registration | |
| `max_concurrency` | 1 | Jobs at once (1–64); also editable in the UI |
| `docker_socket` | `/var/run/docker.sock` | |
| `default_image` | `alpine:3.20` | For jobs without `image` |
| `always_pull` | false | Pull images even when present |
| `cpus`, `memory_mb` | no limit | Per step container |
| `pids_limit` | 4096 | Per step container |
| `network` | default bridge | Docker network for step containers |
| `temp_dir` | system temp | Staging for artifact and cache archives |

`opshub-runner run --shutdown-grace 10m`: on SIGTERM the runner takes no new jobs and lets
running ones finish for up to the grace period, then stops them (reported as `runner_error`).

## How a job runs

1. The agent long-polls `POST /runner/jobs/request`. OpsHub assigns the oldest queued job of the
   organization whose `runs_on` labels the runner has, as long as the runner is below its
   `max_concurrency`, and hands out a job token valid only for that job.
2. A Docker volume becomes `/workspace`. The agent downloads the commit as a tarball **through
   OpsHub** (OpsHub fetches it from GitHub/GitLab with the project's stored token), then the
   artifacts of the jobs in `needs` and the cache for `cache.key`.
3. Each step runs in a new container of the job's image: `/bin/sh -ec "<step>"`, working
   directory `/workspace`, the job's variables in the environment (plus `OPSHUB_WORKSPACE`).
   Containers are never privileged and run with `no-new-privileges`. Output streams to OpsHub
   about once a second.
4. After the last step succeeds, the agent uploads `artifacts.paths` as one archive and saves
   `cache.paths` under the cache key, then reports the result. The volume and containers are
   removed; leftovers from a crash are removed when the agent starts again.

Images must contain `/bin/sh`. Steps run as the image's user; files copied into the workspace
belong to root.

**Timeouts and cancellation:** a job's `timeout` is enforced by the agent (the container is
killed, result `timeout`) and by OpsHub. When a run is canceled, the next heartbeat (≤ 10 s)
tells the agent to kill the job's container.

**Lost runners:** a runner that sends no heartbeat for 30 seconds is shown as offline and its
running jobs fail with `runner_lost`. Deleting a runner revokes its token and fails its running
jobs the same way. Disabling a runner stops new assignments; running jobs finish.

## Artifacts and cache

- Stored by the API under `OPSHUB_BLOB_DIR` (default `data/blobs`; `/data/blobs` in the image).
- Limits: `OPSHUB_ARTIFACT_MAX_BYTES` (100 MiB per job), `OPSHUB_CACHE_MAX_BYTES` (500 MiB per
  entry), `OPSHUB_CACHE_PROJECT_QUOTA_BYTES` (2 GiB per project, least recently used evicted
  first), `OPSHUB_SOURCE_MAX_BYTES` (500 MiB per checkout).
- Artifacts expire after `expire_in` (default 7 days); a background job removes expired
  artifacts and evicts caches every 30 seconds.
- People with `run.view` download artifacts from the job panel of a run.

## Security

- **The Docker socket is root-equivalent on the host.** Anyone who can run a pipeline on a
  runner can control that machine's Docker. Use machines dedicated to CI, and use labels
  (`runs_on`) to keep sensitive jobs on dedicated runners.
- Runner, registration and job tokens are stored as SHA-256 hashes. Job tokens stop working
  when the job finishes. The runner API is rate limited per token.
- The runner image is distroless and runs as a non-root user; it needs the socket's group
  (`--group-add`).
- Job containers can reach the network like any container on that Docker host; pick the
  `network` accordingly.
