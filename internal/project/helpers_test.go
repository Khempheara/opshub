package project

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

const goodToken = "ghp_good-token"

// fakeGit is a stateful stand-in for the GitHub and GitLab APIs.
type fakeGit struct {
	mu         sync.Mutex
	admin      bool // token may manage hooks
	hookStatus int  // non-zero: answer hook creation with this status
	hooks      map[string]string
	nextID     int
	deleted    []string
	lastSecret string
}

func (f *fakeGit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	authorized := r.Header.Get("Authorization") == "Bearer "+goodToken || r.Header.Get("PRIVATE-TOKEN") == goodToken
	if !authorized {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := r.URL.EscapedPath()
	gitlab := strings.HasPrefix(path, "/api/v4/")
	switch {
	case strings.Contains(path, "missing"):
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet && !strings.Contains(path, "/hooks"):
		level := 30
		if f.admin {
			level = 40
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": 4242, "full_name": "acme/api", "path_with_namespace": "acme/api",
			"html_url": "https://github.com/acme/api", "web_url": "https://gitlab.com/acme/api",
			"clone_url": "https://github.com/acme/api.git", "http_url_to_repo": "https://gitlab.com/acme/api.git",
			"default_branch": "main",
			"permissions":    map[string]any{"admin": f.admin, "project_access": map[string]int{"access_level": level}},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/hooks"):
		if f.hookStatus != 0 {
			w.WriteHeader(f.hookStatus)
			return
		}
		var in struct {
			Config struct {
				URL    string `json:"url"`
				Secret string `json:"secret"`
			} `json:"config"`
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.nextID++
		id := strconv.Itoa(f.nextID)
		if gitlab {
			f.hooks[id], f.lastSecret = in.URL, in.Token
		} else {
			f.hooks[id], f.lastSecret = in.Config.URL, in.Config.Secret
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": f.nextID})
	case r.Method == http.MethodDelete:
		id := path[strings.LastIndex(path, "/")+1:]
		delete(f.hooks, id)
		f.deleted = append(f.deleted, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type user struct {
	id    uuid.UUID
	email string
	ctx   context.Context
}

func newUser(t *testing.T) user {
	t.Helper()
	now := time.Now()
	email := "p-" + uuid.NewString()[:8] + "@example.com"
	u, err := store.New(pgtest.Pool(t)).CreateUser(context.Background(), store.CreateUserParams{
		Email: email, DisplayName: "User " + email[:10], Locale: "en", Timezone: "UTC", EmailVerifiedAt: &now,
	})
	require.NoError(t, err)
	ctx := authn.WithPrincipal(context.Background(), authn.Principal{Kind: authn.KindSession, UserID: u.ID, SessionID: uuid.New()})
	return user{id: u.ID, email: email, ctx: ctx}
}

func codeOf(t *testing.T, err error) apperr.Code {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.From(err)
	require.True(t, ok, "expected apperr, got %v", err)
	return ae.Code
}

// noJobs satisfies jobs.Inserter for the org service (no emails are sent in these tests).
type noJobs struct{}

func (noJobs) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

// recJobs records enqueued background jobs.
type recJobs struct {
	mu   sync.Mutex
	args []river.JobArgs
}

func (r *recJobs) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append(r.args, args)
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

func (r *recJobs) all() []river.JobArgs {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]river.JobArgs(nil), r.args...)
}

// env is an organization with one member per role, an outsider, and a fake Git host.
type env struct {
	svc                             *Service
	orgs                            *org.Service
	git                             *fakeGit
	jobs                            *recJobs
	keys                            *crypto.KeyRing
	orgID                           uuid.UUID
	owner, admin, dev, dev2, viewer user
	outsider                        user
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{7}, 32)}})
	require.NoError(t, err)
	git := &fakeGit{admin: true, hooks: map[string]string{}}
	srv := httptest.NewServer(git)
	t.Cleanup(srv.Close)
	factory := gitprovider.Factory{HTTP: srv.Client(), GitHubAPIURL: srv.URL, GitLabURL: srv.URL}
	rec := &recJobs{}
	e := &env{
		jobs: rec,
		svc:  NewService(pool, keys, factory, rec, Config{PublicURL: "https://ops.example.com"}, slog.New(slog.NewTextHandler(io.Discard, nil))),
		orgs: org.NewService(pool, noJobs{}, org.Config{PublicURL: "https://ops.example.com"}),
		git:  git, keys: keys,
		owner: newUser(t), admin: newUser(t), dev: newUser(t), dev2: newUser(t), viewer: newUser(t), outsider: newUser(t),
	}
	o, err := e.orgs.Create(e.owner.ctx, org.CreateInput{Name: "Mekong Apps", Slug: "mk-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	q := store.New(pool)
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.dev2: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	// The outsider owns a different organization.
	_, err = e.orgs.Create(e.outsider.ctx, org.CreateInput{Name: "Elsewhere", Slug: "el-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	return e
}

func (e *env) project(t *testing.T, as user) Detail {
	t.Helper()
	p, err := e.svc.Create(as.ctx, e.orgID, CreateInput{Name: "Payments API", Slug: "pay-" + strings.ToLower(uuid.NewString()[:6])})
	require.NoError(t, err)
	return p
}

func (e *env) team(t *testing.T, members ...user) uuid.UUID {
	t.Helper()
	q := store.New(pgtest.Pool(t))
	tm, err := q.CreateTeam(context.Background(), store.CreateTeamParams{OrganizationID: e.orgID, Slug: "t-" + strings.ToLower(uuid.NewString()[:8]), Name: "Team"})
	require.NoError(t, err)
	for _, m := range members {
		require.NoError(t, q.AddTeamMember(context.Background(), store.AddTeamMemberParams{TeamID: tm.ID, UserID: m.id}))
	}
	return tm.ID
}

func userP(u user) string       { return "user:" + u.id.String() }
func teamP(id uuid.UUID) string { return "team:" + id.String() }
