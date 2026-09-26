package auditlog

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

type noJobs struct{}

func (noJobs) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

type user struct {
	id    uuid.UUID
	name  string
	email string
	ctx   context.Context
}

func newUser(t *testing.T, name string) user {
	t.Helper()
	now := time.Now()
	email := "au-" + uuid.NewString()[:8] + "@example.com"
	u, err := store.New(pgtest.Pool(t)).CreateUser(context.Background(), store.CreateUserParams{
		Email: email, DisplayName: name, Locale: "en", Timezone: "UTC", EmailVerifiedAt: &now,
	})
	require.NoError(t, err)
	return user{id: u.ID, name: name, email: email,
		ctx: authn.WithPrincipal(context.Background(), authn.Principal{Kind: authn.KindSession, UserID: u.ID, SessionID: uuid.New()})}
}

func codeOf(t *testing.T, err error) apperr.Code {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.From(err)
	require.True(t, ok, "expected apperr, got %v", err)
	return ae.Code
}

func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	ae, ok := apperr.From(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, apperr.CodeValidation, ae.Code)
	var out []string
	for _, f := range ae.Details["fields"].([]apperr.FieldError) {
		out = append(out, f.Field+":"+f.Rule)
	}
	return out
}

type env struct {
	svc                       *Service
	pool                      *pgxpool.Pool
	q                         *store.Queries
	orgID                     uuid.UUID
	slug                      string
	owner, admin, dev, viewer user
	outsider                  user
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	e := &env{
		svc: NewService(pool, bundle(t), slog.New(slog.NewTextHandler(io.Discard, nil))), pool: pool, q: store.New(pool),
		owner: newUser(t, "Owner Sok"), admin: newUser(t, "Admin Dara"), dev: newUser(t, "Dev Sophea"), viewer: newUser(t, "Viewer Vanna"),
		outsider: newUser(t, "Outsider"),
	}
	e.slug = "au-" + strings.ToLower(uuid.NewString()[:8])
	o, err := org.NewService(pool, noJobs{}, org.Config{PublicURL: "https://ops.example.com"}).
		Create(e.owner.ctx, org.CreateInput{Name: "Audit Co", Slug: e.slug})
	require.NoError(t, err)
	e.orgID = o.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	return e
}

// record writes an entry as ctx's principal (or as OpsHub when ctx has none).
func (e *env) record(t *testing.T, ctx context.Context, en audit.Entry) {
	t.Helper()
	require.NoError(t, database.InTx(ctx, e.pool, func(tx pgx.Tx) error { return audit.Record(ctx, store.New(tx), en) }))
}

// project inserts a project in the organization.
func (e *env) project(t *testing.T, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`INSERT INTO projects (organization_id, slug, name) VALUES ($1, $2, $3) RETURNING id`,
		e.orgID, "p-"+strings.ToLower(uuid.NewString()[:8]), name).Scan(&id))
	return id
}

func summaries(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, en := range entries {
		out = append(out, en.Summary)
	}
	return out
}
