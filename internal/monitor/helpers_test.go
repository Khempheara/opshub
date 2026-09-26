package monitor

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
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
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
	"github.com/opshub/opshub/internal/testutil/testca"
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
		Email: "mo-" + uuid.NewString()[:8] + "@example.com", DisplayName: "Watcher " + uuid.NewString()[:4],
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
	require.Equal(t, apperr.CodeValidation, ae.Code, "%v", err)
	var out []string
	for _, f := range ae.Details["fields"].([]apperr.FieldError) {
		out = append(out, f.Field+":"+f.Rule)
	}
	return out
}

type env struct {
	svc                       *Service
	ca                        *testca.CA
	q                         *store.Queries
	orgID                     uuid.UUID
	owner, admin, dev, viewer user
	outsider                  user
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	authority := testca.New(t)
	e := &env{
		svc: NewService(pool, Config{OutboundAllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Roots: authority.Pool},
			slog.New(slog.NewTextHandler(io.Discard, nil))),
		ca: authority, q: store.New(pool),
		owner: newUser(t), admin: newUser(t), dev: newUser(t), viewer: newUser(t), outsider: newUser(t),
	}
	orgs := org.NewService(pool, noJobs{}, org.Config{PublicURL: "https://ops.example.com"})
	o, err := orgs.Create(e.owner.ctx, org.CreateInput{Name: "Uptime Co", Slug: "mo-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	return e
}

func (e *env) monitor(t *testing.T, in MonitorInput) Monitor {
	t.Helper()
	m, err := e.svc.CreateMonitor(e.dev.ctx, e.orgID, in)
	require.NoError(t, err)
	return m
}

// check runs one check of the monitor now and records it, as the worker would.
func (e *env) check(t *testing.T, id uuid.UUID) Monitor {
	t.Helper()
	m, err := e.q.GetMonitor(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, e.svc.record(context.Background(), m, e.svc.Run(context.Background(), m)))
	got, err := e.svc.GetMonitor(e.viewer.ctx, id)
	require.NoError(t, err)
	return got
}
