DROP TABLE IF EXISTS cache_entries;
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS job_tokens;
DROP INDEX IF EXISTS pipeline_jobs_runner_running_idx;
ALTER TABLE pipeline_jobs DROP CONSTRAINT IF EXISTS pipeline_jobs_runner_fk;
DROP TABLE IF EXISTS runner_registration_tokens;
DROP TABLE IF EXISTS runners;
