package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
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
		Email: "db-" + uuid.NewString()[:8] + "@example.com", DisplayName: "Dash", Locale: "en", Timezone: "UTC", EmailVerifiedAt: &now,
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

// env is an organization with two projects: A (granted to the developer) and B.
type env struct {
	svc                          *Service
	pool                         *pgxpool.Pool
	orgID                        uuid.UUID
	owner, dev, viewer, outsider user
	projA, projB                 uuid.UUID
	prodA, stagingA, prodB       uuid.UUID
	t0                           time.Time // ten days ago, at midnight UTC
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	ctx := context.Background()
	e := &env{svc: NewService(pool), pool: pool, owner: newUser(t), dev: newUser(t), viewer: newUser(t), outsider: newUser(t)}
	e.t0 = time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -10)
	o, err := org.NewService(pool, noJobs{}, org.Config{PublicURL: "https://ops.example.com"}).
		Create(e.owner.ctx, org.CreateInput{Name: "Dash Co", Slug: "db-" + strings.ToLower(uuid.NewString()[:8])})
	require.NoError(t, err)
	e.orgID = o.ID
	q := store.New(pool)
	for u, role := range map[*user]authz.Role{&e.dev: authz.Developer, &e.viewer: authz.Viewer} {
		require.NoError(t, q.AddOrganizationMember(ctx, store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	project := func(name string) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO projects (organization_id, slug, name) VALUES ($1, $2, $3) RETURNING id`,
			e.orgID, strings.ToLower(name)+"-"+uuid.NewString()[:6], name).Scan(&id))
		return id
	}
	environment := func(p uuid.UUID, name, kind string) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO environments (project_id, name, kind) VALUES ($1, $2, $3) RETURNING id`, p, name, kind).Scan(&id))
		return id
	}
	e.projA, e.projB = project("Alpha"), project("Beta")
	e.prodA, e.stagingA, e.prodB = environment(e.projA, "production", "production"), environment(e.projA, "staging", "staging"), environment(e.projB, "prod", "production")
	_, err = pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'developer')`, e.projA, e.dev.id)
	require.NoError(t, err)
	return e
}

var runNumber int

// run inserts a pipeline run created at `at` (offset from t0) that ran for dur.
func (e *env) run(t *testing.T, project uuid.UUID, status string, at, dur time.Duration, committed *time.Time) uuid.UUID {
	t.Helper()
	runNumber++
	created := e.t0.Add(at)
	var started, finished *time.Time
	if status != "queued" {
		s, f := created.Add(time.Minute), created.Add(time.Minute+dur)
		started, finished = &s, &f
	}
	var id uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(), `INSERT INTO pipeline_runs
		(organization_id, project_id, number, status, trigger, ref, commit_sha, created_at, started_at, finished_at, committed_at)
		VALUES ($1, $2, $3, $4, 'push', 'refs/heads/main', 'abcdef1', $5, $6, $7, $8) RETURNING id`,
		e.orgID, project, runNumber, status, created, started, finished, committed).Scan(&id))
	return id
}

var deployNumber int

type dep struct {
	env      uuid.UUID
	project  uuid.UUID
	status   string
	at       time.Duration // created at t0 + at; started a minute later
	dur      time.Duration // until finished
	run      *uuid.UUID
	rollback *uuid.UUID
	reverted bool
}

func (e *env) deploy(t *testing.T, d dep) uuid.UUID {
	t.Helper()
	deployNumber++
	created := e.t0.Add(d.at)
	started, finished := created.Add(time.Minute), created.Add(time.Minute+d.dur)
	var id uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(), `INSERT INTO deployments
		(organization_id, project_id, environment_id, number, target_name, target_kind, version, strategy, status,
		 run_id, rollback_of_id, reverted, created_at, started_at, finished_at)
		VALUES ($1, $2, $3, $4, 'web', 'ssh', 'app:1', 'rolling', $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		e.orgID, d.project, d.env, deployNumber, d.status, d.run, d.rollback, d.reverted, created, started, finished).Scan(&id))
	return id
}

func (e *env) alert(t *testing.T, started time.Duration, lasted time.Duration) {
	t.Helper()
	s := e.t0.Add(started)
	_, err := e.pool.Exec(context.Background(), `INSERT INTO alerts
		(organization_id, rule_name, rule_kind, severity, subject_type, subject_id, subject_name, status, started_at, resolved_at)
		VALUES ($1, 'Down', 'monitor_down', 'critical', 'monitor', $2, 'web', 'resolved', $3, $4)`, e.orgID, uuid.New(), s, s.Add(lasted))
	require.NoError(t, err)
}

func (e *env) query(from, to time.Duration) Query {
	return Query{From: e.t0.Add(from).Format(time.RFC3339), To: e.t0.Add(to).Format(time.RFC3339)}
}

func approx(t *testing.T, want float64, got *float64, msg string) {
	t.Helper()
	require.NotNil(t, got, msg)
	assert.InDelta(t, want, *got, 0.001, msg)
}

func TestPipelines(t *testing.T) {
	e := newEnv(t)
	day := 24 * time.Hour
	e.run(t, e.projA, "succeeded", 0, 60*time.Second, nil)
	e.run(t, e.projA, "succeeded", 2*time.Hour, 120*time.Second, nil)
	e.run(t, e.projA, "failed", day, 300*time.Second, nil)
	e.run(t, e.projA, "canceled", day+time.Hour, 10*time.Second, nil)
	e.run(t, e.projB, "succeeded", 2*day, 30*time.Second, nil)
	e.run(t, e.projA, "succeeded", -2*day, time.Second, nil) // before the range
	q := e.query(0, 3*day)

	st, err := e.svc.Pipelines(e.owner.ctx, e.orgID, q)
	require.NoError(t, err)
	assert.Equal(t, PipelineSummary{Runs: 5, Succeeded: 3, Failed: 1, Canceled: 1,
		SuccessRate: st.Summary.SuccessRate, DurationP50: st.Summary.DurationP50, DurationP95: st.Summary.DurationP95}, st.Summary)
	approx(t, 0.75, st.Summary.SuccessRate, "success rate")
	approx(t, 90, st.Summary.DurationP50, "median of 30, 60, 120, 300")
	approx(t, 273, st.Summary.DurationP95, "p95")
	assert.Equal(t, "day", st.Range.Bucket)
	require.Len(t, st.Trend, 3)
	assert.Equal(t, e.t0.Format(time.DateOnly), st.Trend[0].Period)
	assert.Equal(t, int32(2), st.Trend[0].Runs)
	approx(t, 1, st.Trend[0].SuccessRate, "day 1")
	approx(t, 90, st.Trend[0].DurationP50, "day 1 median")
	assert.Equal(t, int32(2), st.Trend[1].Runs)
	approx(t, 0, st.Trend[1].SuccessRate, "day 2: one failed, one canceled")
	require.Len(t, st.Projects, 2)
	assert.Equal(t, "Alpha", st.Projects[0].Name)
	assert.Equal(t, int32(4), st.Projects[0].Runs)
	assert.Equal(t, "canceled", st.Projects[0].LastStatus)
	approx(t, 2.0/3, st.Projects[0].SuccessRate, "Alpha")

	// Developers see only the projects granted to them; viewers see all.
	dev, err := e.svc.Pipelines(e.dev.ctx, e.orgID, q)
	require.NoError(t, err)
	assert.Equal(t, int32(4), dev.Summary.Runs)
	require.Len(t, dev.Projects, 1)
	view, err := e.svc.Pipelines(e.viewer.ctx, e.orgID, q)
	require.NoError(t, err)
	assert.Equal(t, int32(5), view.Summary.Runs)

	q.Project = e.projB.String()
	one, err := e.svc.Pipelines(e.owner.ctx, e.orgID, q)
	require.NoError(t, err)
	assert.Equal(t, int32(1), one.Summary.Runs)
	_, err = e.svc.Pipelines(e.dev.ctx, e.orgID, q)
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err), "an ungranted project looks missing")
	_, err = e.svc.Pipelines(e.outsider.ctx, e.orgID, Query{})
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	// An empty range has no rates or durations.
	empty, err := e.svc.Pipelines(e.owner.ctx, e.orgID, e.query(5*day, 6*day))
	require.NoError(t, err)
	assert.Zero(t, empty.Summary.Runs)
	assert.Nil(t, empty.Summary.SuccessRate)
	assert.Nil(t, empty.Summary.DurationP50)
	require.Len(t, empty.Trend, 1)
	assert.Nil(t, empty.Trend[0].DurationP50)
	assert.Empty(t, empty.Projects)
}

func TestDora(t *testing.T) {
	e := newEnv(t)
	day, h := 24*time.Hour, time.Hour
	commit := e.t0
	r1 := e.run(t, e.projA, "succeeded", 0, time.Minute, &commit)
	r2 := e.run(t, e.projA, "succeeded", day-h, time.Minute, nil) // no commit time: lead time starts at the run
	// A healthy release (lead time 2 h from the commit).
	e.deploy(t, dep{env: e.prodA, project: e.projA, status: "succeeded", at: 2*h - time.Minute, dur: 0, run: &r1})
	// A failed release, fixed by the next one 29 minutes later (lead time 1.5 h).
	e.deploy(t, dep{env: e.prodA, project: e.projA, status: "failed", at: day, dur: 5 * time.Minute})
	e.deploy(t, dep{env: e.prodA, project: e.projA, status: "succeeded", at: day + 29*time.Minute, dur: 0, run: &r2})
	// A release rolled back 9 minutes after it went live (the rollback isn't a change).
	d4 := e.deploy(t, dep{env: e.prodA, project: e.projA, status: "succeeded", at: 2 * day, dur: 0})
	e.deploy(t, dep{env: e.prodA, project: e.projA, status: "succeeded", at: 2*day + 9*time.Minute, dur: 0, rollback: &d4})
	// A failed release that reverted itself after 5 minutes.
	e.deploy(t, dep{env: e.prodA, project: e.projA, status: "failed", at: 3 * day, dur: 5 * time.Minute, reverted: true})
	// Staging doesn't count; a running deployment isn't finished.
	e.deploy(t, dep{env: e.stagingA, project: e.projA, status: "succeeded", at: 3 * day, dur: time.Minute})
	e.deploy(t, dep{env: e.prodA, project: e.projA, status: "running", at: 3 * day, dur: time.Minute})
	// Project B: a failure nobody fixed yet.
	e.deploy(t, dep{env: e.prodB, project: e.projB, status: "failed", at: 3 * day, dur: time.Minute})
	e.alert(t, day, h)
	e.alert(t, 2*day, 3*h)
	e.alert(t, -5*day, h) // resolved before the range
	q := e.query(0, 4*day)

	d, err := e.svc.Dora(e.owner.ctx, e.orgID, q)
	require.NoError(t, err)
	assert.Equal(t, int32(3), d.Deployments.Succeeded)
	assert.InDelta(t, 0.75, d.Deployments.PerDay, 0.001)
	assert.Equal(t, int32(6), d.ChangeFailureRate.Changes)
	assert.Equal(t, int32(4), d.ChangeFailureRate.Failed)
	approx(t, 4.0/6, d.ChangeFailureRate.Rate, "change failure rate")
	assert.Equal(t, int32(2), d.LeadTime.Samples)
	approx(t, 1.75*3600, d.LeadTime.Median, "lead time median of 2 h and 1.5 h")
	assert.Equal(t, int32(3), d.TimeToRestore.Restored)
	assert.Equal(t, int32(1), d.TimeToRestore.Open)
	approx(t, 9*60, d.TimeToRestore.Median, "median of 29, 9 and 5 minutes (releases go live a minute after they start)")
	assert.Equal(t, int32(2), d.Alerts.Resolved)
	approx(t, 2*3600, d.Alerts.Median, "alerts")
	require.Len(t, d.Trend, 4)
	assert.Equal(t, DeploymentPoint{Period: e.t0.Format(time.DateOnly), Succeeded: 1}, d.Trend[0])
	assert.Equal(t, DeploymentPoint{Period: e.t0.AddDate(0, 0, 1).Format(time.DateOnly), Succeeded: 1, FailedChanges: 1}, d.Trend[1])
	assert.Equal(t, DeploymentPoint{Period: e.t0.AddDate(0, 0, 2).Format(time.DateOnly), FailedChanges: 1}, d.Trend[2], "rolled back: not also a success")
	assert.Equal(t, DeploymentPoint{Period: e.t0.AddDate(0, 0, 3).Format(time.DateOnly), FailedChanges: 2}, d.Trend[3])
	require.Len(t, d.Projects, 2)
	assert.Equal(t, "Alpha", d.Projects[0].Name)
	assert.Equal(t, int32(5), d.Projects[0].Changes)
	approx(t, 3.0/5, d.Projects[0].ChangeFailureRate, "Alpha")
	approx(t, 1.75*3600, d.Projects[0].LeadTimeMedian, "Alpha lead time")

	// The developer sees Alpha only (alerts stay organization-wide).
	dev, err := e.svc.Dora(e.dev.ctx, e.orgID, q)
	require.NoError(t, err)
	assert.Equal(t, int32(5), dev.ChangeFailureRate.Changes)
	assert.Zero(t, dev.TimeToRestore.Open)
	assert.Equal(t, int32(2), dev.Alerts.Resolved)

	// One environment instead of production.
	q.Environment = e.stagingA.String()
	st, err := e.svc.Dora(e.owner.ctx, e.orgID, q)
	require.NoError(t, err)
	assert.Equal(t, int32(1), st.ChangeFailureRate.Changes)
	assert.Nil(t, st.LeadTime.Median, "no pipeline behind it")
	assert.Nil(t, st.TimeToRestore.Median)
}

func TestQueryValidation(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Dora(e.owner.ctx, e.orgID, Query{From: "x", To: "y", Project: "p", Environment: "e", TZ: "Mars/Olympus"})
	assert.ElementsMatch(t, []string{"from:datetime", "to:datetime", "from:range", "project:uuid", "environment:uuid", "tz:timezone"}, fieldsOf(t, err))
	_, err = e.svc.Dora(e.owner.ctx, e.orgID, Query{From: time.Now().AddDate(-2, 0, 0).Format(time.RFC3339)})
	assert.Equal(t, []string{"from:range"}, fieldsOf(t, err), "at most 366 days")
	_, err = e.svc.Pipelines(e.owner.ctx, e.orgID, Query{TZ: "Local"})
	assert.Equal(t, []string{"tz:timezone"}, fieldsOf(t, err))

	// Long ranges go by week, in the caller's time zone.
	d, err := e.svc.Dora(e.owner.ctx, e.orgID, Query{From: "2026-01-01T00:00:00Z", To: "2026-06-30T00:00:00Z", TZ: "Asia/Phnom_Penh"})
	require.NoError(t, err)
	assert.Equal(t, "week", d.Range.Bucket)
	assert.Equal(t, "Asia/Phnom_Penh", d.Range.TZ)
	assert.Equal(t, "2025-12-29", d.Trend[0].Period, "weeks start on Monday")
	assert.Len(t, d.Trend, 27, "the last week (from 29 June) ends inside the range")
	// Defaults: the last 30 days, per day, in UTC.
	p, err := e.svc.Pipelines(e.owner.ctx, e.orgID, Query{})
	require.NoError(t, err)
	assert.Equal(t, "UTC", p.Range.TZ)
	assert.InDelta(t, (30 * 24 * time.Hour).Seconds(), p.Range.To.Sub(p.Range.From).Seconds(), 1)
	assert.Len(t, p.Trend, 31)
}

func TestPeriodsInTimeZone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Phnom_Penh")
	require.NoError(t, err)
	r := Range{From: time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC), Bucket: "day"}
	assert.Equal(t, []string{"2026-09-02", "2026-09-03"}, periods(r, loc), "20:00 UTC is already the next day in Phnom Penh")
	assert.Equal(t, []string{"2026-09-01", "2026-09-02", "2026-09-03"}, periods(r, time.UTC))
}
