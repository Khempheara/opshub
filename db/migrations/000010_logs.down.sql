DROP TRIGGER IF EXISTS deployment_log_chunks_mirror ON deployment_log_chunks;
DROP TRIGGER IF EXISTS job_log_chunks_mirror ON job_log_chunks;
DROP FUNCTION IF EXISTS opshub_mirror_deployment_log();
DROP FUNCTION IF EXISTS opshub_mirror_job_log();
DROP TABLE IF EXISTS log_ingest_tokens;
DROP FUNCTION IF EXISTS opshub_maintain_log_partitions(integer);
DROP TABLE IF EXISTS log_entries; -- drops its partitions
DROP TYPE IF EXISTS log_source;
DROP TYPE IF EXISTS log_level;
