-- 000009_monitoring: uptime monitors with their results, alert rules over monitors and
-- infrastructure (Module 7), alerts with a timeline, silences, and notification channels.

CREATE TYPE monitor_kind AS ENUM ('http', 'tcp', 'ssl');
CREATE TYPE alert_rule_kind AS ENUM ('monitor_down', 'monitor_latency', 'asset_metric', 'asset_offline', 'certificate');
CREATE TYPE alert_severity AS ENUM ('info', 'warning', 'critical');
CREATE TYPE alert_status AS ENUM ('pending', 'firing', 'resolved');
CREATE TYPE channel_kind AS ENUM ('telegram', 'slack', 'email', 'webhook');

CREATE TABLE monitors (
  id                uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id   uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  kind              monitor_kind NOT NULL,
  -- http(s)://… for http; host:port for tcp and ssl.
  target            text NOT NULL CHECK (length(target) BETWEEN 1 AND 2048),
  interval_seconds  integer NOT NULL DEFAULT 60 CHECK (interval_seconds BETWEEN 30 AND 3600),
  timeout_ms        integer NOT NULL DEFAULT 10000 CHECK (timeout_ms BETWEEN 1000 AND 30000),
  -- http: {method, expected_status[], keyword}; ssl: {expiry_days}.
  config            jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(config) = 'object'),
  labels            text[] NOT NULL DEFAULT '{}' CHECK (cardinality(labels) <= 20),
  enabled           boolean NOT NULL DEFAULT true,
  -- Latest state, updated by each check.
  last_up           boolean,
  last_checked_at   timestamptz,
  last_latency_ms   integer,
  last_error        text NOT NULL DEFAULT '',
  down_since        timestamptz,
  next_check_at     timestamptz NOT NULL DEFAULT now(),
  created_by        uuid REFERENCES users (id) ON DELETE SET NULL,
  version           integer NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, name)
);
CREATE INDEX monitors_due_idx ON monitors (next_check_at) WHERE enabled;
-- Only edits (which bump version) count as updates; checks don't.
CREATE TRIGGER monitors_updated_at BEFORE UPDATE ON monitors FOR EACH ROW
  WHEN (OLD.version IS DISTINCT FROM NEW.version) EXECUTE FUNCTION set_updated_at();

-- One row per check, partitioned by month. Timestamps are set by the server.
CREATE TABLE monitor_results (
  monitor_id   uuid NOT NULL REFERENCES monitors (id) ON DELETE CASCADE,
  ts           timestamptz NOT NULL,
  up           boolean NOT NULL,
  latency_ms   integer CHECK (latency_ms >= 0),
  status_code  integer,
  error        text NOT NULL DEFAULT '',
  PRIMARY KEY (monitor_id, ts)
) PARTITION BY RANGE (ts);

-- Hourly rollups kept long after raw partitions are dropped.
CREATE TABLE monitor_results_hourly (
  monitor_id   uuid NOT NULL REFERENCES monitors (id) ON DELETE CASCADE,
  hour         timestamptz NOT NULL,
  checks       integer NOT NULL,
  up_checks    integer NOT NULL,
  latency_avg  real,
  latency_max  integer,
  PRIMARY KEY (monitor_id, hour)
);

-- Same pattern as opshub_maintain_metric_partitions (000007): the application role has no
-- DDL rights, so one fixed-purpose function owned by the migrator manages the partitions.
CREATE FUNCTION opshub_maintain_monitor_partitions(keep_months integer) RETURNS void
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
    EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF monitor_results FOR VALUES FROM (%L) TO (%L)',
      'monitor_results_' || to_char(m, 'YYYYMM'), m, (m + interval '1 month')::date);
  END LOOP;
  FOR r IN
    SELECT c.relname FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE p.relname = 'monitor_results' AND c.relname ~ '^monitor_results_[0-9]{6}$'
  LOOP
    IF to_date(substr(r.relname, 17), 'YYYYMM') < (date_trunc('month', now()) - make_interval(months => keep_months))::date THEN
      EXECUTE format('DROP TABLE %I', r.relname);
    END IF;
  END LOOP;
END $$;
REVOKE ALL ON FUNCTION opshub_maintain_monitor_partitions(integer) FROM PUBLIC;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'opshub_app') THEN
    GRANT EXECUTE ON FUNCTION opshub_maintain_monitor_partitions(integer) TO opshub_app;
  END IF;
END $$;
SELECT opshub_maintain_monitor_partitions(1);

CREATE TABLE notification_channels (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  kind             channel_kind NOT NULL,
  -- Non-secret settings (telegram chat id, email addresses, webhook URL).
  config           jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(config) = 'object'),
  -- AES-GCM (master key ring, aad = id) of the JSON credentials; never returned.
  secrets_enc      bytea NOT NULL,
  -- Messages in this language; NULL = each member recipient's own, else the org default.
  locale           text CHECK (locale IN ('en', 'km')),
  last_test_at     timestamptz,
  last_test_ok     boolean,
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  version          integer NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, name)
);
CREATE TRIGGER notification_channels_updated_at BEFORE UPDATE ON notification_channels FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE alert_rules (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  kind             alert_rule_kind NOT NULL,
  -- What the rule watches: one monitor or asset (target_id), or all of them, optionally
  -- narrowed to a label (monitors) or tag (assets).
  target_id        uuid,
  label            text,
  -- monitor_latency: ms; asset_metric: percent; certificate: days before expiry.
  threshold        double precision,
  -- asset_metric: cpu | mem | disk.
  metric           text CHECK (metric IN ('cpu', 'mem', 'disk')),
  for_seconds      integer NOT NULL DEFAULT 0 CHECK (for_seconds BETWEEN 0 AND 86400),
  severity         alert_severity NOT NULL DEFAULT 'warning',
  -- [{after_minutes, channel_ids[]}], at most 5 steps, the first after 0 minutes.
  escalation       jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(escalation) = 'array'),
  enabled          boolean NOT NULL DEFAULT true,
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  version          integer NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, name)
);
CREATE TRIGGER alert_rules_updated_at BEFORE UPDATE ON alert_rules FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One alert per rule and subject (a monitor or an asset) while it lasts. pending: the
-- condition holds but not yet for the rule's duration; firing: notified; resolved: history.
CREATE TABLE alerts (
  id                    uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  rule_id               uuid REFERENCES alert_rules (id) ON DELETE SET NULL,
  rule_name             text NOT NULL,
  rule_kind             alert_rule_kind NOT NULL,
  severity              alert_severity NOT NULL,
  subject_type          text NOT NULL CHECK (subject_type IN ('monitor', 'asset')),
  subject_id            uuid NOT NULL,
  subject_name          text NOT NULL,
  subject_labels        text[] NOT NULL DEFAULT '{}',
  status                alert_status NOT NULL DEFAULT 'pending',
  -- What was observed, for messages ({"value": 93.5, "error": "…"}).
  details               jsonb NOT NULL DEFAULT '{}',
  pending_since         timestamptz NOT NULL DEFAULT now(),
  started_at            timestamptz,
  resolved_at           timestamptz,
  acknowledged_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  acknowledged_at       timestamptz,
  -- The next escalation step to send and when; NULL once all steps went out or acknowledged.
  next_step             integer NOT NULL DEFAULT 0,
  next_step_at          timestamptz
);
-- At most one open (pending or firing) alert per rule and subject.
CREATE UNIQUE INDEX alerts_open_key ON alerts (rule_id, subject_id) WHERE status <> 'resolved';
CREATE INDEX alerts_org_started_idx ON alerts (organization_id, started_at DESC) WHERE status <> 'pending';
CREATE INDEX alerts_due_idx ON alerts (next_step_at) WHERE status = 'firing';

-- The alert's timeline: fired, notified (per channel), notify_failed, escalated,
-- acknowledged, silenced, resolved.
CREATE TABLE alert_events (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  alert_id    uuid NOT NULL REFERENCES alerts (id) ON DELETE CASCADE,
  at          timestamptz NOT NULL DEFAULT now(),
  kind        text NOT NULL CHECK (kind IN ('fired', 'notified', 'notify_failed', 'escalated', 'acknowledged', 'silenced', 'resolved')),
  channel_id  uuid REFERENCES notification_channels (id) ON DELETE SET NULL,
  channel_name text NOT NULL DEFAULT '',
  user_id     uuid REFERENCES users (id) ON DELETE SET NULL,
  detail      text NOT NULL DEFAULT ''
);
CREATE INDEX alert_events_alert_idx ON alert_events (alert_id, id);

-- A silence mutes notifications for the alerts it matches between starts_at and ends_at.
-- Every set matcher must match; at least one is required.
CREATE TABLE silences (
  id               uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
  organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
  rule_id          uuid REFERENCES alert_rules (id) ON DELETE CASCADE,
  subject_id       uuid,
  label            text,
  severity         alert_severity,
  comment          text NOT NULL DEFAULT '' CHECK (length(comment) <= 500),
  starts_at        timestamptz NOT NULL,
  ends_at          timestamptz NOT NULL,
  created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at),
  CHECK (rule_id IS NOT NULL OR subject_id IS NOT NULL OR label IS NOT NULL OR severity IS NOT NULL)
);
CREATE INDEX silences_org_ends_idx ON silences (organization_id, ends_at);
