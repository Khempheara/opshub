DROP TABLE IF EXISTS deployment_log_chunks;
ALTER TABLE environments DROP COLUMN IF EXISTS current_deployment_id;
DROP TABLE IF EXISTS deployments;
ALTER TABLE projects DROP COLUMN IF EXISTS last_deployment_number;
DROP TABLE IF EXISTS deploy_targets;
DROP TYPE IF EXISTS deployment_status;
DROP TYPE IF EXISTS deploy_strategy;
DROP TYPE IF EXISTS deploy_target_kind;
