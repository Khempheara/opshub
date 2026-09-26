package logs

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
)

func TestIngestTokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	tok, err := e.svc.CreateToken(e.admin.ctx, e.orgID, TokenInput{Name: " Checkout API ", Service: "shop/checkout"})
	require.NoError(t, err)
	assert.Equal(t, "Checkout API", tok.Name)
	assert.True(t, strings.HasPrefix(tok.Token, tok.TokenPrefix+"_"))
	assert.True(t, strings.HasPrefix(tok.TokenPrefix, "ohl_"))

	_, err = e.svc.CreateToken(e.admin.ctx, e.orgID, TokenInput{Name: "Checkout API", Service: "other"})
	assert.Equal(t, apperr.CodeIngestTokenNameTaken, codeOf(t, err))
	_, err = e.svc.CreateToken(e.admin.ctx, e.orgID, TokenInput{Name: "", Service: "Bad Name"})
	assert.ElementsMatch(t, []string{"name:range", "service:pattern"}, fieldsOf(t, err))
	_, err = e.svc.CreateToken(e.dev.ctx, e.orgID, TokenInput{Name: "x", Service: "x"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err), "developers cannot manage tokens")
	_, err = e.svc.ListTokens(e.viewer.ctx, e.orgID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.ListTokens(e.outsider.ctx, e.orgID)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	list, err := e.svc.ListTokens(e.owner.ctx, e.orgID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Empty(t, list[0].Token, "the token is never shown again")
	assert.NotNil(t, list[0].CreatedByName)

	st, err := e.svc.Authenticate(ctx, tok.Token)
	require.NoError(t, err)
	assert.Equal(t, "shop/checkout", st.Service)
	for _, bad := range []string{"", "ohr_x", tok.Token + "x", "ohl_" + uuid.NewString()} {
		_, err := e.svc.Authenticate(ctx, bad)
		assert.Equal(t, apperr.CodeIngestTokenInvalid, codeOf(t, err), bad)
	}

	// Last use is recorded.
	e.ingest(t, st, `{"message":"hello"}`)
	list, _ = e.svc.ListTokens(e.owner.ctx, e.orgID)
	assert.NotNil(t, list[0].LastUsedAt)

	assert.Equal(t, apperr.CodeIngestTokenNotFound, codeOf(t, e.svc.RevokeToken(e.outsider.ctx, tok.ID)))
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, e.svc.RevokeToken(e.dev.ctx, tok.ID)))
	require.NoError(t, e.svc.RevokeToken(e.admin.ctx, tok.ID))
	assert.Equal(t, apperr.CodeIngestTokenNotFound, codeOf(t, e.svc.RevokeToken(e.admin.ctx, tok.ID)))
	_, err = e.svc.Authenticate(ctx, tok.Token)
	assert.Equal(t, apperr.CodeIngestTokenInvalid, codeOf(t, err), "revoked tokens stop working")

	rows, err := e.pool.Query(ctx, `SELECT action FROM audit_log WHERE resource_id = $1 ORDER BY id`, tok.ID.String())
	require.NoError(t, err)
	var actions []string
	for rows.Next() {
		var a string
		require.NoError(t, rows.Scan(&a))
		actions = append(actions, a)
	}
	assert.Equal(t, []string{"log_token.create", "log_token.revoke"}, actions)
	var n int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE after::text LIKE '%' || $1 || '%'`, tok.Token).Scan(&n))
	assert.Zero(t, n, "the secret is never audited")
}

func TestIngestAndSearch(t *testing.T) {
	e := newEnv(t)
	_, st := e.token(t, "shop/api")
	now := time.Now().UTC()
	ts := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339Nano) }

	res := e.ingest(t, st, strings.Join([]string{
		fmt.Sprintf(`{"ts":%q,"level":"info","msg":"GET /cart 200","path":"/cart"}`, ts(3*time.Minute)),
		fmt.Sprintf(`{"ts":%q,"level":"error","message":"payment gateway timeout","order":42}`, ts(2*time.Minute)),
		``,
		`broken`,
		fmt.Sprintf(`{"ts":%q,"level":"warn","message":"ការទូទាត់បរាជ័យ សម្រាប់អតិថិជន"}`, ts(time.Minute)),
		`{"level":"debug"}`,
		fmt.Sprintf(`{"ts":%q,"message":"ancient"}`, ts(8*24*time.Hour)),
	}, "\n"))
	assert.Equal(t, 3, res.Accepted)
	assert.Equal(t, 3, res.Rejected)
	assert.Equal(t, []LineError{{Line: 4, Rule: "json_object"}, {Line: 6, Rule: "message_required"}, {Line: 7, Rule: "too_old"}}, res.Errors)

	all, err := e.svc.Search(e.viewer.ctx, e.orgID, Query{})
	require.NoError(t, err)
	assert.Equal(t, []string{"ការទូទាត់បរាជ័យ សម្រាប់អតិថិជន", "payment gateway timeout", "GET /cart 200"}, messages(all), "newest first")
	assert.Equal(t, "shop/api", all.Items[0].Service)
	assert.Equal(t, "service", all.Items[0].Source)
	assert.JSONEq(t, `{"order":42}`, string(all.Items[1].Attributes))

	search := func(q Query) []string {
		t.Helper()
		r, err := e.svc.Search(e.viewer.ctx, e.orgID, q)
		require.NoError(t, err)
		return messages(r)
	}
	assert.Equal(t, []string{"payment gateway timeout"}, search(Query{Q: "gateway"}))
	assert.Equal(t, []string{"payment gateway timeout"}, search(Query{Q: `"gateway timeout"`}))
	assert.Equal(t, []string{"GET /cart 200"}, search(Query{Q: "cart -payment"}))
	assert.Len(t, search(Query{Q: "shop/api"}), 3, "the service name is searchable")
	assert.Equal(t, []string{"GET /cart 200"}, search(Query{Q: "/cart"}))
	assert.Equal(t, []string{"ការទូទាត់បរាជ័យ សម្រាប់អតិថិជន"}, search(Query{Q: "បរាជ័យ"}), "Khmer matches inside words")
	assert.Empty(t, search(Query{Q: "ទូទាត់%"}), "LIKE wildcards are literal")
	assert.Equal(t, []string{"ការទូទាត់បរាជ័យ សម្រាប់អតិថិជន", "payment gateway timeout"}, search(Query{Level: "warn"}))
	assert.Equal(t, []string{"payment gateway timeout"}, search(Query{Level: "error"}))
	assert.Len(t, search(Query{Service: "shop/api", Source: "service"}), 3)
	assert.Empty(t, search(Query{Source: "job"}))
	assert.Empty(t, search(Query{Service: "other"}))
	assert.Equal(t, []string{"GET /cart 200"}, search(Query{To: ts(150 * time.Second)}))
	assert.Equal(t, []string{"ការទូទាត់បរាជ័យ សម្រាប់អតិថិជន"}, search(Query{From: ts(90 * time.Second)}))

	_, err = e.svc.Search(e.viewer.ctx, e.orgID, Query{From: "x", To: "y", Level: "fatal", Source: "cron", Limit: "501", Before: "!!", After: "e30", Q: strings.Repeat("a", 201)})
	assert.ElementsMatch(t, []string{"from:datetime", "to:datetime", "from:range", "level:oneof", "source:oneof", "limit:range", "before:cursor", "after:cursor", "q:max"}, fieldsOf(t, err))
	_, err = e.svc.Search(e.viewer.ctx, e.orgID, Query{From: ts(40 * 24 * time.Hour)})
	assert.Equal(t, []string{"from:range"}, fieldsOf(t, err), "at most 31 days")
	_, err = e.svc.Search(e.outsider.ctx, e.orgID, Query{})
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	services, err := e.svc.Services(e.viewer.ctx, e.orgID)
	require.NoError(t, err)
	assert.Equal(t, []string{"shop/api"}, services)
	_, err = e.svc.Services(e.outsider.ctx, e.orgID)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	// Other organizations' lines never show.
	other := newEnv(t)
	r, err := other.svc.Search(other.owner.ctx, other.orgID, Query{})
	require.NoError(t, err)
	assert.Empty(t, r.Items)
}

func TestPagingAndFollow(t *testing.T) {
	e := newEnv(t)
	_, st := e.token(t, "worker")
	base := time.Now().UTC().Add(-10 * time.Minute)
	var b strings.Builder
	for i := range 5 {
		fmt.Fprintf(&b, "{\"ts\":%q,\"message\":\"line %d\"}\n", base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), i)
	}
	require.Equal(t, 5, e.ingest(t, st, b.String()).Accepted)

	p1, err := e.svc.Search(e.dev.ctx, e.orgID, Query{Limit: "2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"line 4", "line 3"}, messages(p1))
	require.NotNil(t, p1.NextCursor)
	p2, err := e.svc.Search(e.dev.ctx, e.orgID, Query{Limit: "2", Before: *p1.NextCursor})
	require.NoError(t, err)
	assert.Equal(t, []string{"line 2", "line 1"}, messages(p2))
	p3, err := e.svc.Search(e.dev.ctx, e.orgID, Query{Limit: "2", Before: *p2.NextCursor})
	require.NoError(t, err)
	assert.Equal(t, []string{"line 0"}, messages(p3))
	assert.Nil(t, p3.NextCursor)

	// Follow: nothing new keeps the cursor; new lines come back alone.
	require.NotNil(t, p1.NewestCursor)
	idle, err := e.svc.Search(e.dev.ctx, e.orgID, Query{After: *p1.NewestCursor})
	require.NoError(t, err)
	assert.Empty(t, idle.Items)
	assert.Equal(t, p1.NewestCursor, idle.NewestCursor)
	e.ingest(t, st, `{"message":"fresh"}`)
	next, err := e.svc.Search(e.dev.ctx, e.orgID, Query{After: *idle.NewestCursor})
	require.NoError(t, err)
	assert.Equal(t, []string{"fresh"}, messages(next))
	assert.NotEqual(t, p1.NewestCursor, next.NewestCursor)
}

func TestIngestLimits(t *testing.T) {
	e := newEnv(t)
	_, st := e.token(t, "noisy")
	ctx := context.Background()

	_, err := e.svc.Ingest(ctx, st, strings.NewReader(strings.Repeat("{\"message\":\"x\"}\n", MaxLines+1)))
	assert.ErrorIs(t, err, ErrTooLarge)
	_, err = e.svc.Ingest(ctx, st, bytes.NewReader(bytes.Repeat([]byte("{\"message\":\""+strings.Repeat("y", 1000)+"\"}\n"), 1100)))
	assert.ErrorIs(t, err, ErrTooLarge)
	_, err = e.svc.Ingest(ctx, st, strings.NewReader(`{"message":"`+strings.Repeat("z", MaxBodyBytes)+`"}`))
	assert.ErrorIs(t, err, ErrTooLarge, "one line over the limit")

	res, err := e.svc.Ingest(ctx, st, strings.NewReader(strings.Repeat("{\"message\":\"x\"}\n", MaxLines)))
	require.NoError(t, err)
	assert.Equal(t, MaxLines, res.Accepted)

	var seen [2]int
	e.svc.SetIngestObserver(func(accepted, rejected int) { seen[0], seen[1] = seen[0]+accepted, seen[1]+rejected })
	res, err = e.svc.Ingest(ctx, st, strings.NewReader(strings.Repeat("oops\n", 30)))
	require.NoError(t, err)
	assert.Equal(t, 30, res.Rejected)
	assert.Len(t, res.Errors, 20, "only the first 20 problems are listed")
	assert.Equal(t, [2]int{0, 30}, seen, "metrics see every request's counts")

	res, err = e.svc.Ingest(ctx, st, strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, IngestResult{Errors: []LineError{}}, res)
}

func TestPipelineAndDeploymentLogs(t *testing.T) {
	e := newEnv(t)
	p := e.project(t)
	e.jobLog(t, p.jobID, 0, "\x1b[36m$ make build\x1b[0m\r\n\ngo build ./...\n")
	e.jobLog(t, p.jobID, 1, "\x1b[1;31merror: exit status 2\x1b[0m\n")
	e.deploymentLog(t, p.deploymentID, 0, "Deploying v1.2.0 to web\n\x1b[31mhealth check failed\x1b[0m\n")

	r, err := e.svc.Search(e.owner.ctx, e.orgID, Query{Source: "job"})
	require.NoError(t, err)
	assert.Equal(t, []string{"error: exit status 2", "go build ./...", "$ make build"}, messages(r))
	assert.Equal(t, "error", r.Items[0].Level)
	assert.Equal(t, "info", r.Items[1].Level)
	assert.Equal(t, p.slug+"/build", r.Items[0].Service)
	assert.Equal(t, &p.id, r.Items[0].ProjectID)
	assert.JSONEq(t, fmt.Sprintf(`{"project":%q,"job":"build","run":7}`, p.slug), string(r.Items[0].Attributes))

	r, err = e.svc.Search(e.owner.ctx, e.orgID, Query{Source: "deployment"})
	require.NoError(t, err)
	assert.Equal(t, []string{"health check failed", "Deploying v1.2.0 to web"}, messages(r))
	assert.Equal(t, "error", r.Items[0].Level)
	assert.Equal(t, p.slug+"/deploy", r.Items[0].Service)
	assert.JSONEq(t, fmt.Sprintf(`{"project":%q,"deployment":3,"environment":"production"}`, p.slug), string(r.Items[0].Attributes))

	// Viewers see every project; developers only projects they were granted.
	count := func(u user) int {
		t.Helper()
		r, err := e.svc.Search(u.ctx, e.orgID, Query{})
		require.NoError(t, err)
		return len(r.Items)
	}
	assert.Equal(t, 5, count(e.viewer))
	assert.Equal(t, 0, count(e.dev))
	services, err := e.svc.Services(e.dev.ctx, e.orgID)
	require.NoError(t, err)
	assert.Empty(t, services)
	_, err = e.pool.Exec(context.Background(), `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)`, p.id, e.dev.id, authz.Developer)
	require.NoError(t, err)
	assert.Equal(t, 5, count(e.dev))
	services, err = e.svc.Services(e.dev.ctx, e.orgID)
	require.NoError(t, err)
	assert.Equal(t, []string{p.slug + "/build", p.slug + "/deploy"}, services)
}

func TestCopyFailureKeepsOriginal(t *testing.T) {
	e := newEnv(t)
	p := e.project(t)
	ctx := context.Background()
	// Without a partition for now, copying fails; the job's own log must still be written.
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `DO $$ DECLARE r record; BEGIN
		FOR r IN SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'log_entries' LOOP EXECUTE format('ALTER TABLE log_entries DETACH PARTITION %I', r.relname); END LOOP; END $$`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO job_log_chunks (job_id, seq, content) VALUES ($1, 0, 'still stored')`, p.jobID)
	require.NoError(t, err)
	var content string
	require.NoError(t, tx.QueryRow(ctx, `SELECT content FROM job_log_chunks WHERE job_id = $1`, p.jobID).Scan(&content))
	assert.Equal(t, "still stored", content)
}

func TestMaintainPartitions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	partitions := func() []string {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
			JOIN pg_class p ON p.oid = i.inhparent WHERE p.relname = 'log_entries' ORDER BY 1`)
		require.NoError(t, err)
		var out []string
		for rows.Next() {
			var n string
			require.NoError(t, rows.Scan(&n))
			out = append(out, n)
		}
		return out
	}
	day := func(d int) string { return "log_entries_" + time.Now().UTC().AddDate(0, 0, d).Format("20060102") }

	_, err := e.pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s PARTITION OF log_entries FOR VALUES FROM ('%s') TO ('%s')`,
		day(-40), time.Now().UTC().AddDate(0, 0, -40).Format("2006-01-02"), time.Now().UTC().AddDate(0, 0, -39).Format("2006-01-02")))
	require.NoError(t, err)
	require.NoError(t, e.svc.Maintain(ctx))
	got := partitions()
	assert.NotContains(t, got, day(-40), "expired partitions are dropped")
	for d := -7; d <= 2; d++ {
		assert.Contains(t, got, day(d))
	}

	short := NewService(e.pool, Config{RetentionDays: 3}, e.svc.logger)
	assert.Equal(t, 3, short.cfg.RetentionDays)
	assert.Equal(t, 30, NewService(e.pool, Config{}, e.svc.logger).cfg.RetentionDays, "invalid retention falls back to 30")

	// Lines are stored in the partition of their day.
	_, st := e.token(t, "cron")
	old := time.Now().UTC().AddDate(0, 0, -6)
	require.Equal(t, 1, e.ingest(t, st, fmt.Sprintf(`{"ts":%q,"message":"six days ago"}`, old.Format(time.RFC3339))).Accepted)
	var n int
	require.NoError(t, e.pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE organization_id = $1`, "log_entries_"+old.Format("20060102")), e.orgID).Scan(&n))
	assert.Equal(t, 1, n)
}
