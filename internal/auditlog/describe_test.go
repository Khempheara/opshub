package auditlog

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/i18n"
)

func bundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	b, err := i18n.NewBundle()
	require.NoError(t, err)
	return b
}

func ptr(s string) *string { return &s }

func raw(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Every sentence renders in both languages without missing template values, even when the
// entry carries none of the data it could use.
func TestEverySentenceRenders(t *testing.T) {
	b := bundle(t)
	var en map[string]string
	data, err := os.ReadFile("../i18n/locales/en.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &en))
	variants := map[string]string{
		"auth.login_failed.bad_password": "auth.login_failed", "auth.login_failed.bad_second_factor": "auth.login_failed",
		"approval.decide.approved": "approval.decide", "approval.decide.rejected": "approval.decide",
	}
	n := 0
	for key := range en {
		if !strings.HasPrefix(key, "audit.") || strings.HasPrefix(key, "audit.actor.") || strings.HasPrefix(key, "audit.role.") ||
			key == "audit.unnamed" || key == "audit.unknown" {
			continue
		}
		action := strings.TrimPrefix(key, "audit.")
		if base, ok := variants[action]; ok {
			action = base
		}
		for _, loc := range []string{"en", "km"} {
			s := describe(b, loc, row{ActorType: "user", ActorName: ptr("Dara"), Action: action, ResourceType: "x"}, names{})
			assert.NotContains(t, s, "<no value>", "%s %s", loc, key)
			assert.NotContains(t, s, "audit.", "%s %s", loc, key)
			if action != "runner.register" && action != "secret.read" && !strings.HasPrefix(action, "deployment.") && action != "auth.password_reset_requested" {
				assert.Contains(t, s, "Dara", "%s %s", loc, key)
			}
		}
		n++
	}
	assert.GreaterOrEqual(t, n, 89)
}

func TestDescribe(t *testing.T) {
	b := bundle(t)
	team, member, asset := uuid.New(), uuid.New(), uuid.New()
	n := names{
		teams:  map[uuid.UUID]string{team: "Platform"},
		users:  map[uuid.UUID]string{member: "Sophea"},
		assets: map[uuid.UUID]string{asset: "web-1"},
	}
	dara := row{ActorType: "user", ActorName: ptr("Dara"), ActorEmail: "dara@example.com"}
	with := func(r row, f func(*row)) row { f(&r); return r }
	cases := []struct {
		name, loc string
		r         row
		want      string
	}{
		{"log token", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType = "log_token.revoke", "log_ingest_token"
			r.Before = raw(map[string]any{"name": "Shop API", "service": "shop/api"})
		}), "Dara revoked the ingest token Shop API for shop/api"},
		{"khmer", "km", with(dara, func(r *row) {
			r.Action, r.ResourceType = "log_token.revoke", "log_ingest_token"
			r.Before = raw(map[string]any{"name": "Shop API", "service": "shop/api"})
		}), "Dara បានដកហូតថូខឹនបញ្ជូន Shop API សម្រាប់ shop/api"},
		{"role change", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType, r.ResourceUserName = "member.update_role", "member", ptr("Sophea")
			r.Before, r.After = raw(map[string]any{"role": "developer"}), raw(map[string]any{"role": "admin"})
		}), "Dara changed Sophea's role from Developer to Admin"},
		{"team member by id", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType, r.ResourceID = "team.remove_member", "team", ptr(team.String())
			r.Before = raw(map[string]any{"user_id": member.String()})
		}), "Dara removed Sophea from the team Platform"},
		{"project grant to team", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType, r.ResourceID, r.ProjectName = "project.grant", "team", ptr(team.String()), ptr("Payments API")
			r.After = raw(map[string]any{"role": "developer"})
		}), "Dara gave Platform Developer access to Payments API"},
		{"run", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType, r.ProjectName = "pipeline.trigger", "run", ptr("Payments API")
			r.After = raw(map[string]any{"number": 12, "ref": "refs/heads/main"})
		}), "Dara started run #12 of Payments API on main"},
		{"approval", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType, r.ProjectName = "approval.decide", "job", ptr("Payments API")
			r.After = raw(map[string]any{"job": "deploy-production", "decision": "rejected", "run_number": 3})
		}), "Dara rejected deploy-production in run #3 of Payments API"},
		{"system deployment", "en", row{
			ActorType: "system", Action: "deployment.failed", ResourceType: "deployment", ProjectName: ptr("Payments API"),
			After: raw(map[string]any{"number": 29, "version": "app:1.3.0"}),
		}, "Deployment #29 of Payments API (app:1.3.0) failed"},
		{"runner secret read", "en", row{
			ActorType: "runner", Action: "secret.read", ResourceType: "secret", ProjectName: ptr("Payments API"),
			Metadata: raw(map[string]any{"name": "DB_PASSWORD"}),
		}, "A runner received the secret DB_PASSWORD for a job of Payments API"},
		{"api token actor", "en", with(dara, func(r *row) {
			r.ActorType, r.Action, r.ResourceType = "api_token", "monitor.create", "monitor"
			r.After = raw(map[string]any{"name": "shop"})
		}), "Dara (API token) added the monitor shop"},
		{"deleted user", "en", row{ActorType: "user", Action: "team.create", ResourceType: "team", After: raw(map[string]any{"name": "QA"})},
			"A deleted user created the team QA"},
		{"failed login", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType = "auth.login_failed", "user"
			r.Metadata = raw(map[string]any{"reason": "bad_second_factor"})
		}), "Failed sign-in to Dara: wrong two-factor code"},
		{"unknown reason", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType = "auth.login_failed", "user"
			r.Metadata = raw(map[string]any{"reason": "new_reason"})
		}), "Failed sign-in to Dara"},
		{"sso", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType = "identity.link", "user"
			r.After = raw(map[string]any{"provider": "github"})
		}), "Dara linked GitHub sign-in"},
		{"agent token", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType, r.ResourceID = "asset.agent_token", "asset", ptr(asset.String())
		}), "Dara issued a new agent token for web-1"},
		{"unnamed", "en", with(dara, func(r *row) { r.Action, r.ResourceType = "monitor.delete", "monitor" }),
			"Dara deleted the monitor (unnamed)"},
		{"unknown action", "en", with(dara, func(r *row) {
			r.Action, r.ResourceType, r.ResourceID = "widget.spin", "widget", ptr("w1")
		}), "Dara: widget.spin (widget w1)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, describe(b, c.loc, c.r, n))
		})
	}
}

func TestHelpers(t *testing.T) {
	assert.Equal(t, "'=SUM(A1)", safeCell("=SUM(A1)"))
	assert.Equal(t, "'+1", safeCell("+1"))
	assert.Equal(t, "'-2", safeCell("-2"))
	assert.Equal(t, "'@x", safeCell("@x"))
	assert.Equal(t, "safe", safeCell("safe"))
	assert.Empty(t, safeCell(""))
	assert.Equal(t, "v1", shortRef("refs/tags/v1"))
	assert.Equal(t, "abc", shortRef("abc"))
	assert.Equal(t, "secret", area("secret.read"))
	assert.Equal(t, "odd", area("odd"))
	assert.Equal(t, "Keycloak", providerName("keycloak"))
	assert.Equal(t, "okta", providerName("okta"))
	assert.Equal(t, "12", text(map[string]any{"n": 12.0}, "n"))
	assert.Equal(t, "true", text(map[string]any{"b": true}, "b"))
	assert.Empty(t, text(map[string]any{}, "x"))
}
