package logs

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
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

type noJobs struct{}

func (noJobs) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

type user struct {
	id  uuid.UUID
	ctx context.Context
}

func newUser(t *testing.T) user {
	t.Helper()
	now := time.Now()
	u, err := store.New(pgtest.Pool(t)).CreateUser(context.Background(), store.CreateUserParams{
		Email: "lg-" + uuid.NewString()[:8] + "@example.com", DisplayName: "Logs " + uuid.NewString()[:4],
		Locale: "en", Timezone: "UTC", EmailVerifiedAt: &now,
	})
	require.NoError(t, err)
	return user{id: u.ID, ctx: authn.WithPrincipal(context.Background(), authn.Principal{Kind: authn.KindSession, UserID: u.ID, SessionID: uuid.New()})}
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
	owner, admin, dev, viewer user
	outsider                  user
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	e := &env{
		svc: NewService(pool, Config{RetentionDays: 30}, slog.New(slog.NewTextHandler(io.Discard, nil))), pool: pool, q: store.New(pool),
		owner: newUser(t), admin: newUser(t), dev: newUser(t), viewer: newUser(t), outsider: newUser(t),
	}
	orgs := org.NewService(pool, noJobs{}, org.Config{PublicURL: "https://ops.example.com"})
	o, err := orgs.Create(e.owner.ctx, org.CreateInput{Name: "Logs Co", Slug: "lg-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	return e
}

// token creates an ingest token for a service and returns it resolved.
func (e *env) token(t *testing.T, service string) (IngestToken, store.LogIngestToken) {
	t.Helper()
	tok, err := e.svc.CreateToken(e.admin.ctx, e.orgID, TokenInput{Name: service + "-" + uuid.NewString()[:6], Service: service})
	require.NoError(t, err)
	st, err := e.svc.Authenticate(context.Background(), tok.Token)
	require.NoError(t, err)
	return tok, st
}

func (e *env) ingest(t *testing.T, st store.LogIngestToken, body string) IngestResult {
	t.Helper()
	res, err := e.svc.Ingest(context.Background(), st, strings.NewReader(body))
	require.NoError(t, err)
	return res
}

// project inserts a project with an environment, a pipeline job and a deployment, all
// directly: the log triggers only need the rows.
type project struct {
	id, jobID, deploymentID uuid.UUID
	slug                    string
}

func (e *env) project(t *testing.T) project {
	t.Helper()
	ctx := context.Background()
	p := project{slug: "p-" + strings.ToLower(uuid.NewString()[:8])}
	var envID, runID uuid.UUID
	require.NoError(t, e.pool.QueryRow(ctx,
		`INSERT INTO projects (organization_id, slug, name) VALUES ($1, $2, $2) RETURNING id`, e.orgID, p.slug).Scan(&p.id))
	require.NoError(t, e.pool.QueryRow(ctx,
		`INSERT INTO environments (project_id, name, kind) VALUES ($1, 'production', 'production') RETURNING id`, p.id).Scan(&envID))
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO pipeline_runs (organization_id, project_id, number, trigger, ref, commit_sha)
		VALUES ($1, $2, 7, 'push', 'refs/heads/main', 'abcdef1') RETURNING id`, e.orgID, p.id).Scan(&runID))
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO pipeline_jobs (run_id, project_id, organization_id, name, stage, stage_index, condition, spec, timeout_seconds)
		VALUES ($1, $2, $3, 'build', 'build', 0, 'on_success', '{}', 600) RETURNING id`, runID, p.id, e.orgID).Scan(&p.jobID))
	require.NoError(t, e.pool.QueryRow(ctx, `INSERT INTO deployments (organization_id, project_id, environment_id, number, target_name, target_kind, version, strategy)
		VALUES ($1, $2, $3, 3, 'web', 'ssh', 'v1.2.0', 'rolling') RETURNING id`, e.orgID, p.id, envID).Scan(&p.deploymentID))
	return p
}

func (e *env) jobLog(t *testing.T, jobID uuid.UUID, seq int, content string) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), `INSERT INTO job_log_chunks (job_id, seq, content) VALUES ($1, $2, $3)`, jobID, seq, content)
	require.NoError(t, err)
}

func (e *env) deploymentLog(t *testing.T, id uuid.UUID, seq int, content string) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), `INSERT INTO deployment_log_chunks (deployment_id, seq, content) VALUES ($1, $2, $3)`, id, seq, content)
	require.NoError(t, err)
}

func messages(r Result) []string {
	out := make([]string, 0, len(r.Items))
	for _, it := range r.Items {
		out = append(out, it.Message)
	}
	return out
}
