package alert

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

// recJobs records the notifications a service enqueues.
type recJobs struct {
	mu     sync.Mutex
	notify []jobs.NotifyArgs
}

func (r *recJobs) InsertTx(_ context.Context, _ pgx.Tx, a river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := a.(jobs.NotifyArgs); ok {
		r.notify = append(r.notify, n)
	}
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

// take returns and forgets the recorded notifications.
func (r *recJobs) take() []jobs.NotifyArgs {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.notify
	r.notify = nil
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
		Email: "al-" + uuid.NewString()[:8] + "@example.com", DisplayName: "Responder " + uuid.NewString()[:4],
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
	jobs                      *recJobs
	q                         *store.Queries
	orgID                     uuid.UUID
	owner, admin, dev, viewer user
	outsider                  user
	// shift moves the service's clock forward (the database clock is unaffected).
	shift time.Duration
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	rec := &recJobs{}
	e := &env{
		svc: NewService(pool, rec, slog.New(slog.NewTextHandler(io.Discard, nil))), jobs: rec, q: store.New(pool),
		owner: newUser(t), admin: newUser(t), dev: newUser(t), viewer: newUser(t), outsider: newUser(t),
	}
	e.svc.now = func() time.Time { return time.Now().Add(e.shift) }
	orgs := org.NewService(pool, rec, org.Config{PublicURL: "https://ops.example.com"})
	o, err := orgs.Create(e.owner.ctx, org.CreateInput{Name: "Alert Co", Slug: "al-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := pgtest.Pool(t).Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

// monitor creates a monitor in the given state (nil: not checked yet).
func (e *env) monitor(t *testing.T, name string, up *bool, latency int32, labels ...string) uuid.UUID {
	t.Helper()
	if labels == nil {
		labels = []string{}
	}
	var id uuid.UUID
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), `INSERT INTO monitors (organization_id, name, kind, target, labels, last_up, last_latency_ms, last_error)
		VALUES ($1, $2, 'http', 'https://x.example.com', $3, $4, $5, CASE WHEN $4 THEN '' ELSE 'unexpected status 503' END) RETURNING id`,
		e.orgID, name, labels, up, latency).Scan(&id))
	return id
}

func (e *env) setMonitor(t *testing.T, id uuid.UUID, up bool, latency int32) {
	t.Helper()
	e.exec(t, `UPDATE monitors SET last_up = $2, last_latency_ms = $3, last_error = CASE WHEN $2 THEN '' ELSE 'unexpected status 503' END WHERE id = $1`, id, up, latency)
}

// server creates a server asset with an agent that reported heartbeatAgo ago.
func (e *env) server(t *testing.T, name string, heartbeatAgo time.Duration, cpu float64, tags ...string) uuid.UUID {
	t.Helper()
	if tags == nil {
		tags = []string{}
	}
	metrics, _ := json.Marshal(map[string]float64{"cpu_pct": cpu, "mem_pct": 40, "disk_pct": 50})
	var id uuid.UUID
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), `INSERT INTO infra_assets (organization_id, kind, name, tags, agent_token_hash,
		agent_token_prefix, last_heartbeat_at, last_metrics) VALUES ($1, 'server', $2, $3, $4, 'x', now() - $5::interval, $6) RETURNING id`,
		e.orgID, name, tags, []byte(uuid.NewString()), heartbeatAgo.String(), metrics).Scan(&id))
	return id
}

// domain creates a domain asset whose certificate expires in the given time (or failed).
func (e *env) domain(t *testing.T, name string, expiresIn time.Duration, checkErr string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), `INSERT INTO infra_assets (organization_id, kind, name, address)
		VALUES ($1, 'domain', $2, $2) RETURNING id`, e.orgID, name).Scan(&id))
	e.exec(t, `INSERT INTO ssl_certificates (asset_id, host, port, not_after, error, last_checked_at, next_check_at)
		VALUES ($1, $2, 443, now() + $3::interval, $4, now(), now() + interval '1 day')`, id, name, expiresIn.String(), checkErr)
	return id
}

func (e *env) channel(t *testing.T, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), `INSERT INTO notification_channels (organization_id, name, kind, secrets_enc)
		VALUES ($1, $2, 'slack', '\x00') RETURNING id`, e.orgID, name).Scan(&id))
	return id
}

func (e *env) rule(t *testing.T, in RuleInput) Rule {
	t.Helper()
	r, err := e.svc.CreateRule(e.dev.ctx, e.orgID, in)
	require.NoError(t, err)
	return r
}

// tick evaluates rules and sends due escalations, as the worker does.
func (e *env) tick(t *testing.T) {
	t.Helper()
	require.NoError(t, e.svc.Evaluate(context.Background()))
	require.NoError(t, e.svc.Escalate(context.Background()))
}

// open returns the rule's open alerts by subject.
func (e *env) open(t *testing.T, ruleID uuid.UUID) map[uuid.UUID]store.Alert {
	t.Helper()
	rows, err := e.q.OpenAlertsForRule(context.Background(), &ruleID)
	require.NoError(t, err)
	out := map[uuid.UUID]store.Alert{}
	for _, a := range rows {
		out[a.SubjectID] = a
	}
	return out
}

// delivered records successful deliveries of the firing message (as the notify worker would).
func (e *env) delivered(t *testing.T, alertID uuid.UUID, channels ...uuid.UUID) {
	t.Helper()
	for _, c := range channels {
		e.exec(t, `INSERT INTO alert_events (alert_id, kind, channel_id, channel_name, detail) VALUES ($1, 'notified', $2, 'c', 'firing')`, alertID, c)
	}
}

func timeline(t *testing.T, e *env, alertID uuid.UUID) []string {
	t.Helper()
	d, err := e.svc.GetAlert(e.viewer.ctx, alertID)
	require.NoError(t, err)
	var out []string
	for _, ev := range d.Events {
		out = append(out, ev.Kind)
	}
	return out
}

func ptr[T any](v T) *T { return &v }
