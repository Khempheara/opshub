# Dashboard

An organization's **Overview** page is its dashboard. Until the required setup is done
(two-factor authentication and a first project), a getting-started checklist appears above it.

Every member sees the dashboard, computed over the projects they can view:
- Owners, Admins and Viewers see every project.
- Developers see the projects granted to them or their teams.

Filter by **project** and **time range**: the last 7, 30 or 90 days, or 12 months. Periods of up
to 92 days are shown per day, longer ones per week (starting on Monday), in your time zone
(Settings → Profile). The figures refresh every minute.

## Delivery performance (DORA)

The four [DORA](https://dora.dev) metrics describe how often and how safely changes reach
production. A **change** is a finished deployment (succeeded or failed) to a production
environment, one whose kind is *production*. Rollbacks aren't changes: they are how you recover
from one.

| Metric | How OpsHub measures it | Elite | High | Medium | Low |
|---|---|---|---|---|---|
| **Deployment frequency** | Successful changes per day, shown per day, week or month | daily or more | weekly | monthly | less |
| **Lead time for changes** | Median time from the commit to the successful production deployment of a change made by a pipeline | < 1 day | < 1 week | < 1 month | longer |
| **Change failure rate** | Changes that failed or were rolled back later ÷ all changes | ≤ 5 % | ≤ 10 % | ≤ 15 % | more |
| **Time to restore** | Median time from a failed change until production works again | < 1 hour | < 1 day | < 1 week | longer |

The levels follow the State of DevOps benchmarks, and each card shows its level.

**The details of each metric:**
- **Commit time:** OpsHub records it for every run from the Git host (the committer date). Runs
  from before Module 12, or commits the host can't describe, start from the run's creation
  instead.
- **Manual deployments** without a pipeline run count as changes, but not for lead time.
- **Restore time** depends on how the change failed:
  - a failed deployment that reverted itself counts until its revert finished;
  - another failed deployment counts until the next successful deployment to that environment;
  - a change rolled back later counts from when it went live until the next successful
    deployment (normally the rollback).
- **Not restored yet:** failed changes still waiting for a fix are counted separately.
- **Alert recovery** is shown beside the chart: the median time from an alert firing to
  resolved. It covers the whole organization, because alerts aren't tied to projects.

The **Production changes** chart splits each day or week into changes that succeeded and stayed,
and changes that failed or were rolled back.

## Pipelines

- **Runs** created in the range, and the **success rate**: succeeded ÷ (succeeded + failed).
  Canceled and unfinished runs don't count toward the rate.
- **Duration** of finished runs, from start to finish: the median, and the 95th percentile.
- Charts of runs per day or week (succeeded, failed, canceled or running), and of the median and
  95th-percentile duration.

## Projects

A table of the busiest projects (up to 50):
- runs, pipeline success rate and median run time;
- production changes, change failure rate and lead time;
- the last run's status and the time of the latest activity.

Each project links to its page.

## How it's computed

Everything is computed on request from `pipeline_runs`, `deployments` and `alerts` (migration
`000012` adds `pipeline_runs.committed_at` and the indexes). There is no materialized copy, so
the numbers are always current and the medians are exact over any range up to 366 days. The API
is `GET /orgs/{orgId}/dashboard/pipelines` and `/dashboard/dora` ([api.md §12](api.md)).

## Demo data

`make seed` adds **Checkout Web · ទំព័រទូទាត់** (`checkout-web`) with 60 days of history, so the
charts and metrics have something to show:
- runs on weekdays, some failing;
- production deployments through staging, including failures, automatic reverts and a rollback.
