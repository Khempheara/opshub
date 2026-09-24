// Package seed creates demo data for local development and E2E tests. It is idempotent
// (existing records are left untouched) and refuses to run in production.
package seed

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
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
	return database.InTx(ctx, pool, func(tx pgx.Tx) error {
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
	})
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
		return nil
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
	var teamID uuid.UUID
	err = tx.QueryRow(ctx, "SELECT id FROM teams WHERE organization_id = $1 AND slug = 'platform' AND deleted_at IS NULL", orgID).Scan(&teamID)
	switch {
	case err == nil:
		if err := q.UpsertProjectTeamGrant(ctx, store.UpsertProjectTeamGrantParams{ProjectID: p.ID, TeamID: teamID, Role: store.MemberRoleDeveloper}); err != nil {
			return err
		}
	case !database.IsNoRows(err):
		return err
	}
	_, _ = fmt.Fprintf(out, "seed: created project %q with development, staging and protected production\n", DemoProjectSlug)
	return nil
}
