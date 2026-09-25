package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
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
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

const (
	gitToken = "ghp_deploy-test"
	sha      = "d3910000000000000000000000000000000000aa"
)

// fakeGit serves one commit and a pipeline file.
type fakeGit struct {
	mu       sync.Mutex
	pipeline string
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
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "commit": map[string]string{"message": "Ship it"}})
	case p == "/repos/acme/api/contents/.opshub.yml":
		_, _ = w.Write([]byte(f.pipeline))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// recJobs records enqueued deployments so tests run them explicitly.
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

func (r *recJobs) deployments() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []uuid.UUID
	for _, a := range r.args {
		if d, ok := a.(jobs.DeploymentArgs); ok {
			out = append(out, d.DeploymentID)
		}
	}
	return out
}

type user struct {
	id  uuid.UUID
	ctx context.Context
}

func newUser(t *testing.T) user {
	t.Helper()
	now := time.Now()
	u, err := store.New(pgtest.Pool(t)).CreateUser(context.Background(), store.CreateUserParams{
		Email: "dp-" + uuid.NewString()[:8] + "@example.com", DisplayName: "Deployer " + uuid.NewString()[:4],
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
	pipelines                 *pipeline.Service
	projects                  *project.Service
	jobs                      *recJobs
	git                       *fakeGit
	q                         *store.Queries
	ssh                       *sshServer
	versionFile               string
	orgID, projectID          uuid.UUID
	owner, admin, dev, viewer user
	outsider                  user
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{3}, 32)}})
	require.NoError(t, err)
	git := &fakeGit{}
	srv := httptest.NewServer(git)
	t.Cleanup(srv.Close)
	rec := &recJobs{}
	projects := project.NewService(pool, keys, gitprovider.Factory{HTTP: srv.Client(), GitHubAPIURL: srv.URL}, rec,
		project.Config{PublicURL: "https://ops.example.com"}, logger)
	pipelines := pipeline.NewService(pool, projects, logger)
	svc := NewService(pool, keys, pipelines, rec, Config{
		OutboundAllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	}, logger)
	svc.health.interval = 50 * time.Millisecond
	e := &env{
		svc: svc, pipelines: pipelines, projects: projects, jobs: rec, git: git, q: store.New(pool), ssh: startSSHServer(t),
		versionFile: filepath.Join(t.TempDir(), "version"),
		owner:       newUser(t), admin: newUser(t), dev: newUser(t), viewer: newUser(t), outsider: newUser(t),
	}
	orgs := org.NewService(pool, rec, org.Config{PublicURL: "https://ops.example.com"})
	o, err := orgs.Create(e.owner.ctx, org.CreateInput{Name: "Deploy Co", Slug: "dp-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	p, err := projects.Create(e.owner.ctx, o.ID, project.CreateInput{Name: "API", Slug: "api"})
	require.NoError(t, err)
	e.projectID = p.ID
	require.NoError(t, projects.Grant(e.owner.ctx, p.ID, "user:"+e.dev.id.String(), authz.Developer))
	_, err = projects.ConnectRepository(e.admin.ctx, p.ID, project.ConnectInput{Provider: "github", FullName: "acme/api", AccessToken: gitToken})
	require.NoError(t, err)
	return e
}

// environment creates an environment, optionally protected.
func (e *env) environment(t *testing.T, name string, protection *project.Protection) uuid.UUID {
	t.Helper()
	env, err := e.projects.CreateEnvironment(e.admin.ctx, e.projectID, project.EnvironmentInput{Name: name, Kind: "staging", Protection: protection})
	require.NoError(t, err)
	return env.ID
}

// sshTarget creates a pinned SSH target on the test server whose command records the
// version (and fails for images tagged "boom").
func (e *env) sshTarget(t *testing.T, name string) Target {
	t.Helper()
	cfg, _ := json.Marshal(SSHConfig{
		Hosts: []string{e.ssh.addr}, User: "deploy", HostKeys: map[string]string{e.ssh.addr: e.ssh.fingerprint},
		Command: `test "$OPSHUB_VERSION" != app:boom && printf %s "$OPSHUB_VERSION" > ` + e.versionFile + ` && echo "now running $OPSHUB_VERSION"`,
	})
	creds, _ := json.Marshal(SSHCredentials{PrivateKey: e.ssh.clientKey})
	tg, err := e.svc.CreateTarget(e.admin.ctx, e.orgID, TargetInput{Name: name, Kind: KindSSH, Config: cfg, Credentials: creds})
	require.NoError(t, err)
	return tg
}

func (e *env) deployed(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(e.versionFile)
	return string(b)
}

// runAll performs every enqueued deployment not yet performed.
func (e *env) runAll(t *testing.T) {
	t.Helper()
	for _, id := range e.jobs.deployments() {
		require.NoError(t, e.svc.Run(context.Background(), id, 1))
	}
}

func (e *env) get(t *testing.T, id uuid.UUID) Deployment {
	t.Helper()
	d, err := e.svc.GetDeployment(e.admin.ctx, id)
	require.NoError(t, err)
	return d
}
