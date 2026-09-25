# Roles & permissions (RBAC)

Four roles exist at **organization** and **project** level: **Owner**, **Admin**, **Developer**, **Viewer**.

## Resolving a user's role on a project

```
effective_project_role = max(
  inherited(org_role),            # Owner/Admin of the org ⇒ same role on every project
  project_members role (direct),
  project_members role (via team)
)
```

| Org role | Inherited project role |
|---|---|
| Owner | Owner on all projects |
| Admin | Admin on all projects |
| Developer | none — needs explicit project membership (direct or via team) |
| Viewer | Viewer on all projects (read-only visibility across the org) |

Personal API tokens act as their user, optionally narrowed by `scopes` (never widened).
Runners use their own credentials and can only fetch jobs, the secrets injected into those jobs, and
upload logs/artifacts for jobs assigned to them.

Rules (Module 3):

- A Developer who creates a project gets a direct **Admin** grant on it.
- Grants can be **Admin, Developer or Viewer**; project Owners are always the organization's
  Owners, so Owner is never granted per project.
- Grants only apply to organization members: when someone leaves the organization their direct
  grants are deleted; deleting a team deletes its grants.
- Grants never lower an inherited role (an org Admin granted Viewer stays Admin), but can raise
  an org Viewer or Developer on one project.
- Projects a user can't see answer `404 PROJECT_NOT_FOUND`; environments of such projects
  `404 ENVIRONMENT_NOT_FOUND`.

## Enforcement

- Checked in the **service layer** (`authz.Require(ctx, q, orgID, action)` in `internal/authz`) before
  any read or write, so REST handlers, webhooks, River jobs, and future gRPC/CLI paths are all covered.
  The matrix below is pinned by `internal/authz/authz_test.go`. The UI only hides controls as a
  convenience: it reads `GET /orgs/{orgId}/permissions` (`usePermissions`, `<RequirePermission>`).
- Denials return `403 FORBIDDEN`. For resources the caller cannot even **view**, the API returns
  `404 <RESOURCE>_NOT_FOUND` so it doesn't reveal that they exist.
- Every allowed mutating action writes an `audit_log` row.
- The last Owner of an organization cannot be removed, demoted or leave (`409 LAST_OWNER`). The check
  runs after locking the organization row (`SELECT … FOR UPDATE`), so two concurrent demotions can't
  both succeed.
- Any member may leave an organization (remove themselves); their team memberships go with it.
- Tenant isolation is tested route by route: `internal/org/http_test.go` calls every tenant-scoped
  route with another organization's IDs and expects a 404, and fails if a new route isn't covered.

## Permission matrix

✅ allowed · ⚠️ allowed with conditions (see notes) · — denied

### Organization scope

| Action | Owner | Admin | Developer | Viewer |
|---|:-:|:-:|:-:|:-:|
| View organization, members, teams | ✅ | ✅ | ✅ | ✅ |
| Update org settings | ✅ | ✅ | — | — |
| Delete organization / transfer ownership | ✅ | — | — | — |
| Invite / remove members | ✅ | ⚠️¹ | — | — |
| Change member roles | ✅ | ⚠️¹ | — | — |
| Manage teams | ✅ | ✅ | — | — |
| Create projects | ✅ | ✅ | ✅ | — |
| Manage runners (register, disable) | ✅ | ✅ | — | — |
| Manage deploy targets (clusters, servers) | ✅ | ✅ | — | — |
| Manage infrastructure inventory | ✅ | ✅ | ✅ | — |
| View infrastructure & metrics | ✅ | ✅ | ✅ | ✅ |
| Manage monitors, alert rules, silences | ✅ | ✅ | ✅ | — |
| Manage notification channels | ✅ | ✅ | — | — |
| View / export audit log | ✅ | ✅ | — | — |
| Configure SSO (OIDC) | ✅ | — | — | — |

¹ Admins can grant at most Developer (invitations and role changes), and cannot change or remove
Owners or other Admins. They may change their own role or leave. Owners manage everyone.

### Project scope (effective project role)

| Action | Owner | Admin | Developer | Viewer |
|---|:-:|:-:|:-:|:-:|
| View project, pipelines, runs, deployments | ✅ | ✅ | ✅ | ✅ |
| View job logs | ✅ | ✅ | ✅ | ✅ |
| Update project settings | ✅ | ✅ | — | — |
| Delete project | ✅ | ✅ | — | — |
| Manage project members | ✅ | ✅ | — | — |
| Connect / disconnect repository | ✅ | ✅ | — | — |
| Manage environments & protection rules | ✅ | ✅ | — | — |
| Trigger / re-run / retry pipeline | ✅ | ✅ | ✅ | — |
| Cancel pipeline run | ✅ | ✅ | ✅ | — |
| Approve manual gate — non-protected env | ✅ | ✅ | ✅ | — |
| Approve manual gate — protected env (e.g. prod) | ✅ | ✅ | ⚠️² | — |
| Deploy to non-protected environment | ✅ | ✅ | ✅ | — |
| Deploy to protected environment | ✅ | ✅ | ⚠️² | — |
| Rollback deployment | ✅ | ✅ | ⚠️² | — |
| List secret names & metadata | ✅ | ✅ | ✅ | — |
| Create / rotate / delete secrets | ✅ | ✅ | ⚠️³ | — |
| Reveal secret value | — | — | — | — |
| Search service logs | ✅ | ✅ | ✅ | ✅ |

² Only if the environment's protection rule allows `developer` (allowed roles mean "this role or higher"). Protection rules can
additionally require N approvals from distinct users; the approver may not be the person who triggered the run.

³ Secrets of unprotected environments only. A secret for all environments also reaches protected
ones, so it counts as protected.

**Secrets are write-only for everyone.** After creation, values are only ever decrypted for the runner
executing a job that lists them (audited as `secret.read`); there is no "reveal" permission
([secrets.md](secrets.md)).

## Action identifiers

Actions are named `<resource>.<verb>`. They are used by `authz.Require`, listed per endpoint in
[api.md](api.md), and reused as audit-log `action` values for mutations.

| Matrix row | Action(s) |
|---|---|
| View organization, members, teams | `org.view`, `member.view`, `team.view` |
| Update org settings · delete · transfer | `org.update`, `org.delete`, `org.transfer` |
| Invite/remove members · change roles | `member.invite`, `member.remove`, `member.update_role` |
| Manage teams | `team.manage` |
| Create projects | `project.create` |
| Manage runners | `runner.view` (Owner/Admin/Developer), `runner.manage` |
| Manage deploy targets | `target.view` (all roles), `target.manage` |
| Infrastructure | `infra.view`, `infra.manage` |
| Monitors, alert rules, silences · acknowledge | `monitor.view`, `monitor.manage`, `alert.ack` (Owner/Admin/Developer) |
| Notification channels | `channel.view` (Owner/Admin/Developer), `channel.manage` |
| Audit log | `audit.view`, `audit.export` |
| Project view · update · delete · members | `project.view`, `project.update`, `project.delete`, `project.manage_members` |
| Repository · environments | `repo.connect`, `environment.manage` |
| Runs | `run.view`, `pipeline.trigger` (trigger, re-run, retry), `run.cancel` |
| Approvals | `approval.decide` |
| Deployments | `deployment.view`, `deployment.create`, `deployment.rollback` |
| Secrets | `secret.list`, `secret.create`, `secret.update`, `secret.rotate`, `secret.delete`; runner fetch audited as `secret.read` |
| Logs | `logs.view` |

Organization changes are audited as `org.create`, `org.update`, `org.delete`, `org.transfer`,
`member.invite`, `member.invite_revoke`, `member.join`, `member.update_role`, `member.remove`,
`member.leave`, `team.create`, `team.update`, `team.delete`, `team.add_member`, `team.remove_member`
(role changes and renames record before/after values).

Authentication events are audited too: `auth.login`, `auth.login_failed`, `auth.locked`,
`auth.refresh_reuse`, `auth.2fa_enabled`, `auth.password_changed`, `token.create`, `token.revoke`.
