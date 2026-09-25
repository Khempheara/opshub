package pipeline

import (
	"bytes"
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
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

const token = "ghp_pipeline-test"

// fakeGit serves commits and `.opshub.yml` contents per commit SHA.
type fakeGit struct {
	mu      sync.Mutex
	commits map[string]gitprovider.Commit // ref → commit
	files   map[string]string             // sha → .opshub.yml ("" = no file)
	down    bool
}

func (f *fakeGit) set(ref, sha, file string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits[ref] = gitprovider.Commit{SHA: sha, Message: "Commit " + sha + "\n\nbody"}
	f.files[sha] = file
}

func (f *fakeGit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := r.URL.Path
	switch {
	case path == "/repos/acme/api":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "full_name": "acme/api", "html_url": "https://github.com/acme/api",
			"clone_url": "https://github.com/acme/api.git", "default_branch": "main", "permissions": map[string]bool{"admin": false},
		})
	case strings.HasPrefix(path, "/repos/acme/api/commits/"):
		c, ok := f.commits[strings.TrimPrefix(path, "/repos/acme/api/commits/")]
		if !ok {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": c.SHA, "commit": map[string]string{"message": c.Message}})
	case path == "/repos/acme/api/contents/.opshub.yml":
		content, ok := f.files[r.URL.Query().Get("ref")]
		if !ok || content == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(content))
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
		Email: "pl-" + uuid.NewString()[:8] + "@example.com", DisplayName: "Pipeline " + uuid.NewString()[:4],
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

func detail(t *testing.T, err error) map[string]any {
	t.Helper()
	ae, ok := apperr.From(err)
	require.True(t, ok)
	return ae.Details
}

// recJobs records jobs enqueued by the webhook receiver.
type recJobs struct {
	mu   sync.Mutex
	args []river.JobArgs
}

func (r *recJobs) InsertTx(_ context.Context, _ pgx.Tx, a river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append(r.args, a)
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

type env struct {
	svc                       *Service
	projects                  *project.Service
	git                       *fakeGit
	q                         *store.Queries
	orgID, projectID          uuid.UUID
	owner, admin, dev, viewer user
	outsider                  user
	runnerID                  uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{9}, 32)}})
	require.NoError(t, err)
	git := &fakeGit{commits: map[string]gitprovider.Commit{}, files: map[string]string{}}
	srv := httptest.NewServer(git)
	t.Cleanup(srv.Close)
	projects := project.NewService(pool, keys, gitprovider.Factory{HTTP: srv.Client(), GitHubAPIURL: srv.URL}, &recJobs{},
		project.Config{PublicURL: "https://ops.example.com"}, logger)
	orgs := org.NewService(pool, &recJobs{}, org.Config{PublicURL: "https://ops.example.com"})
	e := &env{
		svc: NewService(pool, projects, logger), projects: projects, git: git, q: store.New(pool),
		owner: newUser(t), admin: newUser(t), dev: newUser(t), viewer: newUser(t), outsider: newUser(t),
	}
	o, err := orgs.Create(e.owner.ctx, org.CreateInput{Name: "Pipelines Inc", Slug: "pl-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	runner, err := e.q.CreateRunner(context.Background(), store.CreateRunnerParams{
		OrganizationID: o.ID, Name: "test-runner", Labels: []string{"linux"}, TokenHash: []byte(uuid.NewString()),
		TokenPrefix: "test", MaxConcurrency: 4,
	})
	require.NoError(t, err)
	e.runnerID = runner.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	p, err := projects.Create(e.owner.ctx, o.ID, project.CreateInput{Name: "API", Slug: "api"})
	require.NoError(t, err)
	e.projectID = p.ID
	// A plain Developer on the project (a Developer who creates a project becomes its Admin).
	require.NoError(t, projects.Grant(e.owner.ctx, p.ID, "user:"+e.dev.id.String(), authz.Developer))
	_, err = projects.ConnectRepository(e.admin.ctx, p.ID, project.ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: token})
	require.NoError(t, err)
	return e
}

// environment creates an environment, optionally protected.
func (e *env) environment(t *testing.T, name string, protection *project.Protection, vars map[string]string) {
	t.Helper()
	_, err := e.projects.CreateEnvironment(e.admin.ctx, e.projectID, project.EnvironmentInput{
		Name: name, Kind: "production", Variables: vars, Protection: protection,
	})
	require.NoError(t, err)
}

func (e *env) run(t *testing.T, as user, ref string) RunDetail {
	t.Helper()
	r, err := e.svc.TriggerManual(as.ctx, e.projectID, ManualRunInput{Ref: ref})
	require.NoError(t, err)
	return r
}

func (e *env) get(t *testing.T, runID uuid.UUID) RunDetail {
	t.Helper()
	r, err := e.svc.GetRun(e.owner.ctx, runID)
	require.NoError(t, err)
	return r
}

// statuses maps job name → status (+ "/reason").
func statuses(r RunDetail) map[string]string {
	out := map[string]string{}
	for _, j := range r.Jobs {
		s := string(j.Status)
		if j.FailureReason != nil {
			s += "/" + *j.FailureReason
		}
		out[j.Name] = s
	}
	return out
}

func jobByName(t *testing.T, r RunDetail, name string) Job {
	t.Helper()
	for _, j := range r.Jobs {
		if j.Name == name {
			return j
		}
	}
	t.Fatalf("no job %q", name)
	return Job{}
}

// runner claims the next job and finishes it (success or failure), logging one line.
func (e *env) runNext(t *testing.T, success bool) *ClaimedJob {
	t.Helper()
	c, err := e.svc.Claim(context.Background(), e.orgID, e.runnerID, []string{"linux"}, nil)
	require.NoError(t, err)
	require.NotNil(t, c, "expected a queued job")
	ctx := context.Background()
	for i := range c.Job.Steps {
		require.NoError(t, e.svc.ReportStep(ctx, c.Job.ID, int32(i), store.StepStatusRunning, nil)) // #nosec G115
	}
	require.NoError(t, e.svc.AppendLog(ctx, c.Job.ID, 0, "hello from "+c.Job.Name+"\n", nil))
	var code *int32
	if !success {
		one := int32(1)
		code = &one
	}
	require.NoError(t, e.svc.Complete(ctx, c.Job.ID, success, code, ""))
	return c
}
