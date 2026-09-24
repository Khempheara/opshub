package org

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

// fakeJobs records enqueued emails.
type fakeJobs struct {
	mu     sync.Mutex
	emails []jobs.SendEmailArgs
}

func (f *fakeJobs) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := args.(jobs.SendEmailArgs); ok {
		f.emails = append(f.emails, e)
	}
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

func (f *fakeJobs) lastTo(addr string) (jobs.SendEmailArgs, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.emails) - 1; i >= 0; i-- {
		if f.emails[i].To == addr {
			return f.emails[i], true
		}
	}
	return jobs.SendEmailArgs{}, false
}

func inviteToken(t *testing.T, e jobs.SendEmailArgs) string {
	t.Helper()
	link, _ := e.Data["URL"].(string)
	_, frag, ok := strings.Cut(link, "#token=")
	require.True(t, ok, link)
	tok, err := url.QueryUnescape(frag)
	require.NoError(t, err)
	return tok
}

type user struct {
	id    uuid.UUID
	email string
	ctx   context.Context
}

// newUser creates a verified account and returns a context authenticated as it.
func newUser(t *testing.T) user {
	t.Helper()
	now := time.Now()
	email := "u-" + uuid.NewString()[:8] + "@example.com"
	u, err := store.New(pgtest.Pool(t)).CreateUser(context.Background(), store.CreateUserParams{
		Email: email, DisplayName: "User " + email[:10], Locale: "km", Timezone: "UTC", EmailVerifiedAt: &now,
	})
	require.NoError(t, err)
	ctx := authn.WithPrincipal(context.Background(), authn.Principal{Kind: authn.KindSession, UserID: u.ID, SessionID: uuid.New()})
	return user{id: u.ID, email: email, ctx: ctx}
}

func codeOf(t *testing.T, err error) apperr.Code {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.From(err)
	require.True(t, ok, "expected apperr, got %v", err)
	return ae.Code
}

func newService(t *testing.T) (*Service, *fakeJobs) {
	t.Helper()
	fj := &fakeJobs{}
	return NewService(pgtest.Pool(t), fj, Config{PublicURL: "https://ops.example.com"}), fj
}

// orgWith creates an organization owned by owner, with the other users added at the given roles.
func orgWith(t *testing.T, svc *Service, owner user, members map[*user]authz.Role) Organization {
	t.Helper()
	o, err := svc.Create(owner.ctx, CreateInput{Name: "Org", Slug: "org-" + uuid.NewString()[:8]})
	require.NoError(t, err)
	q := store.New(pgtest.Pool(t))
	for u, role := range members {
		require.NoError(t, q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	return o
}

func TestCreateListGetUpdateDelete(t *testing.T) {
	svc, _ := newService(t)
	alice := newUser(t)
	slug := "mekong-" + uuid.NewString()[:8]

	o, err := svc.Create(alice.ctx, CreateInput{Name: "Mekong Labs", Slug: slug})
	require.NoError(t, err)
	assert.Equal(t, "owner", o.Role)
	_, err = svc.Create(alice.ctx, CreateInput{Name: "Again", Slug: slug})
	assert.Equal(t, apperr.CodeSlugTaken, codeOf(t, err))
	_, err = svc.Create(alice.ctx, CreateInput{Name: "អង្គរ"})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err), "Khmer-only names need an explicit slug")

	page, err := svc.ListMine(alice.ctx, pagination.Params{Limit: 50})
	require.NoError(t, err)
	assert.NotEmpty(t, page.Items)

	updated, err := svc.Update(alice.ctx, o.ID, o.Version, UpdateInput{Name: "Mekong Cloud · មេគង្គ"})
	require.NoError(t, err)
	assert.Equal(t, "Mekong Cloud · មេគង្គ", updated.Name)
	_, err = svc.Update(alice.ctx, o.ID, o.Version, UpdateInput{Name: "Stale"})
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))

	assert.Equal(t, apperr.CodeConfirmationMismatch, codeOf(t, svc.Delete(alice.ctx, o.ID, "wrong")))
	require.NoError(t, svc.Delete(alice.ctx, o.ID, slug))
	_, err = svc.Get(alice.ctx, o.ID)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err), "deleted organizations disappear")
	o2, err := svc.Create(alice.ctx, CreateInput{Name: "Reuse", Slug: slug})
	require.NoError(t, err, "the slug of a deleted organization can be reused")
	assert.NotEqual(t, o.ID, o2.ID)
}

func TestPermissionsPerRole(t *testing.T) {
	svc, _ := newService(t)
	owner, admin, dev, viewer, outsider := newUser(t), newUser(t), newUser(t), newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&admin: authz.Admin, &dev: authz.Developer, &viewer: authz.Viewer})

	for u, wantRole := range map[*user]string{&owner: "owner", &admin: "admin", &dev: "developer", &viewer: "viewer"} {
		p, err := svc.Permissions(u.ctx, o.ID)
		require.NoError(t, err)
		assert.Equal(t, wantRole, p.Role)
	}
	vp, _ := svc.Permissions(viewer.ctx, o.ID)
	assert.Contains(t, vp.Actions, "org.view")
	assert.NotContains(t, vp.Actions, "member.invite")

	_, err := svc.Permissions(outsider.ctx, o.ID)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	// Enforced in the service, not just advertised.
	_, err = svc.Update(viewer.ctx, o.ID, o.Version, UpdateInput{Name: "x"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = svc.Update(dev.ctx, o.ID, o.Version, UpdateInput{Name: "x"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, svc.Delete(admin.ctx, o.ID, "x")), "only owners delete")
	_, err = svc.Invite(dev.ctx, o.ID, InviteInput{Email: "x@example.com", Role: "viewer"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = svc.CreateTeam(viewer.ctx, o.ID, TeamInput{Name: "T"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
}

func TestMemberRoleRules(t *testing.T) {
	svc, _ := newService(t)
	owner, admin, admin2, dev, viewer := newUser(t), newUser(t), newUser(t), newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&admin: authz.Admin, &admin2: authz.Admin, &dev: authz.Developer, &viewer: authz.Viewer})

	m, err := svc.UpdateMemberRole(admin.ctx, o.ID, viewer.id, "developer")
	require.NoError(t, err)
	assert.Equal(t, "developer", m.Role)

	_, err = svc.UpdateMemberRole(admin.ctx, o.ID, dev.id, "admin")
	assert.Equal(t, apperr.CodeRoleNotAllowed, codeOf(t, err), "admins can't create admins")
	_, err = svc.UpdateMemberRole(admin.ctx, o.ID, admin2.id, "viewer")
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err), "admins can't change other admins")
	_, err = svc.UpdateMemberRole(admin.ctx, o.ID, owner.id, "viewer")
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err), "admins can't change owners")
	_, err = svc.UpdateMemberRole(dev.ctx, o.ID, viewer.id, "viewer")
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err), "developers can't change roles")
	_, err = svc.UpdateMemberRole(admin.ctx, o.ID, uuid.New(), "viewer")
	assert.Equal(t, apperr.CodeMemberNotFound, codeOf(t, err))
	_, err = svc.UpdateMemberRole(admin.ctx, o.ID, dev.id, "root")
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))

	_, err = svc.UpdateMemberRole(admin.ctx, o.ID, admin.id, "developer")
	require.NoError(t, err, "admins may step down")

	_, err = svc.UpdateMemberRole(owner.ctx, o.ID, owner.id, "admin")
	assert.Equal(t, apperr.CodeLastOwner, codeOf(t, err), "the last owner can't step down")
	_, err = svc.UpdateMemberRole(owner.ctx, o.ID, dev.id, "owner")
	require.NoError(t, err, "owners can promote to owner")
	_, err = svc.UpdateMemberRole(owner.ctx, o.ID, owner.id, "admin")
	require.NoError(t, err, "with a second owner the first may step down")
}

func TestRemoveAndLeave(t *testing.T) {
	svc, _ := newService(t)
	owner, admin, admin2, dev, viewer := newUser(t), newUser(t), newUser(t), newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&admin: authz.Admin, &admin2: authz.Admin, &dev: authz.Developer, &viewer: authz.Viewer})

	team, err := svc.CreateTeam(owner.ctx, o.ID, TeamInput{Name: "Platform"})
	require.NoError(t, err)
	require.NoError(t, svc.AddTeamMember(owner.ctx, team.ID, dev.id))

	assert.Equal(t, apperr.CodeForbidden, codeOf(t, svc.RemoveMember(dev.ctx, o.ID, viewer.id)))
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, svc.RemoveMember(admin.ctx, o.ID, admin2.id)))
	require.NoError(t, svc.RemoveMember(admin.ctx, o.ID, dev.id))
	_, members, err := svc.GetTeam(owner.ctx, team.ID)
	require.NoError(t, err)
	assert.Empty(t, members, "removing a member also removes them from the organization's teams")
	_, err = svc.Get(dev.ctx, o.ID)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err), "removed members lose access")

	require.NoError(t, svc.RemoveMember(viewer.ctx, o.ID, viewer.id), "anyone may leave")
	assert.Equal(t, apperr.CodeLastOwner, codeOf(t, svc.RemoveMember(owner.ctx, o.ID, owner.id)), "the last owner can't leave")
}

func TestTransferOwnership(t *testing.T) {
	svc, _ := newService(t)
	owner, admin := newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&admin: authz.Admin})

	assert.Equal(t, apperr.CodeForbidden, codeOf(t, svc.TransferOwnership(admin.ctx, o.ID, admin.id)))
	assert.Equal(t, apperr.CodeMemberNotFound, codeOf(t, svc.TransferOwnership(owner.ctx, o.ID, uuid.New())))
	require.NoError(t, svc.TransferOwnership(owner.ctx, o.ID, admin.id))

	p, _ := svc.Permissions(admin.ctx, o.ID)
	assert.Equal(t, "owner", p.Role)
	p, _ = svc.Permissions(owner.ctx, o.ID)
	assert.Equal(t, "admin", p.Role)
}

func TestInvitationLifecycle(t *testing.T) {
	svc, fj := newService(t)
	owner, admin := newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&admin: authz.Admin})
	invitee := newUser(t) // existing account with locale "km"

	_, err := svc.Invite(admin.ctx, o.ID, InviteInput{Email: invitee.email, Role: "admin"})
	assert.Equal(t, apperr.CodeRoleNotAllowed, codeOf(t, err), "admins invite at most developers")

	inv, err := svc.Invite(admin.ctx, o.ID, InviteInput{Email: strings.ToUpper(invitee.email), Role: "developer"})
	require.NoError(t, err)
	assert.Equal(t, invitee.email, inv.Email)
	first, ok := fj.lastTo(invitee.email)
	require.True(t, ok)
	assert.Equal(t, "km", first.Locale, "existing users get the email in their language")
	assert.Equal(t, "Developer", first.Data["Role"])

	// Re-inviting replaces the open invitation: the first link stops working.
	_, err = svc.Invite(admin.ctx, o.ID, InviteInput{Email: invitee.email, Role: "viewer"})
	require.NoError(t, err)
	_, err = svc.PreviewInvitation(invitee.ctx, inviteToken(t, first))
	assert.Equal(t, apperr.CodeInvalidToken, codeOf(t, err))
	second, _ := fj.lastTo(invitee.email)
	token := inviteToken(t, second)

	list, err := svc.ListInvitations(admin.ctx, o.ID, pagination.Params{Limit: 50})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "viewer", list.Items[0].Role)

	preview, err := svc.PreviewInvitation(invitee.ctx, token)
	require.NoError(t, err)
	assert.Equal(t, "viewer", preview.Role)

	stranger := newUser(t)
	_, err = svc.AcceptInvitation(stranger.ctx, token)
	assert.Equal(t, apperr.CodeInvitationEmailMismatch, codeOf(t, err), "a forwarded link can't be used by someone else")

	joined, err := svc.AcceptInvitation(invitee.ctx, token)
	require.NoError(t, err)
	assert.Equal(t, o.ID, joined.ID)
	assert.Equal(t, "viewer", joined.Role)
	_, err = svc.AcceptInvitation(invitee.ctx, token)
	assert.Equal(t, apperr.CodeInvalidToken, codeOf(t, err), "single use")

	_, err = svc.Invite(admin.ctx, o.ID, InviteInput{Email: invitee.email, Role: "viewer"})
	assert.Equal(t, apperr.CodeAlreadyMember, codeOf(t, err))
}

func TestInvitationRevokeAndExpiry(t *testing.T) {
	svc, fj := newService(t)
	owner := newUser(t)
	o := orgWith(t, svc, owner, nil)
	invitee := newUser(t)

	inv, err := svc.Invite(owner.ctx, o.ID, InviteInput{Email: invitee.email, Role: "developer"})
	require.NoError(t, err)
	msg, _ := fj.lastTo(invitee.email)

	outsider := newUser(t)
	assert.Equal(t, apperr.CodeInvitationNotFound, codeOf(t, svc.RevokeInvitation(outsider.ctx, inv.ID)), "other tenants can't see it")
	require.NoError(t, svc.RevokeInvitation(owner.ctx, inv.ID))
	assert.Equal(t, apperr.CodeInvitationNotFound, codeOf(t, svc.RevokeInvitation(owner.ctx, inv.ID)))
	_, err = svc.AcceptInvitation(invitee.ctx, inviteToken(t, msg))
	assert.Equal(t, apperr.CodeInvalidToken, codeOf(t, err), "revoked invitations can't be accepted")

	_, err = svc.Invite(owner.ctx, o.ID, InviteInput{Email: invitee.email, Role: "developer"})
	require.NoError(t, err)
	msg, _ = fj.lastTo(invitee.email)
	svc.now = func() time.Time { return time.Now().Add(-InvitationTTL - time.Hour) } // created in the past
	_, err = svc.Invite(owner.ctx, o.ID, InviteInput{Email: invitee.email, Role: "developer"})
	require.NoError(t, err)
	svc.now = time.Now
	expired, _ := fj.lastTo(invitee.email)
	_, err = svc.AcceptInvitation(invitee.ctx, inviteToken(t, expired))
	assert.Equal(t, apperr.CodeInvalidToken, codeOf(t, err), "expired")
	_ = msg
}

func TestTeams(t *testing.T) {
	svc, _ := newService(t)
	owner, dev, outsider := newUser(t), newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&dev: authz.Developer})

	team, err := svc.CreateTeam(owner.ctx, o.ID, TeamInput{Name: "Platform Team", Description: "ក្រុមវេទិកា"})
	require.NoError(t, err)
	assert.Equal(t, "platform-team", team.Slug)
	_, err = svc.CreateTeam(owner.ctx, o.ID, TeamInput{Name: "Platform Team"})
	assert.Equal(t, apperr.CodeSlugTaken, codeOf(t, err))

	require.NoError(t, svc.AddTeamMember(owner.ctx, team.ID, dev.id))
	require.NoError(t, svc.AddTeamMember(owner.ctx, team.ID, dev.id), "idempotent")
	assert.Equal(t, apperr.CodeMemberNotFound, codeOf(t, svc.AddTeamMember(owner.ctx, team.ID, outsider.id)), "only org members")

	got, members, err := svc.GetTeam(dev.ctx, team.ID)
	require.NoError(t, err, "members can view teams")
	assert.Equal(t, int64(1), got.MemberCount)
	require.Len(t, members, 1)

	page, err := svc.ListTeams(dev.ctx, o.ID, pagination.Params{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, int64(1), page.Items[0].MemberCount)

	_, err = svc.UpdateTeam(dev.ctx, team.ID, team.Version, TeamInput{Name: "x"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	updated, err := svc.UpdateTeam(owner.ctx, team.ID, team.Version, TeamInput{Name: "SRE", Description: "On call"})
	require.NoError(t, err)
	_, err = svc.UpdateTeam(owner.ctx, team.ID, team.Version, TeamInput{Name: "stale"})
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))
	assert.Equal(t, "SRE", updated.Name)

	_, _, err = svc.GetTeam(outsider.ctx, team.ID)
	assert.Equal(t, apperr.CodeTeamNotFound, codeOf(t, err), "other tenants get 404")

	require.NoError(t, svc.RemoveTeamMember(owner.ctx, team.ID, dev.id))
	assert.Equal(t, apperr.CodeMemberNotFound, codeOf(t, svc.RemoveTeamMember(owner.ctx, team.ID, dev.id)))
	require.NoError(t, svc.DeleteTeam(owner.ctx, team.ID))
	_, _, err = svc.GetTeam(owner.ctx, team.ID)
	assert.Equal(t, apperr.CodeTeamNotFound, codeOf(t, err))
}

func TestMemberListFilterAndSearch(t *testing.T) {
	svc, _ := newService(t)
	owner, dev, viewer := newUser(t), newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&dev: authz.Developer, &viewer: authz.Viewer})

	all, err := svc.ListMembers(viewer.ctx, o.ID, MemberFilter{}, pagination.Params{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, all.Items, 2)
	assert.NotNil(t, all.NextCursor)

	devs, err := svc.ListMembers(viewer.ctx, o.ID, MemberFilter{Role: "developer"}, pagination.Params{Limit: 50})
	require.NoError(t, err)
	require.Len(t, devs.Items, 1)
	assert.Equal(t, dev.email, devs.Items[0].Email)

	found, err := svc.ListMembers(viewer.ctx, o.ID, MemberFilter{Search: strings.ToUpper(viewer.email[:10])}, pagination.Params{Limit: 50})
	require.NoError(t, err)
	require.Len(t, found.Items, 1)
	none, err := svc.ListMembers(viewer.ctx, o.ID, MemberFilter{Search: "%"}, pagination.Params{Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, none.Items, "LIKE wildcards are matched literally")

	_, err = svc.ListMembers(viewer.ctx, o.ID, MemberFilter{Role: "root"}, pagination.Params{Limit: 50})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
}

func TestMutationsAreAudited(t *testing.T) {
	svc, _ := newService(t)
	owner, dev := newUser(t), newUser(t)
	o := orgWith(t, svc, owner, map[*user]authz.Role{&dev: authz.Developer})
	_, err := svc.UpdateMemberRole(owner.ctx, o.ID, dev.id, "viewer")
	require.NoError(t, err)

	rows, err := pgtest.Pool(t).Query(context.Background(),
		`SELECT action, before->>'role', after->>'role' FROM audit_log WHERE organization_id = $1 ORDER BY created_at, id`, o.ID)
	require.NoError(t, err)
	type entry struct {
		Action        string
		Before, After *string
	}
	entries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[entry])
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "org.create", entries[0].Action)
	assert.Equal(t, "member.update_role", entries[1].Action)
	assert.Equal(t, "developer", *entries[1].Before)
	assert.Equal(t, "viewer", *entries[1].After)
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Angkor Tech":         "angkor-tech",
		"  Mekong -- Labs!! ": "mekong-labs",
		"អង្គរ":               "",
		"Angkor Tech · អង្គរ": "angkor-tech",
		"A_B.C":               "a-b-c",
	} {
		assert.Equal(t, want, Slugify(in), in)
	}
}

var pageAll = pagination.Params{Limit: 200}

// A name written only in Khmer has no Latin letters to derive a URL name from.
func TestKhmerOnlyTeamNameGetsGeneratedSlug(t *testing.T) {
	svc, _ := newService(t)
	owner := newUser(t)
	o := orgWith(t, svc, owner, nil)
	team, err := svc.CreateTeam(owner.ctx, o.ID, TeamInput{Name: "ក្រុមវេទិកា"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(team.Slug, "team-"), team.Slug)
	assert.Equal(t, "ក្រុមវេទិកា", team.Name)
}
