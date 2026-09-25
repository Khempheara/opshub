package secret

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/project"
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
		Email: "sc-" + uuid.NewString()[:8] + "@example.com", DisplayName: "Keeper " + uuid.NewString()[:4],
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

type env struct {
	svc                       *Service
	keys                      *crypto.KeyRing
	pipelines                 *pipeline.Service
	projects                  *project.Service
	q                         *store.Queries
	orgID, projectID          uuid.UUID
	staging, production       uuid.UUID // production is protected
	owner, admin, dev, viewer user
	outsider                  user
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{9}, 32)}})
	require.NoError(t, err)
	projects := project.NewService(pool, keys, gitprovider.Factory{}, noJobs{}, project.Config{PublicURL: "https://ops.example.com"}, logger)
	e := &env{
		svc: NewService(pool, keys, logger), keys: keys, pipelines: pipeline.NewService(pool, projects, logger),
		projects: projects, q: store.New(pool),
		owner: newUser(t), admin: newUser(t), dev: newUser(t), viewer: newUser(t), outsider: newUser(t),
	}
	orgs := org.NewService(pool, noJobs{}, org.Config{PublicURL: "https://ops.example.com"})
	o, err := orgs.Create(e.owner.ctx, org.CreateInput{Name: "Vault Co", Slug: "sc-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	p, err := projects.Create(e.owner.ctx, o.ID, project.CreateInput{Name: "API", Slug: "api"})
	require.NoError(t, err)
	e.projectID = p.ID
	require.NoError(t, projects.Grant(e.owner.ctx, p.ID, "user:"+e.dev.id.String(), authz.Developer))
	st, err := projects.CreateEnvironment(e.admin.ctx, p.ID, project.EnvironmentInput{Name: "staging", Kind: "staging"})
	require.NoError(t, err)
	e.staging = st.ID
	pr, err := projects.CreateEnvironment(e.admin.ctx, p.ID, project.EnvironmentInput{
		Name: "production", Kind: "production", Protection: &project.Protection{AllowedRoles: []authz.Role{authz.Owner, authz.Admin}},
	})
	require.NoError(t, err)
	e.production = pr.ID
	return e
}

func (e *env) create(t *testing.T, name string, envID *uuid.UUID, value string) Secret {
	t.Helper()
	s, err := e.svc.Create(e.admin.ctx, e.projectID, CreateInput{Name: name, EnvironmentID: envID, Value: value})
	require.NoError(t, err)
	return s
}

// run starts a run of a one-stage pipeline and returns its jobs by name.
func (e *env) run(t *testing.T, trigger store.RunTrigger, yaml string) map[string]store.PipelineJob {
	t.Helper()
	def, err := spec.Parse([]byte(yaml))
	require.NoError(t, err)
	id, err := e.pipelines.CreateRunFromDefinition(context.Background(), e.projectID, def, trigger, "refs/heads/main",
		"abc1230000000000000000000000000000000000", "Test", "tester", nil)
	require.NoError(t, err)
	jobs, err := e.q.CurrentJobs(context.Background(), id)
	require.NoError(t, err)
	out := map[string]store.PipelineJob{}
	for _, j := range jobs {
		out[j.Name] = j
	}
	return out
}

func (e *env) forJob(t *testing.T, j store.PipelineJob) (JobSecrets, error) {
	t.Helper()
	var out JobSecrets
	err := database.InTx(context.Background(), pgtest.Pool(t), func(tx pgx.Tx) error {
		var err error
		out, err = e.svc.ForJob(context.Background(), store.New(tx), j, uuid.New())
		return err
	})
	return out, err
}

func reasonOf(j store.PipelineJob) string {
	if j.FailureReason == nil {
		return ""
	}
	return *j.FailureReason
}
