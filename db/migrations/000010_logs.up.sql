-- 000010_logs: searchable logs. Services send lines with organization ingest tokens; pipeline
-- job and deployment output is copied in by triggers as it is written. Day partitions are
-- dropped after the operator's retention.

CREATE TYPE log_level AS ENUM ('debug', 'info', 'warn', 'error');
CREATE TYPE log_source AS ENUM ('service', 'job', 'deployment');

CREATE TABLE log_entries (
  id               uuid NOT NULL DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  ts               timestamptz NOT NULL,
  source           log_source NOT NULL,
  -- The ingest token, pipeline job or deployment the line came from.
  source_id        uuid,
  -- Job and deployment lines belong to a project; searches only show projects the caller sees.
  project_id       uuid,
  service          text NOT NULL,
  level            log_level NOT NULL DEFAULT 'info',
  message          text NOT NULL,
  attributes       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(attributes) = 'object'),
  -- "simple" is language-agnostic: English and Khmer are both tokenized, never stemmed.
  -- Paths, URLs, hosts and key=value pairs are split into words so "cart" finds "/api/cart"
  -- (search queries are split the same way, see internal/logs).
  search           tsvector GENERATED ALWAYS AS (to_tsvector('simple', translate(service || ' ' || message, '/:=.', '    '))) STORED,
  PRIMARY KEY (ts, id)
) PARTITION BY RANGE (ts);
CREATE INDEX log_entries_org_ts_idx ON log_entries (organization_id, ts DESC, id DESC);
CREATE INDEX log_entries_search_idx ON log_entries USING gin (search);

-- Same least-privilege pattern as 000007 and 000009: the application role has no DDL rights,
-- so this function (owned by the migrator) creates day partitions from the ingest window
-- (at most 7 days back) to 2 days ahead, and drops those older than keep_days.
CREATE FUNCTION opshub_maintain_log_partitions(keep_days integer) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
  d date;
  r record;
BEGIN
  IF keep_days < 1 OR keep_days > 365 THEN
    RAISE EXCEPTION 'keep_days must be between 1 and 365';
  END IF;
  FOR i IN -LEAST(keep_days - 1, 7)..2 LOOP
    d := (now() AT TIME ZONE 'UTC')::date + i;
    EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF log_entries FOR VALUES FROM (%L) TO (%L)',
      'log_entries_' || to_char(d, 'YYYYMMDD'), d::timestamp AT TIME ZONE 'UTC', (d + 1)::timestamp AT TIME ZONE 'UTC');
  END LOOP;
  FOR r IN
    SELECT c.relname FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE p.relname = 'log_entries' AND c.relname ~ '^log_entries_[0-9]{8}$'
  LOOP
    IF to_date(substr(r.relname, 13), 'YYYYMMDD') < (now() AT TIME ZONE 'UTC')::date - keep_days THEN
      EXECUTE format('DROP TABLE %I', r.relname);
    END IF;
  END LOOP;
END $$;
REVOKE ALL ON FUNCTION opshub_maintain_log_partitions(integer) FROM PUBLIC;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'opshub_app') THEN
    GRANT EXECUTE ON FUNCTION opshub_maintain_log_partitions(integer) TO opshub_app;
  END IF;
END $$;
SELECT opshub_maintain_log_partitions(30);

-- Organization tokens that services use to send logs, each for one service name.
CREATE TABLE log_ingest_tokens (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  service          text NOT NULL CHECK (service ~ '^[a-z0-9][a-z0-9._/-]{0,99}$'),
  token_hash       bytea NOT NULL UNIQUE,
  token_prefix     text NOT NULL,
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  last_used_at     timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, name)
);

-- Pipeline job and deployment output, one entry per non-empty line, ANSI colours removed.
-- Red lines count as errors. Failing to copy never fails the original write.
CREATE FUNCTION opshub_mirror_job_log() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  j record;
BEGIN
  SELECT pj.id, pj.organization_id, pj.project_id, pj.name, r.number, p.slug INTO j
  FROM pipeline_jobs pj
  JOIN pipeline_runs r ON r.id = pj.run_id
  JOIN projects p ON p.id = pj.project_id
  WHERE pj.id = NEW.job_id;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  BEGIN
    INSERT INTO log_entries (organization_id, ts, source, source_id, project_id, service, level, message, attributes)
    SELECT j.organization_id, clock_timestamp(), 'job', j.id, j.project_id, left(j.slug || '/' || j.name, 100),
           CASE WHEN l ~ '\x1b\[(1;)?31' THEN 'error' ELSE 'info' END::log_level,
           left(regexp_replace(l, '\x1b\[[0-9;]*[A-Za-z]', '', 'g'), 8192),
           jsonb_build_object('project', j.slug, 'job', j.name, 'run', j.number)
    FROM regexp_split_to_table(NEW.content, '\r?\n') AS l
    WHERE btrim(regexp_replace(l, '\x1b\[[0-9;]*[A-Za-z]', '', 'g')) <> '';
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'opshub: copying job log to log_entries failed: %', SQLERRM;
  END;
  RETURN NULL;
END $$;
CREATE TRIGGER job_log_chunks_mirror AFTER INSERT ON job_log_chunks
  FOR EACH ROW EXECUTE FUNCTION opshub_mirror_job_log();

CREATE FUNCTION opshub_mirror_deployment_log() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  d record;
BEGIN
  SELECT dp.id, dp.organization_id, dp.project_id, dp.number, p.slug, e.name AS environment INTO d
  FROM deployments dp
  JOIN projects p ON p.id = dp.project_id
  JOIN environments e ON e.id = dp.environment_id
  WHERE dp.id = NEW.deployment_id;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  BEGIN
    INSERT INTO log_entries (organization_id, ts, source, source_id, project_id, service, level, message, attributes)
    SELECT d.organization_id, clock_timestamp(), 'deployment', d.id, d.project_id, left(d.slug || '/deploy', 100),
           CASE WHEN l ~ '\x1b\[(1;)?31' THEN 'error' ELSE 'info' END::log_level,
           left(regexp_replace(l, '\x1b\[[0-9;]*[A-Za-z]', '', 'g'), 8192),
           jsonb_build_object('project', d.slug, 'deployment', d.number, 'environment', d.environment)
    FROM regexp_split_to_table(NEW.content, '\r?\n') AS l
    WHERE btrim(regexp_replace(l, '\x1b\[[0-9;]*[A-Za-z]', '', 'g')) <> '';
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'opshub: copying deployment log to log_entries failed: %', SQLERRM;
  END;
  RETURN NULL;
END $$;
CREATE TRIGGER deployment_log_chunks_mirror AFTER INSERT ON deployment_log_chunks
  FOR EACH ROW EXECUTE FUNCTION opshub_mirror_deployment_log();
