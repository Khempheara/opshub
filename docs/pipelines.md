# Pipelines (`.opshub.yml`)

A project's pipeline lives in `.opshub.yml` at the root of its connected repository. OpsHub
reads the file at the exact commit being built, so every run uses the pipeline as it was in that
commit; the run keeps a snapshot of it.

Jobs run on [runners](runners.md): each step runs with `/bin/sh -ec` in a container of the job's
`image` (default `alpine:3.20`), in a workspace holding the commit's files. Until a runner picks a
job up it shows **Waiting for a runner**, and a job nobody picks up within 24 hours fails with
`no_runner`.

## Example

```yaml
version: 1
on:
  push:
    branches: [main, "release/*"]
    tags: ["v*"]
  pull_request:
    branches: [main]
  schedule:
    - cron: "0 3 * * *"      # UTC; runs on the default branch
stages: [test, build, deploy]
variables:
  GOFLAGS: -mod=readonly
jobs:
  test:
    stage: test
    image: golang:1.23
    runs_on: [linux]         # runner labels required
    timeout: 20m
    cache: { key: "go-${OPSHUB_REF_NAME}", paths: [.cache/go] }
    steps:
      - go vet ./...
      - name: Unit tests
        run: go test -race ./...
  build:
    stage: build
    needs: [test]
    image: docker:27
    steps:
      - docker build -t app:${OPSHUB_COMMIT_SHA} .
    artifacts: { paths: [dist/], expire_in: 7d }
  deploy-prod:
    stage: deploy
    needs: [build]
    environment: production
    when: manual             # waits for an approval
    deploy:
      target: k8s-prod       # Module 6
      strategy: rolling      # rolling | blue_green
  notify:
    stage: deploy
    when: always
    image: alpine:3
    steps: [./notify.sh]
```

## Top-level keys

| Key | Required | Meaning |
|---|---|---|
| `version` | yes | Must be `1`. |
| `on` | no | Triggers (below). Default: every push. Manual runs are always possible. |
| `stages` | yes | Ordered stage names (lowercase, digits, `-`, `_`), at most 20. |
| `variables` | no | Pipeline variables for every job. Names can't start with `OPSHUB_`. |
| `jobs` | yes | Jobs by name (at most 100). |

## Triggers (`on`)

- List form: `on: [push, pull_request, tag, manual]`.
- Map form: `push: {branches: [...], tags: [...]}`, `pull_request: {branches: [...]}` (filters the
  **target** branch), `tag: {patterns: [...]}`, `schedule: [{cron: "..."}]`, `manual: true`.
- `push: {tags: [...]}` alone means tag pushes only.
- Patterns: `*` matches within a path segment (`release/*` matches `release/1.0`, not
  `release/1/x`); `**` matches across segments.
- Pull requests start runs when opened, reopened or updated with new commits.
- Schedules use standard 5-field cron in UTC (lists, ranges, steps, `MON`/`JAN` names,
  `@hourly`/`@daily`/`@weekly`/`@monthly`/`@yearly`) and run on the default branch. They are
  read from the default branch's file whenever it's pushed or run.
- A branch or tag deletion never starts a run. A push that matches no trigger is ignored; a
  push whose file is **invalid** creates a failed run that lists the problems.

## Jobs

| Key | Meaning |
|---|---|
| `stage` | Required; one of `stages`. |
| `image` | Container image; required when the job has `steps`. |
| `steps` | Commands, run in order. A string, or `{name, run}`. At most 50. |
| `needs` | Jobs this one waits for. **Without `needs`, a job waits for every job of earlier stages**; `needs: []` starts immediately. Needs can't point to a later stage or form a cycle. |
| `when` | `on_success` (default), `on_failure` (runs only if a dependency failed), `always`, `manual` (like `on_success`, then waits for an approval). |
| `environment` | A project environment. Protected environments gate the job (below). |
| `variables` | Job variables. |
| `runs_on` | Runner labels the runner must have. |
| `timeout` | `1m`–`6h`, default `60m`. |
| `artifacts` | Paths (relative to the workspace) kept after a successful job as one archive; `expire_in: Nd` (1–90, default 7). Jobs that `need` it get the files in their workspace; people download them from the job panel. |
| `cache` | `{key, paths}` restored before and saved after a successful job; shared by the project's jobs with the same key. Evicted least-recently-used beyond the project quota. |
| `deploy` | `{target, strategy}`; requires `environment` (Module 6). |

A skipped dependency whose own condition wasn't met (for example an `on_failure` job after a
green run) counts as success for jobs after it.

## Approvals and protected environments

A job waits for approval (**Needs approval**) when it has `when: manual` or targets a
protected environment with required approvals. Current rules apply at decision time:

- **Allowed branches**: a run from another branch fails the job (`branch_not_allowed`).
- **Required approvals**: from distinct people; `when: manual` needs at least one.
- **Allowed roles** mean "this role or higher" (Admin covers Owner).
- Approvers need the `approval.decide` permission (project Developer or higher).
- Whoever started the run can't approve it; each person decides once.
- One rejection fails the job (`rejected`) and skips the jobs after it.

## Variables

Precedence, lowest first: environment variables → pipeline `variables` → job `variables` →
variables given when starting a run manually → predefined variables:

`CI`, `OPSHUB`, `OPSHUB_PROJECT_SLUG`, `OPSHUB_RUN_ID`, `OPSHUB_RUN_NUMBER`, `OPSHUB_JOB_ID`,
`OPSHUB_JOB_NAME`, `OPSHUB_JOB_ATTEMPT`, `OPSHUB_STAGE`, `OPSHUB_COMMIT_SHA`, `OPSHUB_REF`,
`OPSHUB_REF_NAME`, `OPSHUB_TRIGGER`, `OPSHUB_ENVIRONMENT`.

Variables aren't secret. Secrets (Module 8) are injected separately and masked in logs.

## Runs, jobs and retries

- Run status: `queued`, `running`, `waiting` (only approvals left), `succeeded`, `failed`,
  `canceled` — derived from the jobs.
- Job status: `created` (waiting for its needs), `waiting_approval`, `queued`, `running`,
  `succeeded`, `failed`, `canceled`, `skipped`.
- Failure/skip reasons: `upstream_failed`, `not_needed`, `rejected`, `branch_not_allowed`,
  `environment_not_found`, `timeout`, `no_runner`, `step_failed`, `runner_error`, `runner_lost`
  (the runner stopped sending heartbeats or was deleted).
- **Cancel** stops every unfinished job. **Retry job** adds a new attempt of a failed or
  canceled job and re-opens the jobs after it. **Re-run failed jobs** does that for every failed
  job of a finished run. **Re-run** creates a new run from the same snapshot and commit.

## Logs and live updates

Runners send output in numbered chunks (256 KiB max each, 10 MiB per job; the rest is dropped
with a notice). Known secret values are masked before storage. The UI streams run changes and
log output with Server-Sent Events (`GET /runs/{id}/events`, `GET /jobs/{id}/logs/stream`),
resuming with `Last-Event-ID`; the whole log can be downloaded as text.

## Validation rule codes

Problems carry a line, column, path and one of these codes (translated in the UI): `syntax`,
`empty`, `file_too_large`, `type`, `unknown_field`, `duplicate`, `required`,
`unsupported_version`, `oneof`, `pattern`, `max`, `schedule_needs_cron`, `invalid_cron`,
`variable_name`, `reserved`, `job_name`, `unknown_stage`, `unknown_job`, `self_need`,
`need_later_stage`, `cycle`, `duration`, `range`, `path`, `deploy_requires_environment`.

Use **Check pipeline file** on the Pipelines tab (or `POST /projects/{id}/pipeline/validate`)
to lint a file before committing it.
