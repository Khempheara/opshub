package auditlog

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

func pg(limit int, cursor string) pagination.Params {
	r, _ := pagination.Parse(httptest.NewRequest(http.MethodGet, "/?limit="+strconv.Itoa(limit)+"&cursor="+url.QueryEscape(cursor), nil))
	return r
}

func TestListAndFilters(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	shop := e.project(t, "Shop")
	team := uuid.New()
	e.record(t, e.admin.ctx, audit.Entry{OrganizationID: &e.orgID, Action: "team.create", ResourceType: "team", ResourceID: team.String(),
		After: map[string]any{"name": "Release crew"}})
	e.record(t, e.dev.ctx, audit.Entry{OrganizationID: &e.orgID, ProjectID: &shop, Action: "secret.create", ResourceType: "secret",
		ResourceID: uuid.NewString(), Metadata: map[string]any{"name": "DB_PASSWORD"}})
	e.record(t, ctx, audit.Entry{OrganizationID: &e.orgID, ProjectID: &shop, Action: "deployment.failed", ResourceType: "deployment",
		ResourceID: uuid.NewString(), ActorType: "system", After: map[string]any{"number": 4, "version": "shop:2"}})
	e.record(t, e.admin.ctx, audit.Entry{OrganizationID: &e.orgID, Action: "member.update_role", ResourceType: "member",
		ResourceID: e.dev.id.String(), Before: map[string]any{"role": "viewer"}, After: map[string]any{"role": "developer"}})
	// Another organization's entry never shows.
	other := newEnv(t)
	e.record(t, other.owner.ctx, audit.Entry{OrganizationID: &other.orgID, Action: "team.create", ResourceType: "team", After: map[string]any{"name": "Elsewhere"}})

	list := func(u user, f Filter, loc string) []string {
		t.Helper()
		p, err := e.svc.List(u.ctx, e.orgID, f, pg(50, ""), loc)
		require.NoError(t, err)
		return summaries(p.Items)
	}
	all := list(e.owner, Filter{}, "en")
	require.GreaterOrEqual(t, len(all), 5)
	assert.Equal(t, []string{
		"Admin Dara changed Dev Sophea's role from Viewer to Developer",
		"Deployment #4 of Shop (shop:2) failed",
		"Dev Sophea added the secret DB_PASSWORD to Shop",
		"Admin Dara created the team Release crew",
	}, all[:4], "newest first")
	assert.Equal(t, "Owner Sok created the organization Audit Co", all[len(all)-1])
	assert.NotContains(t, strings.Join(all, "\n"), "Elsewhere")

	assert.Equal(t, []string{"Dev Sophea added the secret DB_PASSWORD to Shop"}, list(e.admin, Filter{Areas: []string{"secret"}}, "en"))
	assert.Len(t, list(e.admin, Filter{Areas: []string{"secret", "team"}}, "en"), 2)
	assert.Len(t, list(e.admin, Filter{Actions: []string{"deployment.failed"}, Areas: []string{"team"}}, "en"), 2)
	assert.Len(t, list(e.admin, Filter{Project: shop.String()}, "en"), 2)
	assert.Equal(t, []string{"Dev Sophea added the secret DB_PASSWORD to Shop"}, list(e.admin, Filter{Actor: e.dev.id.String()}, "en"))
	assert.Len(t, list(e.admin, Filter{ResourceType: "member", ResourceID: e.dev.id.String()}, "en"), 1)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	assert.Empty(t, list(e.admin, Filter{From: future}, "en"))
	assert.Len(t, list(e.admin, Filter{To: future}, "en"), len(all))
	assert.Equal(t, "Admin Dara បានបង្កើតក្រុម Release crew", list(e.admin, Filter{Areas: []string{"team"}}, "km")[0])

	// Entry details.
	p, err := e.svc.List(e.admin.ctx, e.orgID, Filter{Areas: []string{"secret"}}, pg(50, ""), "en")
	require.NoError(t, err)
	s := p.Items[0]
	assert.Equal(t, "secret", s.Area)
	assert.Equal(t, "user", s.Actor.Type)
	assert.Equal(t, e.dev.name, *s.Actor.Name)
	assert.Equal(t, e.dev.email, *s.Actor.Email)
	require.NotNil(t, s.Project)
	assert.Equal(t, shop.String(), s.Project.ID)
	assert.Equal(t, "Shop", *s.Project.Name)
	assert.Nil(t, s.Before)
	assert.JSONEq(t, `{"name":"DB_PASSWORD","project_id":"`+shop.String()+`"}`, string(s.Metadata))
	p, err = e.svc.List(e.admin.ctx, e.orgID, Filter{Areas: []string{"deployment"}}, pg(50, ""), "en")
	require.NoError(t, err)
	assert.Equal(t, "system", p.Items[0].Actor.Type)
	assert.Nil(t, p.Items[0].Actor.Name)
	assert.Nil(t, p.Items[0].Actor.Email)

	// Paging.
	p1, err := e.svc.List(e.admin.ctx, e.orgID, Filter{}, pg(2, ""), "en")
	require.NoError(t, err)
	require.Len(t, p1.Items, 2)
	require.NotNil(t, p1.NextCursor)
	p2, err := e.svc.List(e.admin.ctx, e.orgID, Filter{}, pg(2, *p1.NextCursor), "en")
	require.NoError(t, err)
	assert.Equal(t, all[2:4], summaries(p2.Items))
	_, err = e.svc.List(e.admin.ctx, e.orgID, Filter{}, pg(2, "!!"), "en")
	assert.Equal(t, []string{"cursor:cursor"}, fieldsOf(t, err))

	// Validation and permissions.
	_, err = e.svc.List(e.admin.ctx, e.orgID, Filter{Areas: []string{"Bad Area"}, Actions: []string{"nodot"}, Actor: "x", Project: "y",
		From: "yesterday", To: "z"}, pg(50, ""), "en")
	assert.ElementsMatch(t, []string{"area:pattern", "action:pattern", "actor:uuid", "project:uuid", "from:datetime", "to:datetime"}, fieldsOf(t, err))
	_, err = e.svc.List(e.admin.ctx, e.orgID, Filter{From: future, To: future}, pg(50, ""), "en")
	assert.Equal(t, []string{"to:after_from"}, fieldsOf(t, err))
	_, err = e.svc.List(e.admin.ctx, e.orgID, Filter{Areas: make([]string, 31)}, pg(50, ""), "en")
	assert.Contains(t, fieldsOf(t, err), "area:max")
	for _, u := range []user{e.dev, e.viewer} {
		_, err = e.svc.List(u.ctx, e.orgID, Filter{}, pg(50, ""), "en")
		assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	}
	_, err = e.svc.List(e.outsider.ctx, e.orgID, Filter{}, pg(50, ""), "en")
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
}

func TestNamesResolvedAtReadTime(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var teamID, assetID uuid.UUID
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO teams (organization_id, slug, name) VALUES ($1, 'ops', 'Ops Team') RETURNING id`, e.orgID).Scan(&teamID))
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO infra_assets (organization_id, kind, name) VALUES ($1, 'server', 'web-1') RETURNING id`, e.orgID).Scan(&assetID))
	e.record(t, e.admin.ctx, audit.Entry{OrganizationID: &e.orgID, Action: "team.remove_member", ResourceType: "team", ResourceID: teamID.String(),
		Before: map[string]any{"user_id": e.viewer.id.String()}})
	e.record(t, e.admin.ctx, audit.Entry{OrganizationID: &e.orgID, Action: "asset.agent_token", ResourceType: "asset", ResourceID: assetID.String()})
	p, err := e.svc.List(e.admin.ctx, e.orgID, Filter{Areas: []string{"team", "asset"}}, pg(50, ""), "en")
	require.NoError(t, err)
	assert.Equal(t, []string{"Admin Dara issued a new agent token for web-1", "Admin Dara removed Viewer Vanna from the team Ops Team"}, summaries(p.Items))
}

func TestAccountActivity(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	me := e.dev
	e.record(t, ctx, audit.Entry{Action: "auth.login_failed", ResourceType: "user", ResourceID: me.id.String(), ActorUserID: &me.id,
		Metadata: map[string]any{"reason": "bad_password"}})
	e.record(t, ctx, audit.Entry{Action: "auth.login", ResourceType: "user", ResourceID: me.id.String(), ActorUserID: &me.id,
		Metadata: map[string]any{"method": "password"}})
	e.record(t, me.ctx, audit.Entry{Action: "token.create", ResourceType: "api_token", ResourceID: uuid.NewString(), After: map[string]any{"name": "ci"}})
	// Someone else's account event and an organization event of mine stay out.
	e.record(t, ctx, audit.Entry{Action: "auth.login", ResourceType: "user", ResourceID: e.admin.id.String(), ActorUserID: &e.admin.id})
	e.record(t, me.ctx, audit.Entry{OrganizationID: &e.orgID, Action: "team.create", ResourceType: "team", After: map[string]any{"name": "x"}})

	p, err := e.svc.Activity(me.ctx, pg(2, ""), "en")
	require.NoError(t, err)
	assert.Equal(t, []string{"Dev Sophea created the API token ci", "Dev Sophea signed in"}, summaries(p.Items))
	require.NotNil(t, p.NextCursor)
	p, err = e.svc.Activity(me.ctx, pg(2, *p.NextCursor), "km")
	require.NoError(t, err)
	assert.Equal(t, []string{"ការចូលគណនី Dev Sophea បរាជ័យ៖ ពាក្យសម្ងាត់ខុស"}, summaries(p.Items))
	assert.Nil(t, p.NextCursor)

	_, err = e.svc.Activity(context.Background(), pg(2, ""), "en")
	assert.Equal(t, apperr.CodeUnauthenticated, codeOf(t, err))
	_, err = e.svc.Activity(me.ctx, pg(2, "!!"), "en")
	assert.Equal(t, []string{"cursor:cursor"}, fieldsOf(t, err))
}

func TestExport(t *testing.T) {
	e := newEnv(t)
	shop := e.project(t, "ហាង Shop")
	e.record(t, e.dev.ctx, audit.Entry{OrganizationID: &e.orgID, ProjectID: &shop, Action: "secret.create", ResourceType: "secret",
		ResourceID: uuid.NewString(), Metadata: map[string]any{"name": "=HYPERLINK(\"http://x\")"}})
	e.record(t, e.admin.ctx, audit.Entry{OrganizationID: &e.orgID, Action: "team.update", ResourceType: "team",
		Before: map[string]any{"name": "QA"}, After: map[string]any{"name": "ក្រុម QA"}})
	evil := newUser(t, "=cmd|' /C calc'!A0")
	require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: e.orgID, UserID: evil.id, Role: authz.Viewer}))
	e.record(t, evil.ctx, audit.Entry{OrganizationID: &e.orgID, Action: "member.leave", ResourceType: "member", ResourceID: evil.id.String()})

	for _, u := range []user{e.dev, e.viewer} {
		_, err := e.svc.PrepareExport(u.ctx, e.orgID, Filter{}, "en")
		assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	}
	_, err := e.svc.PrepareExport(e.dev.ctx, e.orgID, Filter{Areas: []string{"BAD"}}, "en")
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err), "permission is checked before the filter")
	_, err = e.svc.PrepareExport(e.admin.ctx, e.orgID, Filter{Areas: []string{"BAD"}}, "en")
	assert.Equal(t, []string{"area:pattern"}, fieldsOf(t, err))
	_, err = e.svc.PrepareExport(e.outsider.ctx, e.orgID, Filter{}, "en")
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	exp, err := e.svc.PrepareExport(e.admin.ctx, e.orgID, Filter{Areas: []string{"secret", "team", "audit", "member"}}, "km")
	require.NoError(t, err)
	assert.Equal(t, "audit-log-"+e.slug+"-"+time.Now().UTC().Format("20060102")+".csv", exp.Filename)
	var buf bytes.Buffer
	require.NoError(t, exp.Write(&buf))
	out := buf.Bytes()
	require.True(t, bytes.HasPrefix(out, []byte{0xEF, 0xBB, 0xBF}), "UTF-8 BOM")
	recs, err := csv.NewReader(bytes.NewReader(out[3:])).ReadAll()
	require.NoError(t, err)
	assert.Equal(t, ExportHeader, recs[0])
	require.Len(t, recs, 5, "header, the export itself, member, team, secret")
	assert.Equal(t, "audit.export", recs[1][4])
	assert.Equal(t, "Admin Dara បាននាំចេញកំណត់ត្រាសវនកម្ម", recs[1][5])
	assert.Equal(t, "'=cmd|' /C calc'!A0", recs[2][2], "cells starting with a formula character are escaped")
	assert.Equal(t, "'=cmd|' /C calc'!A0 បានចាកចេញពីអង្គភាព", recs[2][5])
	assert.Equal(t, "team.update", recs[3][4])
	assert.Equal(t, "Admin Dara បានកែប្រែក្រុម ក្រុម QA", recs[3][5])
	assert.JSONEq(t, `{"name":"QA"}`, recs[3][12])
	assert.JSONEq(t, `{"name":"ក្រុម QA"}`, recs[3][13])
	sec := recs[4]
	assert.Equal(t, "Dev Sophea", sec[2])
	assert.Equal(t, e.dev.email, sec[3])
	assert.Equal(t, "Dev Sophea បានបន្ថែម Secret =HYPERLINK(\"http://x\") ទៅ ហាង Shop", sec[5], "text inside a cell is left alone")
	assert.Equal(t, shop.String(), sec[8])
	assert.Equal(t, "ហាង Shop", sec[9])
	_, err = time.Parse(time.RFC3339Nano, sec[0])
	assert.NoError(t, err)

	// The export is recorded with its filter.
	p, err := e.svc.List(e.owner.ctx, e.orgID, Filter{Actions: []string{"audit.export"}}, pg(50, ""), "en")
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	assert.Equal(t, "Admin Dara exported the audit log", p.Items[0].Summary)
	assert.JSONEq(t, `{"filter":{"area":["secret","team","audit","member"]},"locale":"km"}`, string(p.Items[0].Metadata))
}

func TestExportStreamsInBatches(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.pool.Exec(ctx, `INSERT INTO audit_log (organization_id, actor_type, action, resource_type, metadata, created_at)
		SELECT $1, 'system', 'monitor.create', 'monitor', jsonb_build_object('name', 'm' || g), now() - g * interval '1 second'
		FROM generate_series(1, 1203) g`, e.orgID)
	require.NoError(t, err)
	exp, err := e.svc.PrepareExport(e.owner.ctx, e.orgID, Filter{Areas: []string{"monitor"}}, "en")
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, exp.Write(&buf))
	recs, err := csv.NewReader(bytes.NewReader(buf.Bytes()[3:])).ReadAll()
	require.NoError(t, err)
	require.Len(t, recs, 1204)
	assert.Equal(t, "OpsHub added the monitor m1", recs[1][5])
	assert.Equal(t, "OpsHub added the monitor m1203", recs[1203][5])
}

func TestLocale(t *testing.T) {
	assert.Equal(t, "km", Locale(context.Background(), "km-KH"))
	assert.Equal(t, "en", Locale(context.Background(), ""))
	assert.Equal(t, "en", Locale(context.Background(), "fr"))
}
