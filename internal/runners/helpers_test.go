package runners

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	"github.com/opshub/opshub/internal/blob"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

const (
	gitToken = "ghp_runner-test"
	sha      = "abc1230000000000000000000000000000000000"
)

const twoJobs = `version: 1
stages: [build, test]
jobs:
  build:
    stage: build
    image: alpine:3
    steps: [make]
    artifacts:
      paths: [dist/]
      expire_in: 3d
    cache:
      key: deps
      paths: [.cache/]
  test:
    stage: test
    image: alpine:3
    runs_on: [linux]
    steps: [make test, make lint]
`

// fakeGit serves one commit, its pipeline file and its tarball.
type fakeGit struct {
	mu       sync.Mutex
	tarball  []byte
	pipeline string // .opshub.yml; twoJobs when empty
}

func setPipeline(t *testing.T, e *env, content string) {
	t.Helper()
	e.git.mu.Lock()
	defer e.git.mu.Unlock()
	e.git.pipeline = content
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func (f *fakeGit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+gitToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch p := r.URL.Path; {
	case p == "/repos/acme/api":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "full_name": "acme/api", "html_url": "https://github.com/acme/api",
			"clone_url": "https://github.com/acme/api.git", "default_branch": "main", "permissions": map[string]bool{"admin": false},
		})
	case strings.HasPrefix(p, "/repos/acme/api/commits/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "commit": map[string]string{"message": "Build it"}})
	case p == "/repos/acme/api/contents/.opshub.yml":
		content := f.pipeline
		if content == "" {
			content = twoJobs
		}
		_, _ = w.Write([]byte(content))
	case p == "/repos/acme/api/tarball/"+sha:
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(f.tarball)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type user struct {
	id  uuid.UUID
	ctx context.Context
}

func newUser(t *testing.T) user {
	t.Helper()
	now := time.Now()
	u, err := store.New(pgtest.Pool(t)).CreateUser(context.Background(), store.CreateUserParams{
		Email: "rn-" + uuid.NewString()[:8] + "@example.com", DisplayName: "Runner " + uuid.NewString()[:4],
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

type noJobs struct{}

func (noJobs) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

type env struct {
	svc                       *Service
	pipelines                 *pipeline.Service
	projects                  *project.Service
	blobs                     *blob.Local
	git                       *fakeGit
	q                         *store.Queries
	orgID, projectID          uuid.UUID
	owner, admin, dev, viewer user
	outsider                  user
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{7}, 32)}})
	require.NoError(t, err)
	git := &fakeGit{tarball: tarGz(t, map[string]string{"acme-api-abc123/README.md": "hello"})}
	srv := httptest.NewServer(git)
	t.Cleanup(srv.Close)
	projects := project.NewService(pool, keys, gitprovider.Factory{HTTP: srv.Client(), GitHubAPIURL: srv.URL}, noJobs{},
		project.Config{PublicURL: "https://ops.example.com"}, logger)
	pipelines := pipeline.NewService(pool, projects, logger)
	blobs, err := blob.NewLocal(t.TempDir())
	require.NoError(t, err)
	e := &env{
		svc: NewService(pool, pipelines, projects, blobs, Config{
			ArtifactMaxBytes: 1 << 20, CacheMaxBytes: 1 << 20, CacheQuotaBytes: 1 << 20, SourceMaxBytes: 1 << 20,
		}, logger),
		pipelines: pipelines, projects: projects, blobs: blobs, git: git, q: store.New(pool),
		owner: newUser(t), admin: newUser(t), dev: newUser(t), viewer: newUser(t), outsider: newUser(t),
	}
	orgs := org.NewService(pool, noJobs{}, org.Config{PublicURL: "https://ops.example.com"})
	o, err := orgs.Create(e.owner.ctx, org.CreateInput{Name: "Runners Inc", Slug: "rn-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	p, err := projects.Create(e.owner.ctx, o.ID, project.CreateInput{Name: "API", Slug: "api"})
	require.NoError(t, err)
	e.projectID = p.ID
	_, err = projects.ConnectRepository(e.admin.ctx, p.ID, project.ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: gitToken})
	require.NoError(t, err)
	return e
}

// register creates a runner through a registration token and returns it with its token.
func (e *env) register(t *testing.T, name string, labels []string, maxConcurrency int32) (store.Runner, string) {
	t.Helper()
	rt, err := e.svc.CreateRegistrationToken(e.admin.ctx, e.orgID, RegistrationInput{})
	require.NoError(t, err)
	reg, err := e.svc.RegisterRunner(context.Background(), RegisterInput{
		Token: rt.Token, Name: name, Labels: labels, Version: "1.0.0", OS: "linux", Arch: "amd64", MaxConcurrency: maxConcurrency,
	})
	require.NoError(t, err)
	r, err := e.svc.AuthenticateRunner(context.Background(), reg.Token)
	require.NoError(t, err)
	return r, reg.Token
}

func (e *env) trigger(t *testing.T) pipeline.RunDetail {
	t.Helper()
	r, err := e.pipelines.TriggerManual(e.admin.ctx, e.projectID, pipeline.ManualRunInput{Ref: "main"})
	require.NoError(t, err)
	return r
}

// claim requests a job without waiting.
func (e *env) claim(t *testing.T, r store.Runner) *AssignedJob {
	t.Helper()
	j, err := e.svc.tryClaim(context.Background(), r)
	require.NoError(t, err)
	return j
}

func (e *env) job(t *testing.T, a *AssignedJob) store.PipelineJob {
	t.Helper()
	j, err := e.svc.AuthenticateJob(context.Background(), a.Token, a.Job.ID)
	require.NoError(t, err)
	return j
}

func (e *env) jobStatus(t *testing.T, id uuid.UUID) (store.JobStatus, string) {
	t.Helper()
	j, err := e.q.GetJob(context.Background(), id)
	require.NoError(t, err)
	reason := ""
	if j.FailureReason != nil {
		reason = *j.FailureReason
	}
	return j.Status, reason
}
