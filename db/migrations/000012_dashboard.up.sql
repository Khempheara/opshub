-- 000012_dashboard: pipeline statistics and DORA metrics (Module 12) are computed on request
-- from runs, deployments and alerts; this adds the commit time lead time starts from, and the
-- indexes the organization-wide queries use.

-- When the run's commit was made (from the Git provider). NULL for runs created before this
-- migration or when the provider didn't say; lead time then starts at the run's creation.
ALTER TABLE pipeline_runs ADD COLUMN committed_at timestamptz;

CREATE INDEX pipeline_runs_org_created_idx ON pipeline_runs (organization_id, created_at);
CREATE INDEX deployments_org_created_idx ON deployments (organization_id, created_at);
-- "Was this deployment rolled back later?"
CREATE INDEX deployments_rollback_of_idx ON deployments (rollback_of_id) WHERE rollback_of_id IS NOT NULL;
-- Time to restore: the next successful deployment in an environment.
CREATE INDEX deployments_env_succeeded_idx ON deployments (environment_id, finished_at) WHERE status = 'succeeded';
CREATE INDEX alerts_org_resolved_idx ON alerts (organization_id, resolved_at) WHERE status = 'resolved';
