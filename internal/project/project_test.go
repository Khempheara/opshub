package project

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

var pageAll = pagination.Params{Limit: 100}

func names(p pagination.Page[Project]) []string {
	out := []string{}
	for _, it := range p.Items {
		out = append(out, it.Slug)
	}
	return out
}

func TestCreateAndVisibility(t *testing.T) {
	e := newEnv(t)

	// A Developer can create a project and becomes its Admin.
	p, err := e.svc.Create(e.dev.ctx, e.orgID, CreateInput{Name: "Payments API · API ទូទាត់ប្រាក់", Slug: "payments"})
	require.NoError(t, err)
	assert.Equal(t, authz.Admin, p.Role)
	assert.Equal(t, "main", p.DefaultBranch)
	assert.Contains(t, p.Actions, authz.RepoConnect)

	// Khmer-only names get a generated URL name.
	km, err := e.svc.Create(e.owner.ctx, e.orgID, CreateInput{Name: "គម្រោងថ្មី"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(km.Slug, "project-"), km.Slug)
	assert.Equal(t, authz.Owner, km.Role, "org Owners inherit Owner")

	_, err = e.svc.Create(e.admin.ctx, e.orgID, CreateInput{Name: "Dup", Slug: "payments"})
	assert.Equal(t, apperr.CodeSlugTaken, codeOf(t, err))
	_, err = e.svc.Create(e.viewer.ctx, e.orgID, CreateInput{Name: "Nope"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.Create(e.outsider.ctx, e.orgID, CreateInput{Name: "Nope"})
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
	_, err = e.svc.Create(e.owner.ctx, e.orgID, CreateInput{Name: "Bad", DefaultBranch: "has space"})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))

	// Owners, Admins and Viewers see every project; Developers only granted ones.
	for _, u := range []user{e.owner, e.admin, e.viewer} {
		list, err := e.svc.List(u.ctx, e.orgID, "", pageAll)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"payments", km.Slug}, names(list))
	}
	list, err := e.svc.List(e.dev.ctx, e.orgID, "", pageAll)
	require.NoError(t, err)
	assert.Equal(t, []string{"payments"}, names(list))
	list, err = e.svc.List(e.dev2.ctx, e.orgID, "", pageAll)
	require.NoError(t, err)
	assert.Empty(t, list.Items)
	list, err = e.svc.List(e.owner.ctx, e.orgID, "PAY", pageAll)
	require.NoError(t, err)
	assert.Equal(t, []string{"payments"}, names(list), "search is case-insensitive")
	list, err = e.svc.List(e.owner.ctx, e.orgID, "%", pageAll)
	require.NoError(t, err)
	assert.Empty(t, list.Items, "LIKE wildcards are escaped")
	_, err = e.svc.List(e.outsider.ctx, e.orgID, "", pageAll)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	// Hidden projects look like missing ones.
	_, err = e.svc.Get(e.dev2.ctx, p.ID)
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))
	_, err = e.svc.Get(e.outsider.ctx, p.ID)
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))
	got, err := e.svc.Get(e.viewer.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, authz.Viewer, got.Role)
	assert.Equal(t, []authz.Action{authz.ProjectView, authz.RunView}, got.Actions)
}

func TestUpdateAndDelete(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, e.admin)

	_, err := e.svc.Update(e.viewer.ctx, p.ID, p.Version, UpdateInput{Name: "X", DefaultBranch: "main"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	up, err := e.svc.Update(e.admin.ctx, p.ID, p.Version, UpdateInput{Name: "Payments", Description: "Card processing", DefaultBranch: "release/v2"})
	require.NoError(t, err)
	assert.Equal(t, p.Version+1, up.Version)
	assert.Equal(t, "release/v2", up.DefaultBranch)
	_, err = e.svc.Update(e.admin.ctx, p.ID, p.Version, UpdateInput{Name: "Stale", DefaultBranch: "main"})
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))

	assert.Equal(t, apperr.CodeConfirmationMismatch, codeOf(t, e.svc.Delete(e.admin.ctx, p.ID, "wrong")))
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, e.svc.Delete(e.viewer.ctx, p.ID, p.Slug)))
	require.NoError(t, e.svc.Delete(e.admin.ctx, p.ID, p.Slug))
	_, err = e.svc.Get(e.owner.ctx, p.ID)
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))

	// The URL name is free again.
	_, err = e.svc.Create(e.owner.ctx, e.orgID, CreateInput{Name: "Again", Slug: p.Slug})
	require.NoError(t, err)
}

func TestGrantsAndEffectiveRoles(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, e.owner)
	ctx := e.owner.ctx

	// A team grant gives dev2 access.
	team := e.team(t, e.dev2)
	require.NoError(t, e.svc.Grant(ctx, p.ID, teamP(team), authz.Developer))
	got, err := e.svc.Get(e.dev2.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, authz.Developer, got.Role)
	_, err = e.svc.Update(e.dev2.ctx, p.ID, p.Version, UpdateInput{Name: "X", DefaultBranch: "main"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	// A direct grant can raise it; the highest role wins.
	require.NoError(t, e.svc.Grant(ctx, p.ID, userP(e.dev2), authz.Admin))
	got, _ = e.svc.Get(e.dev2.ctx, p.ID)
	assert.Equal(t, authz.Admin, got.Role)
	// Grants never lower an inherited role; an org Viewer can be raised per project.
	require.NoError(t, e.svc.Grant(ctx, p.ID, userP(e.admin), authz.Viewer))
	got, _ = e.svc.Get(e.admin.ctx, p.ID)
	assert.Equal(t, authz.Admin, got.Role)
	require.NoError(t, e.svc.Grant(ctx, p.ID, userP(e.viewer), authz.Developer))
	got, _ = e.svc.Get(e.viewer.ctx, p.ID)
	assert.Equal(t, authz.Developer, got.Role)

	members, err := e.svc.ListMembers(e.viewer.ctx, p.ID)
	require.NoError(t, err)
	bySource := map[string][]string{}
	for _, m := range members {
		bySource[m.Source] = append(bySource[m.Source], m.Principal)
	}
	assert.ElementsMatch(t, []string{userP(e.owner), userP(e.admin)}, bySource[SourceOrganization])
	assert.ElementsMatch(t, []string{userP(e.dev2), userP(e.admin), userP(e.viewer)}, bySource[SourceDirect])
	assert.Equal(t, []string{teamP(team)}, bySource[SourceTeam])

	// Rules.
	assert.Equal(t, apperr.CodeRoleNotAllowed, codeOf(t, e.svc.Grant(ctx, p.ID, userP(e.dev), authz.Owner)))
	assert.Equal(t, apperr.CodeMemberNotFound, codeOf(t, e.svc.Grant(ctx, p.ID, userP(e.outsider), authz.Viewer)))
	assert.Equal(t, apperr.CodeMemberNotFound, codeOf(t, e.svc.Grant(ctx, p.ID, "robot:1", authz.Viewer)))
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, e.svc.Grant(e.viewer.ctx, p.ID, userP(e.dev), authz.Viewer)),
		"a project Developer can't manage members")
	assert.Equal(t, apperr.CodeMemberNotFound, codeOf(t, e.svc.Revoke(ctx, p.ID, userP(e.dev))))

	// Revoking the direct grant falls back to the team grant.
	require.NoError(t, e.svc.Revoke(ctx, p.ID, userP(e.dev2)))
	got, _ = e.svc.Get(e.dev2.ctx, p.ID)
	assert.Equal(t, authz.Developer, got.Role)

	// Deleting the team removes its grants.
	require.NoError(t, e.orgs.DeleteTeam(ctx, team))
	_, err = e.svc.Get(e.dev2.ctx, p.ID)
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))

	// Leaving the organization removes direct grants (they don't come back on rejoin).
	require.NoError(t, e.orgs.RemoveMember(ctx, e.orgID, e.viewer.id))
	_, err = e.svc.Get(e.viewer.ctx, p.ID)
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))
	members, _ = e.svc.ListMembers(ctx, p.ID)
	for _, m := range members {
		assert.NotEqual(t, userP(e.viewer), m.Principal)
	}
}

func TestEnvironments(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, e.admin)
	ctx := e.admin.ctx

	prod, err := e.svc.CreateEnvironment(ctx, p.ID, EnvironmentInput{
		Name: "production", Kind: "production", Variables: map[string]string{"LOG_LEVEL": "warn"},
		Protection: &Protection{RequiredApprovals: 2, AllowedBranches: []string{"main", "release/*"}, AllowedRoles: []authz.Role{authz.Admin, authz.Owner, authz.Admin}},
	})
	require.NoError(t, err)
	require.NotNil(t, prod.Protection)
	assert.Equal(t, int32(2), prod.Protection.RequiredApprovals)
	assert.Equal(t, []authz.Role{authz.Admin, authz.Owner}, prod.Protection.AllowedRoles, "duplicates dropped")
	_, err = e.svc.CreateEnvironment(ctx, p.ID, EnvironmentInput{Name: "dev", Kind: "development"})
	require.NoError(t, err)
	_, err = e.svc.CreateEnvironment(ctx, p.ID, EnvironmentInput{Name: "staging", Kind: "staging"})
	require.NoError(t, err)

	list, err := e.svc.ListEnvironments(e.viewer.ctx, p.ID)
	require.NoError(t, err)
	var order []string
	for _, env := range list {
		order = append(order, env.Name)
	}
	assert.Equal(t, []string{"dev", "staging", "production"}, order)
	assert.Nil(t, list[0].Protection)
	assert.Equal(t, map[string]string{}, list[0].Variables)

	_, err = e.svc.CreateEnvironment(ctx, p.ID, EnvironmentInput{Name: "production", Kind: "production"})
	assert.Equal(t, apperr.CodeEnvironmentNameTaken, codeOf(t, err))
	for _, bad := range []EnvironmentInput{
		{Name: "Prod", Kind: "production"},
		{Name: "qa", Kind: "testing"},
		{Name: "qa", Kind: "staging", Variables: map[string]string{"1BAD": "x"}},
		{Name: "qa", Kind: "staging", Variables: map[string]string{"BIG": strings.Repeat("x", MaxVariableValue+1)}},
		{Name: "qa", Kind: "staging", Protection: &Protection{RequiredApprovals: 11, AllowedRoles: []authz.Role{authz.Admin}}},
		{Name: "qa", Kind: "staging", Protection: &Protection{AllowedRoles: []authz.Role{authz.Viewer}}},
		{Name: "qa", Kind: "staging", Protection: &Protection{AllowedRoles: nil}},
		{Name: "qa", Kind: "staging", Protection: &Protection{AllowedBranches: []string{"has space"}, AllowedRoles: []authz.Role{authz.Admin}}},
	} {
		_, err := e.svc.CreateEnvironment(ctx, p.ID, bad)
		assert.Equal(t, apperr.CodeValidation, codeOf(t, err), "%+v", bad)
	}
	_, err = e.svc.CreateEnvironment(e.viewer.ctx, p.ID, EnvironmentInput{Name: "qa", Kind: "staging"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	// Update replaces kind, variables and protection; the name is fixed.
	up, err := e.svc.UpdateEnvironment(ctx, prod.ID, prod.Version, EnvironmentInput{
		Name: "ignored", Kind: "production", Variables: map[string]string{"LOG_LEVEL": "error", "REGION": "ap-southeast-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "production", up.Name)
	assert.Nil(t, up.Protection, "protection removed")
	assert.Equal(t, "ap-southeast-1", up.Variables["REGION"])
	_, err = e.svc.UpdateEnvironment(ctx, prod.ID, prod.Version, EnvironmentInput{Kind: "production"})
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))

	// Environments of hidden projects are not found.
	_, err = e.svc.GetEnvironment(e.dev2.ctx, prod.ID)
	assert.Equal(t, apperr.CodeEnvironmentNotFound, codeOf(t, err))
	_, err = e.svc.GetEnvironment(e.outsider.ctx, prod.ID)
	assert.Equal(t, apperr.CodeEnvironmentNotFound, codeOf(t, err))
	got, err := e.svc.GetEnvironment(e.viewer.ctx, prod.ID)
	require.NoError(t, err)
	assert.Equal(t, up.Version, got.Version)

	require.NoError(t, e.svc.DeleteEnvironment(ctx, prod.ID))
	_, err = e.svc.GetEnvironment(ctx, prod.ID)
	assert.Equal(t, apperr.CodeEnvironmentNotFound, codeOf(t, err))
	_, err = e.svc.CreateEnvironment(ctx, p.ID, EnvironmentInput{Name: "production", Kind: "production"})
	require.NoError(t, err, "the name is free after deletion")

	// At most MaxEnvironments per project.
	for i := len(list); i < MaxEnvironments; i++ {
		_, err := e.svc.CreateEnvironment(ctx, p.ID, EnvironmentInput{Name: "env-" + string(rune('a'+i)), Kind: "development"})
		require.NoError(t, err)
	}
	_, err = e.svc.CreateEnvironment(ctx, p.ID, EnvironmentInput{Name: "one-too-many", Kind: "development"})
	assert.Equal(t, apperr.CodeEnvironmentLimit, codeOf(t, err))
}

func TestConnectRepositoryAutomaticWebhook(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, e.admin)
	ctx := e.admin.ctx

	res, err := e.svc.ConnectRepository(ctx, p.ID, ConnectInput{Provider: "github", FullName: " acme/api/ ", AccessToken: goodToken})
	require.NoError(t, err)
	assert.Equal(t, "automatic", res.Webhook.Mode)
	assert.Empty(t, res.Webhook.Secret, "the secret isn't shown when OpsHub installed the hook")
	assert.Equal(t, "https://ops.example.com/api/v1/webhooks/github/"+res.Repository.ID.String(), res.Webhook.URL)
	assert.Equal(t, res.Webhook.URL, e.git.hooks["1"])
	assert.Len(t, e.git.lastSecret, 43, "32 random bytes, base64url")
	assert.Equal(t, "https://github.com/acme/api.git", res.Repository.CloneURL)

	// The token and secret are encrypted at rest.
	var tokenEnc, secretEnc []byte
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		"SELECT access_token_enc, webhook_secret_enc FROM repositories WHERE id = $1", res.Repository.ID).Scan(&tokenEnc, &secretEnc))
	assert.NotContains(t, string(tokenEnc), goodToken)
	assert.NotContains(t, string(secretEnc), e.git.lastSecret)

	got, err := e.svc.GetRepository(e.viewer.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, res.Repository.ID, got.ID)

	test, err := e.svc.TestRepository(ctx, p.ID)
	require.NoError(t, err)
	assert.True(t, test.CanManageWebhooks)
	assert.True(t, test.WebhookInstalled)

	// Reconnecting replaces the repository and removes the old hook.
	res2, err := e.svc.ConnectRepository(ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)
	assert.NotEqual(t, res.Repository.ID, res2.Repository.ID)
	assert.Equal(t, []string{"1"}, e.git.deleted)
	assert.Len(t, e.git.hooks, 1)

	require.NoError(t, e.svc.DisconnectRepository(ctx, p.ID))
	assert.Equal(t, []string{"1", "2"}, e.git.deleted)
	_, err = e.svc.GetRepository(ctx, p.ID)
	assert.Equal(t, apperr.CodeRepositoryNotFound, codeOf(t, err))
	assert.Equal(t, apperr.CodeRepositoryNotFound, codeOf(t, e.svc.DisconnectRepository(ctx, p.ID)))

	// Deleting a project removes its hook too.
	_, err = e.svc.ConnectRepository(ctx, p.ID, ConnectInput{Provider: "gitlab", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)
	require.NoError(t, e.svc.Delete(ctx, p.ID, p.Slug))
	assert.Empty(t, e.git.hooks)
}

func TestConnectRepositoryManualFallbackAndErrors(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, e.admin)
	ctx := e.admin.ctx

	// The token can't manage hooks: connected, with manual setup instructions.
	e.git.admin = false
	res, err := e.svc.ConnectRepository(ctx, p.ID, ConnectInput{Provider: "gitlab", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)
	assert.Equal(t, "manual", res.Webhook.Mode)
	assert.Equal(t, ManualNoPermission, res.Webhook.Reason)
	assert.NotEmpty(t, res.Webhook.Secret)
	assert.Empty(t, e.git.hooks)

	// The host refuses the hook (e.g. GitLab rejects a local URL).
	e.git.admin, e.git.hookStatus = true, http.StatusUnprocessableEntity
	res, err = e.svc.ConnectRepository(ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)
	assert.Equal(t, ManualRejected, res.Webhook.Reason)
	e.git.hookStatus = http.StatusBadGateway
	res, err = e.svc.ConnectRepository(ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)
	assert.Equal(t, ManualUnreachable, res.Webhook.Reason)
	e.git.hookStatus = 0

	for _, c := range []struct {
		in   ConnectInput
		code apperr.Code
	}{
		{ConnectInput{Provider: "github", FullName: "acme/missing", AccessToken: goodToken}, apperr.CodeGitRepoNotFound},
		{ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: "wrong"}, apperr.CodeGitAccessDenied},
		{ConnectInput{Provider: "github", FullName: "not-a-repo", AccessToken: goodToken}, apperr.CodeValidation},
		{ConnectInput{Provider: "github", BaseURL: "http://insecure.example.com", FullName: "a/b", AccessToken: goodToken}, apperr.CodeValidation},
		{ConnectInput{Provider: "gitlab", BaseURL: "https://gitlab.invalid", FullName: "a/b", AccessToken: goodToken}, apperr.CodeGitProviderUnreachable},
	} {
		_, err := e.svc.ConnectRepository(ctx, p.ID, c.in)
		assert.Equal(t, c.code, codeOf(t, err), "%+v", c.in)
	}
	_, err = e.svc.ConnectRepository(e.dev2.ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))
	_, err = e.svc.ConnectRepository(e.viewer.ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
}

func TestSSRFBlockedGitHost(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, e.admin)
	// The production client refuses internal addresses; this "cloud API" is on loopback.
	e.svc.git = gitprovider.Factory{HTTP: safehttp.NewClient(safehttp.Options{}), GitHubAPIURL: "http://127.0.0.1:9"}
	_, err := e.svc.ConnectRepository(e.admin.ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	assert.Equal(t, apperr.CodeSSRFBlocked, codeOf(t, err))
}

func TestWebhookReceiver(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, e.admin)
	e.git.admin = false // manual mode: we get the secret back
	res, err := e.svc.ConnectRepository(e.admin.ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)
	secret := []byte(res.Webhook.Secret)
	repoID := res.Repository.ID
	ctx := context.Background() // webhooks carry no user

	body := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef"}`)
	h := http.Header{}
	h.Set("X-GitHub-Event", "push")
	h.Set("X-GitHub-Delivery", "d-1")
	h.Set("X-Hub-Signature-256", gitprovider.SignGitHub(secret, body))

	out, err := e.svc.ReceiveWebhook(ctx, gitprovider.GitHub, repoID, h, body)
	require.NoError(t, err)
	assert.Equal(t, WebhookAccepted, out)
	out, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitHub, repoID, h, body)
	require.NoError(t, err)
	assert.Equal(t, WebhookDuplicate, out, "redelivery")
	// The accepted push (and only it) was handed to the pipeline worker.
	enqueued := e.jobs.all()
	require.Len(t, enqueued, 1)
	assert.Equal(t, jobs.PipelineFromEventArgs{RepositoryID: repoID, ProjectID: p.ID, Event: "push", Ref: "refs/heads/main", SHA: "0123456789abcdef"}, enqueued[0])

	bad := h.Clone()
	bad.Set("X-GitHub-Delivery", "d-2")
	bad.Set("X-Hub-Signature-256", gitprovider.SignGitHub([]byte("guess"), body))
	_, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitHub, repoID, bad, body)
	assert.Equal(t, apperr.CodeWebhookSignatureInvalid, codeOf(t, err))
	// A forged request can't block the real delivery with the same id.
	bad.Set("X-GitHub-Delivery", "d-3")
	_, _ = e.svc.ReceiveWebhook(ctx, gitprovider.GitHub, repoID, bad, body)
	good := h.Clone()
	good.Set("X-GitHub-Delivery", "d-3")
	out, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitHub, repoID, good, body)
	require.NoError(t, err)
	assert.Equal(t, WebhookAccepted, out)

	_, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitLab, repoID, h, body)
	assert.Equal(t, apperr.CodeNotFound, codeOf(t, err), "provider mismatch")
	_, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitHub, uuid.New(), h, body)
	assert.Equal(t, apperr.CodeNotFound, codeOf(t, err))
	_, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitHub, repoID, good, []byte(`not json`))
	assert.Equal(t, apperr.CodeWebhookSignatureInvalid, codeOf(t, err), "signature covers the body")

	page, err := e.svc.ListDeliveries(e.admin.ctx, p.ID, pageAll)
	require.NoError(t, err)
	require.Len(t, page.Items, 5)
	// Newest first: the tampered-body attempt, then the accepted d-3.
	assert.Equal(t, "d-3", page.Items[0].DeliveryID)
	assert.False(t, page.Items[0].SignatureValid)
	assert.Empty(t, page.Items[0].Ref, "nothing is trusted from unverified requests")
	assert.Equal(t, "d-3", page.Items[1].DeliveryID)
	assert.True(t, page.Items[1].SignatureValid)
	assert.Equal(t, "refs/heads/main", page.Items[1].Ref)
	assert.Equal(t, "0123456789abcdef", page.Items[1].CommitSHA)
	valid := 0
	for _, d := range page.Items {
		if d.SignatureValid {
			valid++
		}
	}
	assert.Equal(t, 2, valid)
	_, err = e.svc.ListDeliveries(e.viewer.ctx, p.ID, pageAll)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	repo, _ := e.svc.GetRepository(e.admin.ctx, p.ID)
	assert.NotNil(t, repo.LastDeliveryAt)

	// GitLab: token header, constant-time compare.
	p2 := e.project(t, e.admin)
	res2, err := e.svc.ConnectRepository(e.admin.ctx, p2.ID, ConnectInput{Provider: "gitlab", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)
	gl := http.Header{}
	gl.Set("X-Gitlab-Event", "Push Hook")
	gl.Set("X-Gitlab-Event-UUID", "u-1")
	gl.Set("X-Gitlab-Token", res2.Webhook.Secret)
	out, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitLab, res2.Repository.ID, gl, []byte(`{"ref":"refs/heads/main","checkout_sha":"abc"}`))
	require.NoError(t, err)
	assert.Equal(t, WebhookAccepted, out)
	gl.Set("X-Gitlab-Token", "nope")
	_, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitLab, res2.Repository.ID, gl, []byte(`{}`))
	assert.Equal(t, apperr.CodeWebhookSignatureInvalid, codeOf(t, err))

	// Deleted projects stop accepting webhooks.
	require.NoError(t, e.svc.Delete(e.admin.ctx, p.ID, p.Slug))
	h.Set("X-GitHub-Delivery", "d-9")
	_, err = e.svc.ReceiveWebhook(ctx, gitprovider.GitHub, repoID, h, body)
	assert.Equal(t, apperr.CodeNotFound, codeOf(t, err))
}

func TestAuditTrail(t *testing.T) {
	e := newEnv(t)
	p := e.project(t, e.admin)
	_, err := e.svc.CreateEnvironment(e.admin.ctx, p.ID, EnvironmentInput{Name: "prod", Kind: "production", Variables: map[string]string{"TOKENISH": "value-not-logged"}})
	require.NoError(t, err)
	require.NoError(t, e.svc.Grant(e.admin.ctx, p.ID, userP(e.dev), authz.Developer))
	_, err = e.svc.ConnectRepository(e.admin.ctx, p.ID, ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: goodToken})
	require.NoError(t, err)

	rows, err := pgtest.Pool(t).Query(context.Background(),
		`SELECT action, coalesce(after::text, '') FROM audit_log WHERE metadata->>'project_id' = $1 ORDER BY id`, p.ID.String())
	require.NoError(t, err)
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, after string
		require.NoError(t, rows.Scan(&action, &after))
		actions = append(actions, action)
		assert.NotContains(t, after, "value-not-logged", "variable values stay out of the audit log")
		assert.NotContains(t, after, goodToken, "tokens stay out of the audit log")
	}
	assert.Equal(t, []string{"project.create", "environment.create", "project.grant", "repo.connect"}, actions)
}
