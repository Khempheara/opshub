-- 000007_infrastructure: the organization's asset inventory (servers, clusters, databases,
-- domains), metrics sent by infra agents, and TLS certificates probed on domains.

CREATE TYPE asset_kind AS ENUM ('server', 'cluster', 'database', 'domain');

CREATE TABLE infra_assets (
  id                  uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id     uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  kind                asset_kind NOT NULL,
  name                text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  -- Host name or IP (server), API URL (cluster), host:port (database), domain name (domain).
  address             text NOT NULL DEFAULT '' CHECK (length(address) <= 255),
  description         text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  tags                text[] NOT NULL DEFAULT '{}' CHECK (cardinality(tags) <= 20),
  metadata            jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
  tls_port            integer NOT NULL DEFAULT 443 CHECK (tls_port BETWEEN 1 AND 65535), -- domains
  -- Infra agent (servers): token stored as a SHA-256 hash; what the agent last reported.
  agent_token_hash    bytea UNIQUE,
  agent_token_prefix  text NOT NULL DEFAULT '',
  agent_version       text NOT NULL DEFAULT '' CHECK (length(agent_version) <= 50),
  agent_hostname      text NOT NULL DEFAULT '' CHECK (length(agent_hostname) <= 255),
  agent_os            text NOT NULL DEFAULT '' CHECK (length(agent_os) <= 50),
  agent_arch          text NOT NULL DEFAULT '' CHECK (length(agent_arch) <= 50),
  last_heartbeat_at   timestamptz,
  last_metrics        jsonb,       -- the latest sample, for lists
  created_by          uuid REFERENCES users (id) ON DELETE SET NULL,
  version             integer NOT NULL DEFAULT 1,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, name)
);
CREATE INDEX infra_assets_org_kind_idx ON infra_assets (organization_id, kind, name);
CREATE INDEX infra_assets_tags_idx ON infra_assets USING gin (tags);
-- Only edits (which bump version) count as updates; agent heartbeats don't.
CREATE TRIGGER infra_assets_updated_at BEFORE UPDATE ON infra_assets FOR EACH ROW
  WHEN (OLD.version IS DISTINCT FROM NEW.version) EXECUTE FUNCTION set_updated_at();

-- Raw samples, one row per heartbeat, partitioned by month. Timestamps are set by the server.
CREATE TABLE asset_metrics (
  asset_id   uuid NOT NULL REFERENCES infra_assets (id) ON DELETE CASCADE,
  ts         timestamptz NOT NULL,
  cpu_pct    real CHECK (cpu_pct BETWEEN 0 AND 100),
  mem_pct    real CHECK (mem_pct BETWEEN 0 AND 100),
  disk_pct   real CHECK (disk_pct BETWEEN 0 AND 100),
  load1      real CHECK (load1 >= 0),
  PRIMARY KEY (asset_id, ts)
) PARTITION BY RANGE (ts);

-- Hourly rollups kept long after raw partitions are dropped.
CREATE TABLE asset_metrics_hourly (
  asset_id   uuid NOT NULL REFERENCES infra_assets (id) ON DELETE CASCADE,
  hour       timestamptz NOT NULL,
  cpu_avg    real, cpu_max real,
  mem_avg    real, mem_max real,
  disk_avg   real, disk_max real,
  samples    integer NOT NULL,
  PRIMARY KEY (asset_id, hour)
);

-- The API's role can't run DDL (least privilege). This one fixed-purpose function, owned by
-- the migrator, creates this month's and the next two months' partitions and drops those
-- entirely older than keep_months full months.
CREATE FUNCTION opshub_maintain_metric_partitions(keep_months integer) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
  m date;
  r record;
BEGIN
  IF keep_months < 1 OR keep_months > 24 THEN
    RAISE EXCEPTION 'keep_months must be between 1 and 24';
  END IF;
  FOR i IN 0..2 LOOP
    m := (date_trunc('month', now()) + make_interval(months => i))::date;
    EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF asset_metrics FOR VALUES FROM (%L) TO (%L)',
      'asset_metrics_' || to_char(m, 'YYYYMM'), m, (m + interval '1 month')::date);
  END LOOP;
  FOR r IN
    SELECT c.relname FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE p.relname = 'asset_metrics' AND c.relname ~ '^asset_metrics_[0-9]{6}$'
  LOOP
    IF to_date(substr(r.relname, 15), 'YYYYMM') < (date_trunc('month', now()) - make_interval(months => keep_months))::date THEN
      EXECUTE format('DROP TABLE %I', r.relname);
    END IF;
  END LOOP;
END $$;
REVOKE ALL ON FUNCTION opshub_maintain_metric_partitions(integer) FROM PUBLIC;
-- The application role runs it from the hourly maintenance job (it has no DDL rights itself).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'opshub_app') THEN
    GRANT EXECUTE ON FUNCTION opshub_maintain_metric_partitions(integer) TO opshub_app;
  END IF;
END $$;
SELECT opshub_maintain_metric_partitions(1);

-- The certificate served on a domain asset, probed by a daily job.
CREATE TABLE ssl_certificates (
  asset_id         uuid PRIMARY KEY REFERENCES infra_assets (id) ON DELETE CASCADE,
  host             text NOT NULL,
  port             integer NOT NULL,
  subject          text NOT NULL DEFAULT '',
  issuer           text NOT NULL DEFAULT '',
  dns_names        text[] NOT NULL DEFAULT '{}',
  serial           text NOT NULL DEFAULT '',
  fingerprint      text NOT NULL DEFAULT '', -- SHA-256 of the leaf, hex
  not_before       timestamptz,
  not_after        timestamptz,
  error            text NOT NULL DEFAULT '', -- empty: valid and trusted for the host
  last_checked_at  timestamptz NOT NULL,
  next_check_at    timestamptz NOT NULL
);
CREATE INDEX ssl_certificates_expiry_idx ON ssl_certificates (not_after);
CREATE INDEX ssl_certificates_next_check_idx ON ssl_certificates (next_check_at);
