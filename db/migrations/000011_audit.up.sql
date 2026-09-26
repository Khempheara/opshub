-- 000011_audit: indexes for the audit log viewer (Module 11). audit_log itself exists since
-- 000001 and stays append-only; entries are kept forever.

-- Filter by area ("secret", "monitor", …): the action's part before the dot.
CREATE INDEX audit_log_org_area_created_idx ON audit_log (organization_id, split_part(action, '.', 1), created_at DESC, id DESC);
-- Filter by project (project-scoped events carry metadata.project_id).
CREATE INDEX audit_log_org_project_created_idx ON audit_log (organization_id, (metadata->>'project_id'), created_at DESC, id DESC)
  WHERE metadata ? 'project_id';
-- Filter by actor within an organization.
CREATE INDEX audit_log_org_actor_created_idx ON audit_log (organization_id, actor_user_id, created_at DESC, id DESC);
-- A user's own account activity (events without an organization) is found by actor or by
-- the user as the resource.
CREATE INDEX audit_log_account_resource_idx ON audit_log (resource_id, created_at DESC, id DESC)
  WHERE organization_id IS NULL AND resource_type = 'user';
