-- name: CreateAlertRule :one
INSERT INTO alert_rules (organization_id, name, kind, target_id, label, threshold, metric, for_seconds, severity, escalation, enabled, created_by)
VALUES (@organization_id, @name, @kind, sqlc.narg(target_id), sqlc.narg(label), sqlc.narg(threshold), sqlc.narg(metric),
  @for_seconds, @severity, @escalation, @enabled, sqlc.narg(created_by))
RETURNING *;

-- name: GetAlertRule :one
SELECT * FROM alert_rules WHERE id = @id;

-- name: ListAlertRules :many
SELECT r.*,
       (SELECT count(*) FROM alerts a WHERE a.rule_id = r.id AND a.status = 'firing')::integer AS firing
FROM alert_rules r WHERE r.organization_id = @organization_id ORDER BY r.name;

-- name: UpdateAlertRule :one
UPDATE alert_rules SET name = @name, target_id = sqlc.narg(target_id), label = sqlc.narg(label), threshold = sqlc.narg(threshold),
  metric = sqlc.narg(metric), for_seconds = @for_seconds, severity = @severity, escalation = @escalation, enabled = @enabled,
  version = version + 1
WHERE id = @id AND version = @expected_version
RETURNING *;

-- name: DeleteAlertRule :execrows
DELETE FROM alert_rules WHERE id = @id;

-- name: EnabledAlertRules :many
SELECT * FROM alert_rules WHERE enabled ORDER BY organization_id, id;

-- name: EvalMonitors :many
SELECT id, name, labels, enabled, last_up, last_latency_ms, last_error, down_since, last_checked_at
FROM monitors WHERE organization_id = @organization_id;

-- name: EvalServers :many
SELECT id, name, tags, (agent_token_hash IS NOT NULL)::boolean AS has_agent, last_heartbeat_at, last_metrics
FROM infra_assets WHERE organization_id = @organization_id AND kind = 'server';

-- name: EvalCertificates :many
SELECT a.id, a.name, a.tags, c.not_after, c.error, c.last_checked_at
FROM infra_assets a JOIN ssl_certificates c ON c.asset_id = a.id
WHERE a.organization_id = @organization_id AND a.kind = 'domain';

-- name: OpenAlertsForRule :many
SELECT * FROM alerts WHERE rule_id = @rule_id AND status <> 'resolved';

-- name: InsertPendingAlert :one
INSERT INTO alerts (organization_id, rule_id, rule_name, rule_kind, severity, subject_type, subject_id, subject_name, subject_labels, details)
VALUES (@organization_id, @rule_id, @rule_name, @rule_kind, @severity, @subject_type, @subject_id, @subject_name, @subject_labels, @details)
ON CONFLICT (rule_id, subject_id) WHERE status <> 'resolved' DO NOTHING
RETURNING *;

-- name: UpdateAlertObservation :exec
UPDATE alerts SET details = @details, subject_name = @subject_name, subject_labels = @subject_labels, severity = @severity, rule_name = @rule_name
WHERE id = @id;

-- name: FireAlert :one
UPDATE alerts SET status = 'firing', started_at = now(), next_step = 0, next_step_at = now()
WHERE id = @id AND status = 'pending'
RETURNING *;

-- name: DeletePendingAlert :exec
DELETE FROM alerts WHERE id = @id AND status = 'pending';

-- name: ResolveAlert :one
UPDATE alerts SET status = 'resolved', resolved_at = now(), next_step_at = NULL
WHERE id = @id AND status = 'firing'
RETURNING *;

-- name: DueEscalations :many
-- Firing, unacknowledged alerts whose next escalation step is due.
SELECT * FROM alerts
WHERE status = 'firing' AND acknowledged_at IS NULL AND next_step_at <= now()
ORDER BY next_step_at LIMIT @max_alerts FOR UPDATE SKIP LOCKED;

-- name: AdvanceAlertStep :exec
UPDATE alerts SET next_step = @next_step, next_step_at = sqlc.narg(next_step_at) WHERE id = @id;

-- name: AcknowledgeAlert :one
UPDATE alerts SET acknowledged_by = @user_id, acknowledged_at = now(), next_step_at = NULL
WHERE id = @id AND status = 'firing' AND acknowledged_at IS NULL
RETURNING *;

-- name: GetAlert :one
SELECT sqlc.embed(a), u.display_name AS acknowledged_by_name
FROM alerts a LEFT JOIN users u ON u.id = a.acknowledged_by
WHERE a.id = @id;

-- name: ListAlerts :many
-- Firing and resolved alerts, newest first (pending ones aren't alerts yet).
SELECT sqlc.embed(a), u.display_name AS acknowledged_by_name
FROM alerts a LEFT JOIN users u ON u.id = a.acknowledged_by
WHERE a.organization_id = @organization_id AND a.status <> 'pending'
  AND (sqlc.narg(status)::alert_status IS NULL OR a.status = sqlc.narg(status))
  AND (sqlc.narg(severity)::alert_severity IS NULL OR a.severity = sqlc.narg(severity))
  AND (sqlc.narg(before)::timestamptz IS NULL OR a.started_at < sqlc.narg(before))
ORDER BY a.status = 'firing' DESC, a.started_at DESC
LIMIT @max_alerts;

-- name: InsertAlertEvent :exec
INSERT INTO alert_events (alert_id, kind, channel_id, channel_name, user_id, detail)
VALUES (@alert_id, @kind, sqlc.narg(channel_id), @channel_name, sqlc.narg(user_id), @detail);

-- name: ListAlertEvents :many
SELECT e.*, u.display_name AS user_name
FROM alert_events e LEFT JOIN users u ON u.id = e.user_id
WHERE e.alert_id = @alert_id ORDER BY e.id;

-- name: LastAlertEventKind :one
SELECT kind FROM alert_events WHERE alert_id = @alert_id ORDER BY id DESC LIMIT 1;

-- name: NotifiedChannelIDs :many
-- Channels that received this alert's firing message (they also get the resolution).
SELECT DISTINCT channel_id::uuid FROM alert_events
WHERE alert_id = @alert_id AND kind = 'notified' AND channel_id IS NOT NULL AND detail = 'firing';

-- name: CreateSilence :one
INSERT INTO silences (organization_id, rule_id, subject_id, label, severity, comment, starts_at, ends_at, created_by)
VALUES (@organization_id, sqlc.narg(rule_id), sqlc.narg(subject_id), sqlc.narg(label), sqlc.narg(severity), @comment, @starts_at, @ends_at, sqlc.narg(created_by))
RETURNING *;

-- name: GetSilence :one
SELECT * FROM silences WHERE id = @id;

-- name: ListSilences :many
-- Active and upcoming silences, then (with include_expired) the last 100 expired ones.
SELECT s.*, u.display_name AS created_by_name, r.name AS rule_name
FROM silences s
LEFT JOIN users u ON u.id = s.created_by
LEFT JOIN alert_rules r ON r.id = s.rule_id
WHERE s.organization_id = @organization_id AND (@include_expired::boolean OR s.ends_at > now())
ORDER BY s.ends_at <= now(), s.starts_at DESC
LIMIT 200;

-- name: ExpireSilence :one
UPDATE silences SET ends_at = greatest(now(), starts_at + interval '1 millisecond') WHERE id = @id AND ends_at > now()
RETURNING *;

-- name: ActiveSilences :many
SELECT * FROM silences WHERE organization_id = @organization_id AND starts_at <= now() AND ends_at > now();
