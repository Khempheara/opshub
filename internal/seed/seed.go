// Package seed creates demo data for local development and E2E tests. It is idempotent
// (existing records are left untouched) and refuses to run in production.
package seed

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/store"
)

// DemoOrg is the demo organization. Names are bilingual because the org name is shown
// verbatim in both UI languages.
const (
	DemoOrgSlug = "angkor-tech"
	DemoOrgName = "Angkor Tech · អង្គរ តិច"
)

// DemoUser is one seeded account (one per role; one prefers Khmer).
type DemoUser struct {
	Email, Name, Locale string
	Role                store.MemberRole
	PlatformAdmin       bool
}

var DemoUsers = []DemoUser{
	{Email: "owner@demo.opshub.local", Name: "Sokha Chan · សុខា ចាន់", Locale: "km", Role: store.MemberRoleOwner, PlatformAdmin: true},
	{Email: "admin@demo.opshub.local", Name: "Dara Kim · តារា គីម", Locale: "en", Role: store.MemberRoleAdmin},
	{Email: "dev@demo.opshub.local", Name: "Vicheka Sok · វិច្ឆិកា សុខ", Locale: "km", Role: store.MemberRoleDeveloper},
	{Email: "viewer@demo.opshub.local", Name: "Alex Morgan", Locale: "en", Role: store.MemberRoleViewer},
}

// Options control the seed run.
type Options struct {
	// Password for every demo user; a random one is generated (and printed) when empty.
	Password string
	Hasher   *authn.Hasher
	Out      io.Writer
	// Keys encrypt the demo deploy target's credentials; without them it isn't seeded.
	Keys *crypto.KeyRing
	// LogRetentionDays is OPSHUB_LOG_RETENTION_DAYS (log partitions are prepared for it).
	LogRetentionDays int
}

// Run seeds the demo organization and users.
func Run(ctx context.Context, pool *pgxpool.Pool, opts Options) error {
	password := opts.Password
	if password == "" {
		password = "demo-" + crypto.RandomToken(12)
	}
	if perr := authn.CheckPasswordPolicy(password, ""); perr != nil {
		return fmt.Errorf("seed password rejected by policy: %s", perr.Message)
	}
	hash, err := opts.Hasher.Hash(password)
	if err != nil {
		return err
	}
	if err := database.InTx(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// Check first: a failed INSERT would abort the transaction, so "skip" couldn't commit.
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM organizations WHERE slug = $1)", DemoOrgSlug).Scan(&exists); err != nil {
			return err
		}
		if exists {
			_, _ = fmt.Fprintf(opts.Out, "seed: organization %q already exists; skipping users and teams\n", DemoOrgSlug)
			return seedProject(ctx, q, tx, opts.Out)
		}
		org, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{Slug: DemoOrgSlug, Name: DemoOrgName})
		if err != nil {
			return err
		}
		now := time.Now()
		for _, du := range DemoUsers {
			u, err := q.GetUserByEmail(ctx, du.Email)
			if database.IsNoRows(err) {
				u, err = q.CreateUser(ctx, store.CreateUserParams{
					Email: du.Email, DisplayName: du.Name, PasswordHash: &hash, Locale: du.Locale,
					Timezone: "Asia/Phnom_Penh", IsPlatformAdmin: du.PlatformAdmin, EmailVerifiedAt: &now,
				})
			}
			if err != nil {
				return fmt.Errorf("seed user %s: %w", du.Email, err)
			}
			if err := q.AddOrganizationMember(ctx, store.AddOrganizationMemberParams{OrganizationID: org.ID, UserID: u.ID, Role: du.Role}); err != nil {
				return err
			}
		}
		team, err := q.CreateTeam(ctx, store.CreateTeamParams{
			OrganizationID: org.ID, Slug: "platform", Name: "Platform Team · ក្រុមវេទិកា",
			Description: "Runs CI/CD and infrastructure · គ្រប់គ្រង CI/CD និងហេដ្ឋារចនាសម្ព័ន្ធ",
		})
		if err != nil {
			return err
		}
		for _, du := range DemoUsers {
			if du.Role != store.MemberRoleAdmin && du.Role != store.MemberRoleDeveloper {
				continue
			}
			u, err := q.GetUserByEmail(ctx, du.Email)
			if err != nil {
				return err
			}
			if err := q.AddTeamMember(ctx, store.AddTeamMemberParams{TeamID: team.ID, UserID: u.ID}); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(opts.Out, "seed: created %s with %d users and a team\n", DemoOrgName, len(DemoUsers))
		for _, du := range DemoUsers {
			_, _ = fmt.Fprintf(opts.Out, "  %-28s %s\n", du.Email, du.Role)
		}
		_, _ = fmt.Fprintf(opts.Out, "  password (all demo users): %s\n", password)
		return seedProject(ctx, q, tx, opts.Out)
	}); err != nil {
		return err
	}
	if err := seedRuns(ctx, pool, opts.Out); err != nil {
		return err
	}
	if err := seedDeployments(ctx, pool, opts.Keys, opts.Out); err != nil {
		return err
	}
	if err := seedInfra(ctx, pool, opts.Out); err != nil {
		return err
	}
	if err := seedSecrets(ctx, pool, opts.Keys, opts.Out); err != nil {
		return err
	}
	if err := seedMonitoring(ctx, pool, opts.Keys, opts.Out); err != nil {
		return err
	}
	if err := seedLogs(ctx, pool, opts.LogRetentionDays, opts.Out); err != nil {
		return err
	}
	if err := seedHistory(ctx, pool, opts.Out); err != nil {
		return err
	}
	return seedShowcase(ctx, pool, opts.Keys, hash, opts.LogRetentionDays, opts.Out)
}

// DemoProjectSlug is the seeded project in the demo organization.
const DemoProjectSlug = "payments-api"

// seedProject creates the demo project with development, staging and a protected
// production environment, and gives the Platform team Developer access. It runs on every
// seed so databases seeded before Module 3 get it too.
func seedProject(ctx context.Context, q *store.Queries, tx pgx.Tx, out io.Writer) error {
	var orgID, ownerID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT o.id, om.user_id FROM organizations o
		JOIN organization_members om ON om.organization_id = o.id AND om.role = 'owner'
		WHERE o.slug = $1 AND o.deleted_at IS NULL ORDER BY om.created_at LIMIT 1`, DemoOrgSlug).Scan(&orgID, &ownerID); err != nil {
		return fmt.Errorf("seed project: demo organization: %w", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM projects WHERE organization_id = $1 AND slug = $2 AND deleted_at IS NULL)",
		orgID, DemoProjectSlug).Scan(&exists); err != nil {
		return err
	}
	if exists {
		_, _ = fmt.Fprintf(out, "seed: project %q already exists; skipping\n", DemoProjectSlug)
		var projectID uuid.UUID
		if err := tx.QueryRow(ctx, "SELECT id FROM projects WHERE organization_id = $1 AND slug = $2 AND deleted_at IS NULL",
			orgID, DemoProjectSlug).Scan(&projectID); err != nil {
			return err
		}
		return seedAccess(ctx, q, tx, orgID, projectID)
	}
	p, err := q.CreateProject(ctx, store.CreateProjectParams{
		OrganizationID: orgID, Slug: DemoProjectSlug, Name: "Payments API · API ទូទាត់ប្រាក់",
		Description: "Card and KHQR payment processing · ដំណើរការការទូទាត់តាមកាត និង KHQR", DefaultBranch: "main",
		CreatedBy: &ownerID,
	})
	if err != nil {
		return err
	}
	envs := []struct {
		name, kind string
		vars       string
	}{
		{"development", "development", `{"LOG_LEVEL":"debug","PAYMENT_GATEWAY":"sandbox"}`},
		{"staging", "staging", `{"LOG_LEVEL":"info","PAYMENT_GATEWAY":"sandbox"}`},
		{"production", "production", `{"LOG_LEVEL":"warn","PAYMENT_GATEWAY":"live","REGION":"ap-southeast-1"}`},
	}
	for _, e := range envs {
		env, err := q.CreateEnvironment(ctx, store.CreateEnvironmentParams{
			ProjectID: p.ID, Name: e.name, Kind: store.EnvironmentKind(e.kind), Variables: []byte(e.vars),
		})
		if err != nil {
			return err
		}
		if e.kind == "production" {
			if err := q.UpsertProtectionRule(ctx, store.UpsertProtectionRuleParams{
				EnvironmentID: env.ID, RequiredApprovals: 1, AllowedBranches: []string{"main"}, AllowedRoles: []string{"owner", "admin"},
			}); err != nil {
				return err
			}
		}
	}
	if err := seedAccess(ctx, q, tx, orgID, p.ID); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "seed: created project %q with development, staging and protected production\n", DemoProjectSlug)
	return nil
}

// seedAccess gives the demo Developer access to the demo project: through the Platform team
// when it exists (fresh databases), otherwise directly (databases seeded before teams).
func seedAccess(ctx context.Context, q *store.Queries, tx pgx.Tx, orgID, projectID uuid.UUID) error {
	var teamID uuid.UUID
	err := tx.QueryRow(ctx, "SELECT id FROM teams WHERE organization_id = $1 AND slug = 'platform' AND deleted_at IS NULL", orgID).Scan(&teamID)
	switch {
	case err == nil:
		return q.UpsertProjectTeamGrant(ctx, store.UpsertProjectTeamGrantParams{ProjectID: projectID, TeamID: teamID, Role: store.MemberRoleDeveloper})
	case !database.IsNoRows(err):
		return err
	}
	for _, du := range DemoUsers {
		if du.Role != store.MemberRoleDeveloper {
			continue
		}
		var userID uuid.UUID
		if err := tx.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", du.Email).Scan(&userID); err != nil {
			return err
		}
		if err := q.UpsertProjectUserGrant(ctx, store.UpsertProjectUserGrantParams{ProjectID: projectID, UserID: userID, Role: store.MemberRoleDeveloper}); err != nil {
			return err
		}
	}
	return nil
}

// DemoPipeline is the demo project's .opshub.yml.
const DemoPipeline = `version: 1
on:
  push:
    branches: [main, "feature/*"]
  pull_request:
    branches: [main]
stages: [test, build, deploy]
jobs:
  test:
    stage: test
    image: golang:1.23
    steps:
      - go vet ./...
      - go test -race ./...
  build:
    stage: build
    needs: [test]
    image: docker:27
    steps:
      - docker build -t payments-api:${OPSHUB_COMMIT_SHA} .
    artifacts: [dist/]
  deploy-production:
    stage: deploy
    needs: [build]
    environment: production
    when: manual
    image: alpine:3
    steps:
      - ./scripts/deploy.sh production
`

const (
	esc    = "\x1b"
	green  = esc + "[32m"
	red    = esc + "[31;1m"
	yellow = esc + "[33m"
	dim    = esc + "[2m"
	reset  = esc + "[0m"
)

var demoLogs = map[string]string{
	"test": dim + "$ go vet ./..." + reset + "\n" + dim + "$ go test -race ./..." + reset + "\n" +
		"ok  \tpayments/api/internal/khqr\t0.412s\n" +
		"ok  \tpayments/api/internal/cards\t1.087s\n" +
		green + "PASS" + reset + " 214 tests, 0 failures\n",
	"test-failed": dim + "$ go test -race ./..." + reset + "\n" +
		"--- " + red + "FAIL" + reset + ": TestRefundRounding (0.00s)\n" +
		"    refund_test.go:42: expected 1250 riel, got 1249\n" +
		red + "FAIL" + reset + "\tpayments/api/internal/refunds\t0.311s\n",
	"build": dim + "$ docker build ." + reset + "\n" +
		"#1 [internal] load build definition from Dockerfile\n" +
		"#7 [builder 4/4] RUN go build -o /out/api ./cmd/api\n" +
		yellow + "#9 exporting to image" + reset + "\n" +
		green + "Successfully built payments-api" + reset + "\n",
	"deploy-production": dim + "$ ./scripts/deploy.sh production" + reset + "\n" +
		"Rolling out 3 replicas…\n" + green + "Deployment healthy" + reset + "\n",
}

// seedRuns adds demo pipeline runs to the demo project when it has none: a green run with
// an approved production deploy, a failed feature branch, a run waiting for approval and a
// queued manual run. Jobs are "executed" through the runner-side service calls.
func seedRuns(ctx context.Context, pool *pgxpool.Pool, out io.Writer) error {
	var projectID, orgID uuid.UUID
	err := pool.QueryRow(ctx, `SELECT p.id, p.organization_id FROM projects p JOIN organizations o ON o.id = p.organization_id
		WHERE o.slug = $1 AND p.slug = $2 AND p.deleted_at IS NULL`, DemoOrgSlug, DemoProjectSlug).Scan(&projectID, &orgID)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var runs int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pipeline_runs WHERE project_id = $1", projectID).Scan(&runs); err != nil {
		return err
	}
	if runs > 0 {
		_, _ = fmt.Fprintf(out, "seed: project %q already has runs; skipping\n", DemoProjectSlug)
		return nil
	}
	users := map[string]uuid.UUID{}
	for _, du := range DemoUsers {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", du.Email).Scan(&id); err != nil {
			return err
		}
		users[string(du.Role)] = id
	}
	def, err := spec.Parse([]byte(DemoPipeline))
	if err != nil {
		return err
	}
	svc := pipeline.NewService(pool, nil, slog.New(slog.DiscardHandler))
	// The demo runs were "executed" by a placeholder runner that never connects.
	var runner uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runners (organization_id, name, labels, token_hash, token_prefix, disabled_at)
		VALUES ($1, 'demo-runner (seed data)', '{linux}', $2, 'demo', now()) RETURNING id`,
		orgID, crypto.HashToken(crypto.RandomToken(32))).Scan(&runner); err != nil {
		return err
	}

	// work runs every queued job; failing names fail with the "test-failed" log.
	work := func(fail bool) error {
		for {
			c, err := svc.Claim(ctx, orgID, runner, nil, nil)
			if err != nil || c == nil {
				return err
			}
			failing := fail && c.Job.Name == "test"
			log := demoLogs[c.Job.Name]
			if failing {
				log = demoLogs["test-failed"]
			}
			for i := range c.Job.Steps {
				if err := svc.ReportStep(ctx, c.Job.ID, int32(i), store.StepStatusRunning, nil); err != nil { // #nosec G115 -- few steps
					return err
				}
			}
			if err := svc.AppendLog(ctx, c.Job.ID, 0, log, nil); err != nil {
				return err
			}
			var code *int32
			if failing {
				one := int32(1)
				code = &one
			}
			if err := svc.Complete(ctx, c.Job.ID, !failing, code, ""); err != nil {
				return err
			}
		}
	}
	approve := func(runID uuid.UUID, as uuid.UUID) error {
		var jobID uuid.UUID
		if err := pool.QueryRow(ctx, "SELECT id FROM pipeline_jobs WHERE run_id = $1 AND status = 'waiting_approval'", runID).Scan(&jobID); err != nil {
			return err
		}
		actx := authn.WithPrincipal(ctx, authn.Principal{Kind: authn.KindSession, UserID: as, SessionID: uuid.New()})
		_, err := svc.Decide(actx, jobID, pipeline.DecideInput{Decision: store.ApprovalDecisionApproved, Comment: "Looks good · ល្អ"})
		return err
	}

	r1, err := svc.CreateRunFromDefinition(ctx, projectID, def, store.RunTriggerPush, "refs/heads/main", "4f9c2d1a7b3e", "Add KHQR refunds", "sokha-chan", nil)
	if err != nil {
		return err
	}
	if err := work(false); err != nil {
		return err
	}
	if err := approve(r1, users[string(store.MemberRoleAdmin)]); err != nil {
		return err
	}
	if err := work(false); err != nil {
		return err
	}
	if _, err := svc.CreateRunFromDefinition(ctx, projectID, def, store.RunTriggerPush, "refs/heads/feature/refund-rounding", "b81e0c55d2a9",
		"Round refunds to the nearest riel", "vicheka-sok", nil); err != nil {
		return err
	}
	if err := work(true); err != nil {
		return err
	}
	if _, err := svc.CreateRunFromDefinition(ctx, projectID, def, store.RunTriggerPush, "refs/heads/main", "c3d4e5f60718",
		"Bump card processor SDK", "dara-kim", nil); err != nil {
		return err
	}
	if err := work(false); err != nil {
		return err
	}
	dev := users[string(store.MemberRoleDeveloper)]
	if _, err := svc.CreateRunFromDefinition(ctx, projectID, def, store.RunTriggerManual, "refs/heads/main", "c3d4e5f60718",
		"Bump card processor SDK", "", &dev); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "seed: created 4 demo pipeline runs (succeeded, failed, waiting for approval, queued)\n")
	return nil
}
