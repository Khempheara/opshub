// Package seed creates demo data for local development and E2E tests. It is idempotent
// (existing records are left untouched) and refuses to run in production.
package seed

import (
	"context"
	"fmt"
	"io"
	"time"

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
		org, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{Slug: DemoOrgSlug, Name: DemoOrgName})
		orgCreated := err == nil
		if database.IsUniqueViolation(err, "organizations_slug_key") {
			_, _ = fmt.Fprintf(opts.Out, "seed: organization %q already exists; skipping\n", DemoOrgSlug)
			return nil
		}
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
			if orgCreated {
				if err := q.AddOrganizationMember(ctx, store.AddOrganizationMemberParams{OrganizationID: org.ID, UserID: u.ID, Role: du.Role}); err != nil {
					return err
				}
			}
		}
		_, _ = fmt.Fprintf(opts.Out, "seed: created %s with %d users\n", DemoOrgName, len(DemoUsers))
		for _, du := range DemoUsers {
			_, _ = fmt.Fprintf(opts.Out, "  %-28s %s\n", du.Email, du.Role)
		}
		_, _ = fmt.Fprintf(opts.Out, "  password (all demo users): %s\n", password)
		return nil
	})
}
