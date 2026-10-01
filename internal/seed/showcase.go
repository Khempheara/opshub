package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
)

// The showcase organization fills every page with data in every state (runs of each status
// and trigger, all deploy target and channel kinds, firing/acknowledged/pending/resolved
// alerts, silences, invitations, audit history…). It is separate from angkor-tech so the
// E2E tests, which count seeded rows there, are unaffected. Names sort after Angkor Tech, so
// signing in still opens angkor-tech.
const (
	ShowcaseOrgSlug = "mekong-cloud"
	ShowcaseOrgName = "Mekong Cloud · មេគង្គ ក្លោដ"
	// ShowcaseTwoFactorEmail is the demo account with two-factor sign-in turned on.
	ShowcaseTwoFactorEmail = "secure@demo.opshub.local"
)

// showcaseMembers are the showcase organization's people. The four demo users keep their
// roles; the others have no password (as if they sign in with SSO) and only fill the lists.
var showcaseMembers = []struct {
	email, name, role string
	login             bool
}{
	{"owner@demo.opshub.local", "", "owner", true},
	{"admin@demo.opshub.local", "", "admin", true},
	{"dev@demo.opshub.local", "", "developer", true},
	{"viewer@demo.opshub.local", "", "viewer", true},
	{ShowcaseTwoFactorEmail, "Chantha Ros · ចន្ថា រស់", "admin", true},
	{"bopha@demo.opshub.local", "Bopha Ly · បុប្ផា លី", "developer", false},
	{"rithy@demo.opshub.local", "Rithy Chea · រិទ្ធី ជា", "developer", false},
	{"maya@demo.opshub.local", "Maya Patel", "viewer", false},
}

// sc carries what the showcase steps share.
type sc struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	keys  *crypto.KeyRing
	out   io.Writer
	now   time.Time
	org   uuid.UUID
	users map[string]uuid.UUID // by email
	teams map[string]uuid.UUID // by slug
	// Projects and their environments, by slug and name.
	projects map[string]uuid.UUID
	envs     map[string]map[string]uuid.UUID
	// Filled by later steps for the ones after them.
	targets  map[string]uuid.UUID
	assets   map[string]uuid.UUID
	monitors map[string]uuid.UUID
	rules    map[string]uuid.UUID
	channels map[string]uuid.UUID
	runners  map[string]uuid.UUID
	// Run and deployment numbers the audit entries mention, by a short key.
	numbers map[string]int32
}

func (s *sc) user(email string) uuid.UUID { return s.users[email] }

// seedShowcase creates the showcase organization. It does nothing when it exists. Without
// keys (no OPSHUB_MASTER_KEYS) it is skipped: its targets, secrets and channels are encrypted.
func seedShowcase(ctx context.Context, pool *pgxpool.Pool, keys *crypto.KeyRing, passwordHash string, retentionDays int, out io.Writer) error {
	if keys == nil {
		return nil
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM organizations WHERE slug = $1)", ShowcaseOrgSlug).Scan(&exists); err != nil {
		return err
	}
	if exists {
		_, _ = fmt.Fprintf(out, "seed: organization %q already exists; skipping the showcase\n", ShowcaseOrgSlug)
		return nil
	}
	s := &sc{
		ctx: ctx, pool: pool, keys: keys, out: out, now: time.Now().UTC(),
		users: map[string]uuid.UUID{}, teams: map[string]uuid.UUID{}, projects: map[string]uuid.UUID{},
		envs: map[string]map[string]uuid.UUID{}, targets: map[string]uuid.UUID{}, assets: map[string]uuid.UUID{},
		monitors: map[string]uuid.UUID{}, rules: map[string]uuid.UUID{}, channels: map[string]uuid.UUID{}, runners: map[string]uuid.UUID{},
		numbers: map[string]int32{},
	}
	totp, codes, err := s.people(passwordHash)
	if err != nil {
		return fmt.Errorf("showcase people: %w", err)
	}
	steps := []struct {
		name string
		fn   func() error
	}{
		{"projects", s.projectsAndRepos},
		{"runners", s.runnersAndTargets},
		{"history", s.history},
		{"runs", s.runs},
		{"deployments", s.deployments},
		{"secrets", s.secrets},
		{"infrastructure", s.infrastructure},
		{"monitoring", s.monitoring},
		{"alerts", s.alerts},
		{"logs", func() error { return s.logs(retentionDays) }},
		{"audit", s.audit},
	}
	for _, st := range steps {
		if err := st.fn(); err != nil {
			return fmt.Errorf("showcase %s: %w (delete the %q organization's data or reset the database to retry)", st.name, err, ShowcaseOrgSlug)
		}
	}
	_, _ = fmt.Fprintf(out, "seed: created the showcase organization %s (%s): every page has data\n", ShowcaseOrgName, ShowcaseOrgSlug)
	_, _ = fmt.Fprintf(out, "  %s (same password as the other demo users) has two-factor sign-in. Add this key to an authenticator app:\n", ShowcaseTwoFactorEmail)
	_, _ = fmt.Fprintf(out, "    %s\n    %s\n", totp, authn.TOTPURI(totp, ShowcaseTwoFactorEmail, "OpsHub"))
	_, _ = fmt.Fprintf(out, "  recovery codes: %s\n", strings.Join(codes, " "))
	return nil
}

// people creates the organization, its members (and the 2FA user), invitations, teams and
// personal API tokens. It returns the 2FA user's TOTP secret and recovery codes.
func (s *sc) people(passwordHash string) (string, []string, error) {
	ctx := s.ctx
	totp := authn.NewTOTPSecret()
	codes := make([]string, 10)
	for i := range codes {
		raw := strings.ToLower(crypto.RandomBase32(10))
		codes[i] = raw[:5] + "-" + raw[5:] // the format auth hands out
	}
	err := database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// New accounts get the demo owner's password, so every demo account shares one (the
		// password of this run is random when OPSHUB_SEED_PASSWORD isn't set).
		if err := tx.QueryRow(ctx, "SELECT coalesce(password_hash, $2) FROM users WHERE email = $1", DemoUsers[0].Email, passwordHash).Scan(&passwordHash); err != nil && !database.IsNoRows(err) {
			return err
		}
		if err := tx.QueryRow(ctx, "INSERT INTO organizations (slug, name, created_at) VALUES ($1, $2, $3) RETURNING id",
			ShowcaseOrgSlug, ShowcaseOrgName, s.now.AddDate(0, 0, -45)).Scan(&s.org); err != nil {
			return err
		}
		for i, m := range showcaseMembers {
			var id uuid.UUID
			err := tx.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", m.email).Scan(&id)
			if database.IsNoRows(err) {
				var hash *string
				if m.login {
					hash = &passwordHash
				}
				err = tx.QueryRow(ctx, `INSERT INTO users (email, display_name, password_hash, locale, timezone, email_verified_at, created_at)
					VALUES ($1, $2, $3, 'en', 'Asia/Phnom_Penh', $4, $4) RETURNING id`, m.email, m.name, hash, s.now.AddDate(0, 0, -44)).Scan(&id)
			}
			if err != nil {
				return fmt.Errorf("user %s: %w", m.email, err)
			}
			s.users[m.email] = id
			if _, err := tx.Exec(ctx, "INSERT INTO organization_members (organization_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)",
				s.org, id, m.role, s.now.AddDate(0, 0, -44+i)); err != nil {
				return err
			}
		}

		// Two-factor sign-in for the secure@ user: the secret is sealed the way auth seals it.
		secure := s.user(ShowcaseTwoFactorEmail)
		enc, err := s.keys.Encrypt([]byte(totp), []byte("totp:"+secure.String()))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET totp_secret_enc = $2, totp_enabled_at = $3 WHERE id = $1",
			secure, enc, s.now.AddDate(0, 0, -30)); err != nil {
			return err
		}
		for i, c := range codes {
			var used *time.Time
			if i == 0 { // one already used: "9 of 10 left"
				t := s.now.AddDate(0, 0, -12)
				used = &t
			}
			if _, err := tx.Exec(ctx, "INSERT INTO user_recovery_codes (user_id, code_hash, used_at) VALUES ($1, $2, $3)",
				secure, crypto.HashToken(strings.ReplaceAll(c, "-", "")), used); err != nil { // hashed without the dash, as auth does

				return err
			}
		}

		owner := s.user("owner@demo.opshub.local")
		for _, inv := range []struct {
			email, role string
			expires     time.Duration
			ago         time.Duration
		}{
			{"lina.chan@example.com", "developer", 6 * 24 * time.Hour, 24 * time.Hour},
			{"contractor@example.com", "viewer", 2 * 24 * time.Hour, 5 * 24 * time.Hour},
			{"sophea.lim@example.com", "admin", -24 * time.Hour, 8 * 24 * time.Hour}, // expired
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO invitations (organization_id, email, role, token_hash, invited_by, expires_at, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, s.org, inv.email, inv.role, crypto.HashToken(crypto.RandomToken(32)), owner,
				s.now.Add(inv.expires), s.now.Add(-inv.ago)); err != nil {
				return err
			}
		}

		for _, t := range []struct {
			slug, name, description string
			members                 []string
		}{
			{"backend", "Backend · ក្រុម Backend", "APIs and data jobs · API និងការងារទិន្នន័យ",
				[]string{"dev@demo.opshub.local", "bopha@demo.opshub.local", "rithy@demo.opshub.local"}},
			{"mobile", "Mobile · ក្រុមទូរស័ព្ទ", "The Android and iOS apps · កម្មវិធី Android និង iOS",
				[]string{"bopha@demo.opshub.local", "maya@demo.opshub.local"}},
			{"sre", "SRE · ក្រុម SRE", "On-call, infrastructure and releases · ការប្រចាំការ ហេដ្ឋារចនាសម្ព័ន្ធ និងការចេញផ្សាយ",
				[]string{"admin@demo.opshub.local", ShowcaseTwoFactorEmail, "rithy@demo.opshub.local"}},
		} {
			var id uuid.UUID
			if err := tx.QueryRow(ctx, "INSERT INTO teams (organization_id, slug, name, description, created_at) VALUES ($1, $2, $3, $4, $5) RETURNING id",
				s.org, t.slug, t.name, t.description, s.now.AddDate(0, 0, -40)).Scan(&id); err != nil {
				return err
			}
			s.teams[t.slug] = id
			for _, m := range t.members {
				if _, err := tx.Exec(ctx, "INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)", id, s.user(m)); err != nil {
					return err
				}
			}
		}

		// Personal API tokens: active, read-only, expired and revoked. The secrets are random
		// and never shown (create your own in Settings → API tokens).
		for _, t := range []struct {
			email, name string
			scopes      []string
			expires     *time.Time
			used        *time.Time
			revoked     *time.Time
			ago         time.Duration
		}{
			{"owner@demo.opshub.local", "Release script", []string{"api:read", "api:write"}, at(s.now.AddDate(0, 3, 0)), at(s.now.Add(-2 * time.Hour)), nil, 20 * 24 * time.Hour},
			{"owner@demo.opshub.local", "Grafana (read-only)", []string{"api:read"}, nil, at(s.now.Add(-10 * time.Minute)), nil, 35 * 24 * time.Hour},
			{"owner@demo.opshub.local", "Old laptop", []string{"api:read", "api:write"}, at(s.now.AddDate(0, 0, -3)), at(s.now.AddDate(0, 0, -9)), nil, 60 * 24 * time.Hour},
			{"owner@demo.opshub.local", "Leaked in a screenshot", []string{"api:write"}, nil, nil, at(s.now.AddDate(0, 0, -15)), 16 * 24 * time.Hour},
			{"admin@demo.opshub.local", "Terraform", []string{"api:read", "api:write"}, at(s.now.AddDate(0, 1, 0)), at(s.now.Add(-26 * time.Hour)), nil, 10 * 24 * time.Hour},
			{ShowcaseTwoFactorEmail, "Status board", []string{"api:read"}, nil, at(s.now.Add(-5 * time.Minute)), nil, 7 * 24 * time.Hour},
		} {
			prefix := authn.APITokenPrefix + strings.ToLower(crypto.RandomBase32(8))
			if _, err := tx.Exec(ctx, `INSERT INTO api_tokens (user_id, name, token_prefix, token_hash, scopes, expires_at, last_used_at, revoked_at, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, s.user(t.email), t.name, prefix,
				crypto.HashToken(prefix+"_"+crypto.RandomToken(32)), t.scopes, t.expires, t.used, t.revoked, s.now.Add(-t.ago)); err != nil {
				return err
			}
		}
		return nil
	})
	return totp, codes, err
}

func at(t time.Time) *time.Time { return &t }

// showcaseProjects are the showcase projects. internal-tools has no repository and no runs,
// so its empty states show.
var showcaseProjects = []struct {
	slug, name, description string
	envs                    []string
}{
	{"mobile-app", "Mobile App · កម្មវិធីទូរស័ព្ទ", "Android and iOS backend · ផ្នែកខាងក្រោយកម្មវិធីទូរស័ព្ទ", []string{"development", "staging", "production"}},
	{"data-pipeline", "Data Pipeline · បំពង់ទិន្នន័យ", "Nightly sales import and reports · ការនាំចូលទិន្នន័យលក់ប្រចាំយប់", []string{"staging", "production"}},
	{"internal-tools", "Internal Tools · ឧបករណ៍ផ្ទៃក្នុង", "Admin scripts, not set up yet · មិនទាន់រៀបចំ", []string{"development"}},
}

// projectsAndRepos creates the projects with environments, protection rules, access grants,
// connected repositories (GitHub automatic, self-hosted GitLab manual) with recent webhook
// deliveries, and a nightly schedule.
func (s *sc) projectsAndRepos() error {
	ctx := s.ctx
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		owner := s.user("owner@demo.opshub.local")
		vars := map[string]string{
			"development": `{"LOG_LEVEL":"debug","API_URL":"https://dev.mekong.example"}`,
			"staging":     `{"LOG_LEVEL":"info","API_URL":"https://staging.mekong.example"}`,
			"production":  `{"LOG_LEVEL":"warn","API_URL":"https://api.mekong.example","REGION":"ap-southeast-1"}`,
		}
		for i, p := range showcaseProjects {
			var id uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO projects (organization_id, slug, name, description, created_by, created_at)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, s.org, p.slug, p.name, p.description, owner, s.now.AddDate(0, 0, -42+i*5)).Scan(&id); err != nil {
				return err
			}
			s.projects[p.slug] = id
			s.envs[p.slug] = map[string]uuid.UUID{}
			for _, e := range p.envs {
				var envID uuid.UUID
				if err := tx.QueryRow(ctx, "INSERT INTO environments (project_id, name, kind, variables) VALUES ($1, $2, $3, $4) RETURNING id",
					id, e, e, vars[e]).Scan(&envID); err != nil {
					return err
				}
				s.envs[p.slug][e] = envID
			}
		}
		protect := []struct {
			project   string
			approvals int
			branches  []string
			roles     []string
		}{
			{"mobile-app", 2, []string{"main", "release/*", "v*"}, []string{"owner", "admin"}},
			{"data-pipeline", 1, []string{"main"}, []string{"owner", "admin", "developer"}},
		}
		for _, p := range protect {
			if _, err := tx.Exec(ctx, `INSERT INTO protection_rules (environment_id, required_approvals, allowed_branches, allowed_roles)
				VALUES ($1, $2, $3, $4::member_role[])`, s.envs[p.project]["production"], p.approvals, p.branches, p.roles); err != nil {
				return err
			}
		}
		grants := []struct {
			project, team, user, role string
		}{
			{project: "mobile-app", team: "mobile", role: "developer"},
			{project: "mobile-app", team: "sre", role: "admin"},
			{project: "mobile-app", user: "dev@demo.opshub.local", role: "developer"},
			{project: "mobile-app", user: "maya@demo.opshub.local", role: "viewer"},
			{project: "data-pipeline", team: "backend", role: "developer"},
			{project: "data-pipeline", team: "sre", role: "admin"},
			{project: "internal-tools", team: "backend", role: "viewer"},
		}
		for _, g := range grants {
			var team, user *uuid.UUID
			if g.team != "" {
				t := s.teams[g.team]
				team = &t
			} else {
				u := s.user(g.user)
				user = &u
			}
			if _, err := tx.Exec(ctx, "INSERT INTO project_members (project_id, user_id, team_id, role) VALUES ($1, $2, $3, $4)",
				s.projects[g.project], user, team, g.role); err != nil {
				return err
			}
		}

		repos := []struct {
			project, provider, base, fullName, web, mode string
			hook                                         *string
		}{
			{"mobile-app", "github", "", "mekong-cloud/mobile-app", "https://github.com/mekong-cloud/mobile-app", "automatic", ptr("482911037")},
			{"data-pipeline", "gitlab", "https://gitlab.mekong.example", "data/data-pipeline", "https://gitlab.mekong.example/data/data-pipeline", "manual", nil},
		}
		for i, r := range repos {
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			// Placeholder credentials, sealed the way projects seal them (the row id is the AAD).
			tokenEnc, err := s.keys.Encrypt([]byte("demo-token-not-real"), id[:])
			if err != nil {
				return err
			}
			secretEnc, err := s.keys.Encrypt([]byte(crypto.RandomToken(32)), id[:])
			if err != nil {
				return err
			}
			var base *string
			if r.base != "" {
				base = &r.base
			}
			if _, err := tx.Exec(ctx, `INSERT INTO repositories (id, project_id, provider, base_url, full_name, external_id, web_url, clone_url,
				default_branch, access_token_enc, webhook_secret_enc, webhook_mode, webhook_id, connected_by, last_delivery_at, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'main', $9, $10, $11, $12, $13, $14, $15)`,
				id, s.projects[r.project], r.provider, base, r.fullName, fmt.Sprint(73100512+i), r.web, r.web+".git",
				tokenEnc, secretEnc, r.mode, r.hook, owner, s.now.Add(-25*time.Minute), s.now.AddDate(0, 0, -40)); err != nil {
				return err
			}
			deliveries := []struct {
				event, ref string
				valid      bool
				ago        time.Duration
			}{
				{"push", "refs/heads/main", true, 25 * time.Minute},
				{"pull_request", "refs/pull/42/head", true, 3 * time.Hour},
				{"push", "refs/tags/v2.3.0", true, 26 * time.Hour},
				{"push", "refs/heads/main", false, 50 * time.Hour}, // a bad signature (wrong secret)
				{"ping", "", true, 40 * 24 * time.Hour},
			}
			for j, d := range deliveries {
				sha := "" // a ping has no ref or commit
				if d.ref != "" {
					sha = fmt.Sprintf("%040x", 0xa1b2c3d4+j*7919)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries (repository_id, delivery_id, event, ref, commit_sha, signature_valid, payload, received_at)
					VALUES ($1, $2, $3, $4, $5, $6, '{}', $7)`, id, uuid.NewString(), d.event, d.ref, sha, d.valid, s.now.Add(-d.ago)); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, "INSERT INTO pipeline_schedules (project_id, cron, next_run_at, last_run_at) VALUES ($1, '0 19 * * *', $2, $3)",
			s.projects["data-pipeline"], s.now.Truncate(24*time.Hour).Add(19*time.Hour), s.now.Truncate(24*time.Hour).Add(-5*time.Hour))
		return err
	})
}

// mustJSON marshals demo values (which always marshal).
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
