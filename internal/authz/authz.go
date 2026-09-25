// Package authz implements role-based access control (docs/rbac.md). Every service checks
// permissions here before reading or changing tenant data, so REST handlers, background
// jobs and future entry points share one enforcement point.
//
// Non-members of an organization get ORG_NOT_FOUND (404), never FORBIDDEN, so the existence
// of other tenants is not revealed. Members lacking a permission get FORBIDDEN (403).
package authz

import (
	"context"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// Role is an organization (and, from Module 3, project) role.
type Role = store.MemberRole

const (
	Owner     = store.MemberRoleOwner
	Admin     = store.MemberRoleAdmin
	Developer = store.MemberRoleDeveloper
	Viewer    = store.MemberRoleViewer
)

var rank = map[Role]int{Viewer: 1, Developer: 2, Admin: 3, Owner: 4}

// AtLeast reports whether r is min or higher (owner > admin > developer > viewer).
func AtLeast(r, min Role) bool { return rank[r] >= rank[min] }

// ValidRole reports whether r is a known role.
func ValidRole(r Role) bool { _, ok := rank[r]; return ok }

// Action is "<resource>.<verb>"; the same string is used as the audit-log action.
type Action string

// Organization-scope actions.
const (
	OrgView          Action = "org.view"
	OrgUpdate        Action = "org.update"
	OrgDelete        Action = "org.delete"
	OrgTransfer      Action = "org.transfer"
	MemberView       Action = "member.view"
	MemberInvite     Action = "member.invite"
	MemberRemove     Action = "member.remove"
	MemberUpdateRole Action = "member.update_role"
	TeamView         Action = "team.view"
	TeamManage       Action = "team.manage"
	ProjectCreate    Action = "project.create"
	RunnerView       Action = "runner.view"
	RunnerManage     Action = "runner.manage"
	TargetView       Action = "target.view"
	TargetManage     Action = "target.manage"
	InfraView        Action = "infra.view"
	InfraManage      Action = "infra.manage"
	MonitorView      Action = "monitor.view"
	MonitorManage    Action = "monitor.manage"
	AlertAck         Action = "alert.ack"
	ChannelView      Action = "channel.view"
	ChannelManage    Action = "channel.manage"
	AuditView        Action = "audit.view"
	AuditExport      Action = "audit.export"
	LogsView         Action = "logs.view"
)

// orgMatrix is the minimum role for each organization-scope action (docs/rbac.md).
// Conditional rules (e.g. Admins can't change Owners) are enforced by the calling service
// with the helpers below.
var orgMatrix = map[Action]Role{
	OrgView:          Viewer,
	OrgUpdate:        Admin,
	OrgDelete:        Owner,
	OrgTransfer:      Owner,
	MemberView:       Viewer,
	MemberInvite:     Admin,
	MemberRemove:     Admin,
	MemberUpdateRole: Admin,
	TeamView:         Viewer,
	TeamManage:       Admin,
	ProjectCreate:    Developer,
	RunnerView:       Developer,
	RunnerManage:     Admin,
	TargetView:       Viewer,
	TargetManage:     Admin,
	InfraView:        Viewer,
	InfraManage:      Developer,
	MonitorView:      Viewer,
	MonitorManage:    Developer,
	AlertAck:         Developer,
	ChannelView:      Developer,
	ChannelManage:    Admin,
	AuditView:        Admin,
	AuditExport:      Admin,
	LogsView:         Viewer,
}

// OrgActions lists every organization-scope action (stable order), for the permissions API.
func OrgActions() []Action {
	out := make([]Action, 0, len(orgMatrix))
	for a := range orgMatrix {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// Can reports whether role may perform an organization-scope action.
func Can(role Role, a Action) bool {
	min, ok := orgMatrix[a]
	return ok && AtLeast(role, min)
}

// Allowed returns every organization-scope action role may perform.
func Allowed(role Role) []Action {
	var out []Action
	for _, a := range OrgActions() {
		if Can(role, a) {
			out = append(out, a)
		}
	}
	return out
}

// Project-scope actions. Later modules add secrets.
const (
	ProjectView          Action = "project.view"
	ProjectUpdate        Action = "project.update"
	ProjectDelete        Action = "project.delete"
	ProjectManageMembers Action = "project.manage_members"
	RepoConnect          Action = "repo.connect"
	EnvironmentManage    Action = "environment.manage"
	RunView              Action = "run.view"
	PipelineTrigger      Action = "pipeline.trigger" // trigger, re-run, retry
	RunCancel            Action = "run.cancel"
	ApprovalDecide       Action = "approval.decide" // further limited by environment protection rules
	DeploymentView       Action = "deployment.view"
	DeploymentCreate     Action = "deployment.create"   // further limited by environment protection rules
	DeploymentRollback   Action = "deployment.rollback" // further limited by environment protection rules
)

// projectMatrix is the minimum effective project role for each project-scope action.
var projectMatrix = map[Action]Role{
	ProjectView:          Viewer,
	ProjectUpdate:        Admin,
	ProjectDelete:        Admin,
	ProjectManageMembers: Admin,
	RepoConnect:          Admin,
	EnvironmentManage:    Admin,
	RunView:              Viewer,
	PipelineTrigger:      Developer,
	RunCancel:            Developer,
	ApprovalDecide:       Developer,
	DeploymentView:       Viewer,
	DeploymentCreate:     Developer,
	DeploymentRollback:   Developer,
}

// ProjectActions lists every project-scope action (stable order).
func ProjectActions() []Action {
	out := make([]Action, 0, len(projectMatrix))
	for a := range projectMatrix {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// CanProject reports whether an effective project role may perform a project-scope action.
func CanProject(role Role, a Action) bool {
	min, ok := projectMatrix[a]
	return ok && AtLeast(role, min)
}

// AllowedProject returns every project-scope action the role may perform.
func AllowedProject(role Role) []Action {
	var out []Action
	for _, a := range ProjectActions() {
		if CanProject(role, a) {
			out = append(out, a)
		}
	}
	return out
}

// InheritedProjectRole is the project role an organization role grants on every project:
// Owners, Admins and Viewers keep their role; Developers need an explicit grant.
func InheritedProjectRole(orgRole Role) (Role, bool) {
	switch orgRole {
	case Owner, Admin, Viewer:
		return orgRole, true
	}
	return "", false
}

// EffectiveProjectRole is the highest of the inherited role and the direct and team grants
// (docs/rbac.md). Empty strings mean "none". ok is false when the user has no access.
func EffectiveProjectRole(orgRole, direct, team Role) (Role, bool) {
	if !ValidRole(orgRole) {
		return "", false // not a member of the organization: grants don't apply
	}
	var best Role
	if r, ok := InheritedProjectRole(orgRole); ok {
		best = r
	}
	for _, r := range []Role{direct, team} {
		if ValidRole(r) && (best == "" || AtLeast(r, best)) {
			best = r
		}
	}
	return best, best != ""
}

// GrantableProjectRole reports whether a role may be granted on a project. Owner is never
// granted per project: project Owners are the organization's Owners.
func GrantableProjectRole(r Role) bool { return ValidRole(r) && r != Owner }

// Membership is the verified caller of an organization-scoped operation.
type Membership struct {
	OrganizationID uuid.UUID
	UserID         uuid.UUID
	Role           Role
}

// Can reports whether the member may perform an action.
func (m Membership) Can(a Action) bool { return Can(m.Role, a) }

// MaxAssignableRole is the highest role this member may grant: Owners may grant any role;
// Admins at most Developer, because Admins may not create or change other Admins or Owners
// (docs/rbac.md, note 1).
func (m Membership) MaxAssignableRole() Role {
	if m.Role == Owner {
		return Owner
	}
	return Developer
}

// CanManageMember reports whether the caller may change or remove a member holding role
// target. Owners manage everyone; Admins only Developers and Viewers (and themselves).
func (m Membership) CanManageMember(target Role, targetUserID uuid.UUID) bool {
	if m.Role == Owner {
		return true
	}
	if targetUserID == m.UserID {
		return true
	}
	return m.Role == Admin && !AtLeast(target, Admin)
}

// Querier is the subset of the store this package needs (a pool or a transaction).
type Querier interface {
	GetMembership(ctx context.Context, arg store.GetMembershipParams) (store.MemberRole, error)
}

// ErrOrgNotFound is returned for missing organizations and for non-members alike.
func ErrOrgNotFound() *apperr.Error {
	return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
}

// Membership resolves the caller's membership in an organization. It returns
// UNAUTHENTICATED without a principal and ORG_NOT_FOUND for non-members.
func Resolve(ctx context.Context, q Querier, orgID uuid.UUID) (Membership, error) {
	p, ok := authn.PrincipalFrom(ctx)
	if !ok {
		return Membership{}, apperr.Unauthenticated()
	}
	role, err := q.GetMembership(ctx, store.GetMembershipParams{OrganizationID: orgID, UserID: p.UserID})
	if database.IsNoRows(err) {
		return Membership{}, ErrOrgNotFound()
	}
	if err != nil {
		return Membership{}, err
	}
	return Membership{OrganizationID: orgID, UserID: p.UserID, Role: role}, nil
}

// Require resolves the caller's membership and checks the action.
func Require(ctx context.Context, q Querier, orgID uuid.UUID, a Action) (Membership, error) {
	m, err := Resolve(ctx, q, orgID)
	if err != nil {
		return Membership{}, err
	}
	if !m.Can(a) {
		return Membership{}, apperr.Forbidden().WithDetails(map[string]any{"action": string(a)})
	}
	return m, nil
}
