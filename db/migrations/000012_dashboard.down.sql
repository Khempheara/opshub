DROP INDEX IF EXISTS alerts_org_resolved_idx;
DROP INDEX IF EXISTS deployments_env_succeeded_idx;
DROP INDEX IF EXISTS deployments_rollback_of_idx;
DROP INDEX IF EXISTS deployments_org_created_idx;
DROP INDEX IF EXISTS pipeline_runs_org_created_idx;
ALTER TABLE pipeline_runs DROP COLUMN IF EXISTS committed_at;
