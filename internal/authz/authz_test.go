package authz

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// The permission matrix from docs/rbac.md, organization scope. A change here must be
// mirrored in the docs (and vice versa): this table is the contract.
func TestOrgPermissionMatrix(t *testing.T) {
	type row struct{ owner, admin, developer, viewer bool }
	matrix := map[Action]row{
		OrgView:          {true, true, true, true},
		MemberView:       {true, true, true, true},
		TeamView:         {true, true, true, true},
		OrgUpdate:        {true, true, false, false},
		OrgDelete:        {true, false, false, false},
		OrgTransfer:      {true, false, false, false},
		MemberInvite:     {true, true, false, false},
		MemberRemove:     {true, true, false, false},
		MemberUpdateRole: {true, true, false, false},
		TeamManage:       {true, true, false, false},
		ProjectCreate:    {true, true, true, false},
		RunnerView:       {true, true, true, false},
		RunnerManage:     {true, true, false, false},
		TargetView:       {true, true, true, true},
		TargetManage:     {true, true, false, false},
		InfraView:        {true, true, true, true},
		InfraManage:      {true, true, true, false},
		MonitorView:      {true, true, true, true},
		MonitorManage:    {true, true, true, false},
		AlertAck:         {true, true, true, false},
		ChannelView:      {true, true, true, false},
		ChannelManage:    {true, true, false, false},
		AuditView:        {true, true, false, false},
		AuditExport:      {true, true, false, false},
		LogsView:         {true, true, true, true},
	}
	assert.Len(t, OrgActions(), len(matrix), "every action in the matrix is covered by this test")
	for action, want := range matrix {
		for role, allowed := range map[Role]bool{Owner: want.owner, Admin: want.admin, Developer: want.developer, Viewer: want.viewer} {
			assert.Equal(t, allowed, Can(role, action), "%s × %s", role, action)
		}
	}
	assert.False(t, Can(Owner, "unknown.action"), "unknown actions are denied")
	assert.False(t, Can("guest", OrgView), "unknown roles are denied")
}

func TestAllowedIsSortedAndConsistent(t *testing.T) {
	viewer := Allowed(Viewer)
	assert.Contains(t, viewer, OrgView)
	assert.NotContains(t, viewer, MemberInvite)
	assert.IsIncreasing(t, viewer)
	assert.Len(t, Allowed(Owner), len(OrgActions()), "owners can do everything")
}

func TestMemberManagementRules(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	owner := Membership{UserID: me, Role: Owner}
	admin := Membership{UserID: me, Role: Admin}

	assert.Equal(t, Owner, owner.MaxAssignableRole())
	assert.Equal(t, Developer, admin.MaxAssignableRole())

	assert.True(t, owner.CanManageMember(Owner, other))
	assert.True(t, admin.CanManageMember(Developer, other))
	assert.True(t, admin.CanManageMember(Viewer, other))
	assert.False(t, admin.CanManageMember(Admin, other), "admins can't change other admins")
	assert.False(t, admin.CanManageMember(Owner, other), "admins can't change owners")
	assert.True(t, admin.CanManageMember(Admin, me), "but can step down themselves")
}

func TestRoleOrdering(t *testing.T) {
	assert.True(t, AtLeast(Owner, Admin))
	assert.True(t, AtLeast(Developer, Developer))
	assert.False(t, AtLeast(Viewer, Developer))
	assert.True(t, ValidRole(Viewer))
	assert.False(t, ValidRole("root"))
}

// The project-scope matrix from docs/rbac.md (Module 3 actions).
func TestProjectPermissionMatrix(t *testing.T) {
	type row struct{ owner, admin, developer, viewer bool }
	matrix := map[Action]row{
		ProjectView:          {true, true, true, true},
		ProjectUpdate:        {true, true, false, false},
		ProjectDelete:        {true, true, false, false},
		ProjectManageMembers: {true, true, false, false},
		RepoConnect:          {true, true, false, false},
		EnvironmentManage:    {true, true, false, false},
		RunView:              {true, true, true, true},
		PipelineTrigger:      {true, true, true, false},
		RunCancel:            {true, true, true, false},
		ApprovalDecide:       {true, true, true, false},
		DeploymentView:       {true, true, true, true},
		DeploymentCreate:     {true, true, true, false},
		DeploymentRollback:   {true, true, true, false},
	}
	assert.ElementsMatch(t, ProjectActions(), func() []Action {
		var out []Action
		for a := range matrix {
			out = append(out, a)
		}
		return out
	}(), "every project action must be pinned here")
	for a, r := range matrix {
		assert.Equal(t, r.owner, CanProject(Owner, a), "owner %s", a)
		assert.Equal(t, r.admin, CanProject(Admin, a), "admin %s", a)
		assert.Equal(t, r.developer, CanProject(Developer, a), "developer %s", a)
		assert.Equal(t, r.viewer, CanProject(Viewer, a), "viewer %s", a)
	}
	assert.False(t, CanProject(Owner, OrgView), "org actions aren't project actions")
	assert.Equal(t, []Action{DeploymentView, ProjectView, RunView}, AllowedProject(Viewer))
}

// Effective project role = max(inherited org role, direct grant, team grant); docs/rbac.md.
func TestEffectiveProjectRole(t *testing.T) {
	cases := []struct {
		org, direct, team Role
		want              Role
		ok                bool
	}{
		{Owner, "", "", Owner, true},
		{Admin, "", "", Admin, true},
		{Viewer, "", "", Viewer, true},
		{Developer, "", "", "", false}, // developers need a grant
		{Developer, Developer, "", Developer, true},
		{Developer, "", Admin, Admin, true},     // via team
		{Developer, Viewer, Admin, Admin, true}, // highest wins
		{Viewer, Developer, "", Developer, true},
		{Admin, Viewer, Viewer, Admin, true}, // grants never lower the inherited role
		{"", Admin, Admin, "", false},        // not an org member: grants don't count
	}
	for _, c := range cases {
		got, ok := EffectiveProjectRole(c.org, c.direct, c.team)
		assert.Equal(t, c.ok, ok, "%v", c)
		assert.Equal(t, c.want, got, "%v", c)
	}
	assert.False(t, GrantableProjectRole(Owner))
	assert.True(t, GrantableProjectRole(Admin))
	assert.False(t, GrantableProjectRole("root"))
}
