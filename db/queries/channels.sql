-- name: CreateChannel :one
INSERT INTO notification_channels (id, organization_id, name, kind, config, secrets_enc, locale, created_by)
VALUES (@id, @organization_id, @name, @kind, @config, @secrets_enc, sqlc.narg(locale), sqlc.narg(created_by))
RETURNING *;

-- name: GetChannel :one
SELECT * FROM notification_channels WHERE id = @id;

-- name: ListChannels :many
SELECT * FROM notification_channels WHERE organization_id = @organization_id ORDER BY name;

-- name: UpdateChannel :one
UPDATE notification_channels SET name = @name, config = @config, secrets_enc = @secrets_enc, locale = sqlc.narg(locale),
  version = version + 1
WHERE id = @id AND version = @expected_version
RETURNING *;

-- name: DeleteChannel :execrows
DELETE FROM notification_channels WHERE id = @id;

-- name: SetChannelTest :exec
UPDATE notification_channels SET last_test_at = now(), last_test_ok = @ok WHERE id = @id;

-- name: MemberLocalesByEmail :many
-- The language of each address that belongs to a member of the organization.
SELECT lower(u.email)::text AS email, u.locale
FROM users u JOIN organization_members m ON m.user_id = u.id
WHERE m.organization_id = @organization_id AND lower(u.email) = ANY (@emails::text[]);

-- name: RulesUsingChannel :many
-- Names of the rules whose escalation notifies the channel.
SELECT r.name FROM alert_rules r
WHERE r.organization_id = @organization_id
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(r.escalation) step WHERE step->'channel_ids' ? @channel_id::text)
ORDER BY r.name;

-- name: GetOrgSlugName :one
SELECT slug, name FROM organizations WHERE id = @id;

-- name: OrgChannelIDs :many
SELECT id FROM notification_channels WHERE organization_id = @organization_id AND id = ANY (@ids::uuid[]);
