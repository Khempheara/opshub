package seed

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/database"
)

// showcaseAuditEntry is one past action. Fields follow what the services record, so the audit
// log renders them as sentences (see auditlog.describe).
type showcaseAuditEntry struct {
	ago                 time.Duration
	actor               string // email; "" with actorType runner/system
	actorType           string // user (default), api_token, runner, system
	action, resource    string
	resourceID          string
	before, after, meta map[string]any
	project             string // adds metadata.project_id
	ip, agent           string
	account             bool // an account entry (no organization)
}

// audit writes past actions over 45 days, by several people, an API token, a runner and
// OpsHub itself, from different addresses. The audit log is append-only, so the entries are
// inserted with their time instead of being created now and moved back. Actions the seed
// performs through services (secrets, monitors, rules, channels, ingest tokens, approvals,
// re-runs, cancels) are recorded by those services and aren't repeated here.
func (s *sc) audit() error {
	const (
		chrome  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6) AppleWebKit/537.36 Chrome/130.0 Safari/537.36"
		android = "Mozilla/5.0 (Linux; Android 15; Pixel 8) AppleWebKit/537.36 Chrome/130.0 Mobile Safari/537.36"
		cli     = "opshub-cli/1.4.0"
	)
	d := 24 * time.Hour
	h := time.Hour
	id := func(m map[string]uuid.UUID, k string) string { return m[k].String() }
	u := s.user
	owner, admin, dev, secure := "owner@demo.opshub.local", "admin@demo.opshub.local", "dev@demo.opshub.local", ShowcaseTwoFactorEmail
	entries := []showcaseAuditEntry{
		{ago: 45 * d, actor: owner, action: "org.create", resource: "organization", resourceID: s.org.String(), after: map[string]any{"name": ShowcaseOrgName, "slug": ShowcaseOrgSlug}},
		{ago: 44 * d, actor: owner, action: "member.invite", resource: "invitation", after: map[string]any{"email": admin, "role": "admin"}},
		{ago: 44 * d, actor: admin, action: "member.join", resource: "member", resourceID: u(admin).String(), after: map[string]any{"role": "admin"}},
		{ago: 43 * d, actor: dev, action: "member.join", resource: "member", resourceID: u(dev).String(), after: map[string]any{"role": "viewer"}},
		{ago: 41 * d, actor: owner, action: "member.update_role", resource: "member", resourceID: u(dev).String(), before: map[string]any{"role": "viewer"}, after: map[string]any{"role": "developer"}},
		{ago: 42 * d, actor: owner, action: "project.create", resource: "project", resourceID: id(s.projects, "mobile-app"), project: "mobile-app", after: map[string]any{"name": "Mobile App · កម្មវិធីទូរស័ព្ទ"}},
		{ago: 40 * d, actor: admin, action: "team.create", resource: "team", resourceID: id(s.teams, "sre"), after: map[string]any{"name": "SRE · ក្រុម SRE"}},
		{ago: 40 * d, actor: admin, action: "team.add_member", resource: "team", resourceID: id(s.teams, "sre"), after: map[string]any{"user_id": u(secure).String()}},
		{ago: 40 * d, actor: owner, action: "repo.connect", resource: "repository", project: "mobile-app", after: map[string]any{"provider": "github", "full_name": "mekong-cloud/mobile-app", "webhook_mode": "automatic"}},
		{ago: 39 * d, actor: admin, action: "project.grant", resource: "project_member", project: "mobile-app", after: map[string]any{"name": "Mobile · ក្រុមទូរស័ព្ទ", "role": "developer"}},
		{ago: 38 * d, actor: admin, action: "runner.registration_token", resource: "runner_registration_token"},
		{ago: 38 * d, actorType: "runner", action: "runner.register", resource: "runner", resourceID: id(s.runners, "linux-builder-01"), after: map[string]any{"name": "linux-builder-01"}},
		{ago: 37 * d, actor: admin, action: "deploy_target.create", resource: "deploy_target", resourceID: id(s.targets, "mekong-k8s"), after: map[string]any{"name": "mekong-k8s", "kind": "kubernetes"}},
		{ago: 37 * d, actor: admin, action: "deploy_target.create", resource: "deploy_target", resourceID: id(s.targets, "edge-ssh"), after: map[string]any{"name": "edge-ssh", "kind": "ssh"}},
		{ago: 36 * d, actor: admin, action: "asset.create", resource: "asset", resourceID: id(s.assets, "api-1"), after: map[string]any{"name": "api-1"}},
		{ago: 36 * d, actor: admin, action: "asset.agent_token", resource: "asset", resourceID: id(s.assets, "api-1")},
		{ago: 34 * d, actor: owner, action: "token.create", resource: "api_token", after: map[string]any{"name": "Grafana (read-only)", "scopes": []string{"api:read"}}},
		{ago: 30 * d, actor: secure, action: "auth.2fa_enabled", resource: "user", resourceID: u(secure).String(), account: true},
		{ago: 25 * d, actor: admin, action: "log_token.revoke", resource: "log_ingest_token", before: map[string]any{"name": "Old collector", "service": "mobile-app/legacy"}},
		{ago: 21 * d, actor: owner, action: "project.create", resource: "project", resourceID: id(s.projects, "internal-tools"), project: "internal-tools", after: map[string]any{"name": "Internal Tools · ឧបករណ៍ផ្ទៃក្នុង"}},
		{ago: 18 * d, actor: admin, actorType: "api_token", action: "deploy_target.update", resource: "deploy_target", resourceID: id(s.targets, "reports-docker"), after: map[string]any{"name": "reports-docker"}, agent: "Terraform/1.9"},
		{ago: 15 * d, actor: owner, action: "token.revoke", resource: "api_token", before: map[string]any{"name": "Leaked in a screenshot"}},
		{ago: 12 * d, actor: secure, action: "auth.recovery_code_used", resource: "user", resourceID: u(secure).String(), account: true, agent: android, ip: "36.37.212.9"},
		{ago: 10 * d, actor: admin, action: "silence.create", resource: "silence", after: map[string]any{"comment": "Load test"}},
		{ago: 10*d - 2*h, actor: admin, action: "silence.expire", resource: "silence"},
		{ago: 9 * d, actor: admin, action: "runner.update", resource: "runner", resourceID: id(s.runners, "old-builder"), after: map[string]any{"name": "old-builder", "disabled": true}},
		{ago: 6 * d, actor: admin, action: "alert.ack", resource: "alert", meta: map[string]any{"rule": "Production down · ផលិតកម្មដាច់", "subject": "Orders DB"}},
		{ago: 6 * d, actor: secure, action: "deployment.create", resource: "deployment", project: "data-pipeline", after: map[string]any{"version": "ghcr.io/mekong-cloud/data-api:0.9.1", "environment": "staging"}},
		{ago: 5 * d, actorType: "system", action: "deployment.succeeded", resource: "deployment", project: "data-pipeline", after: map[string]any{"number": s.numbers["ghcr.io/mekong-cloud/reports:3.2.0"], "version": "ghcr.io/mekong-cloud/reports:3.2.0"}},
		{ago: 3 * d, actorType: "runner", action: "secret.read", resource: "secret", project: "data-pipeline", after: map[string]any{"name": "WAREHOUSE_URL"}},
		{ago: 25 * h, actor: owner, action: "member.invite", resource: "invitation", after: map[string]any{"email": "lina.chan@example.com", "role": "developer"}},
		{ago: 9 * h, actor: dev, action: "pipeline.trigger", resource: "pipeline_run", project: "mobile-app", after: map[string]any{"number": s.numbers["failed"], "ref": "refs/heads/release/2.4"}},
		{ago: 4 * h, actorType: "system", action: "deployment.failed", resource: "deployment", project: "mobile-app", after: map[string]any{"number": s.numbers["ghcr.io/mekong-cloud/mobile-api:2.40.0-rc1"], "version": "ghcr.io/mekong-cloud/mobile-api:2.40.0-rc1"}},
		{ago: 2 * h, actor: admin, action: "alert.ack", resource: "alert", meta: map[string]any{"rule": "api-2 agent offline", "subject": "api-2"}},
		{ago: 90 * time.Minute, actor: secure, action: "auth.login_failed", resource: "user", resourceID: u(secure).String(), account: true, meta: map[string]any{"reason": "bad_second_factor"}, ip: "36.37.212.9", agent: android},
		{ago: 88 * time.Minute, actor: secure, action: "auth.login", resource: "user", resourceID: u(secure).String(), account: true, ip: "36.37.212.9", agent: android},
		{ago: 50 * time.Minute, actor: secure, action: "deployment.create", resource: "deployment", project: "data-pipeline", after: map[string]any{"version": "ghcr.io/mekong-cloud/data-api:0.10.0", "environment": "staging"}, agent: cli},
		{ago: 20 * time.Minute, actor: owner, action: "audit.export", resource: "audit_log"},
	}
	ips := []string{"203.144.92.10", "103.216.51.77", "192.168.10.24"}
	return database.InTx(s.ctx, s.pool, func(tx pgx.Tx) error {
		for i, e := range entries {
			actorType := e.actorType
			if actorType == "" {
				actorType = "user"
			}
			var actor *uuid.UUID
			if e.actor != "" {
				a := s.user(e.actor)
				actor = &a
			}
			meta := map[string]any{}
			for k, v := range e.meta {
				meta[k] = v
			}
			if e.project != "" {
				meta["project_id"] = s.projects[e.project].String()
			}
			var org *uuid.UUID
			if !e.account {
				org = &s.org
			}
			var resourceID *string
			if e.resourceID != "" {
				resourceID = &e.resourceID
			}
			ip, agent := e.ip, e.agent
			if ip == "" && actorType == "user" || actorType == "api_token" {
				ip = ips[i%len(ips)]
			}
			if agent == "" && actorType == "user" {
				agent = chrome
			}
			var ipArg, agentArg *string
			if ip != "" {
				ipArg = &ip
			}
			if agent != "" {
				agentArg = &agent
			}
			var before, after []byte
			if e.before != nil {
				before = mustJSON(e.before)
			}
			if e.after != nil {
				after = mustJSON(e.after)
			}
			if _, err := tx.Exec(s.ctx, `INSERT INTO audit_log (organization_id, actor_user_id, actor_type, action, resource_type, resource_id,
				ip, user_agent, before, after, metadata, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7::inet, $8, $9, $10, $11, $12)`,
				org, actor, actorType, e.action, e.resource, resourceID, ipArg, agentArg, before, after, mustJSON(meta), s.now.Add(-e.ago)); err != nil {
				return err
			}
		}
		return nil
	})
}
