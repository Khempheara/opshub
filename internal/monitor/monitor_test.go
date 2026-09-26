package monitor

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

func TestValidation(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		in   MonitorInput
		want []string
	}{
		{MonitorInput{Kind: store.MonitorKindHttp, Target: "ftp://x"}, []string{"name:range", "target:url"}},
		{MonitorInput{Name: "a", Kind: store.MonitorKindHttp, Target: "https://x.example.com", IntervalSeconds: 10, TimeoutMs: 500},
			[]string{"interval_seconds:range", "timeout_ms:range"}},
		{MonitorInput{Name: "a", Kind: store.MonitorKindHttp, Target: "https://x.example.com", IntervalSeconds: 30, TimeoutMs: 30000},
			[]string{"timeout_ms:lt_interval"}},
		{MonitorInput{Name: "a", Kind: store.MonitorKindHttp, Target: "https://x.example.com",
			Settings: Settings{Method: "POST", ExpectedStatus: []int{99}, Keyword: strings.Repeat("k", 201)}},
			[]string{"settings.method:oneof", "settings.expected_status[0]:range", "settings.keyword:max"}},
		{MonitorInput{Name: "a", Kind: store.MonitorKindHttp, Target: "https://x.example.com", Settings: Settings{Method: "HEAD", Keyword: "ok"}},
			[]string{"settings.keyword:keyword_needs_get"}},
		{MonitorInput{Name: "a", Kind: store.MonitorKindTcp, Target: "db.internal"}, []string{"target:host_port"}},
		{MonitorInput{Name: "a", Kind: store.MonitorKindSsl, Target: "bad host!", Settings: Settings{ExpiryDays: 91}},
			[]string{"target:host", "settings.expiry_days:range"}},
		{MonitorInput{Name: "a", Kind: "dns", Target: "x", Labels: []string{"Bad Label"}}, []string{"kind:oneof", "labels[0]:pattern"}},
	} {
		_, err := e.svc.CreateMonitor(e.dev.ctx, e.orgID, c.in)
		assert.ElementsMatch(t, c.want, fieldsOf(t, err), "%+v", c.in)
	}

	// Defaults and normalization.
	m := e.monitor(t, MonitorInput{Name: "shop", Kind: store.MonitorKindSsl, Target: "shop.example.com", Labels: []string{"Prod", "prod", "web"}})
	assert.Equal(t, "shop.example.com:443", m.Target)
	assert.Equal(t, DefaultExpiryDays, m.Settings.ExpiryDays)
	assert.Equal(t, int32(DefaultInterval), m.IntervalSeconds)
	assert.Equal(t, []string{"prod", "web"}, m.Labels)
	assert.Equal(t, StatusPending, m.Status)
	h := e.monitor(t, MonitorInput{Name: "api", Kind: store.MonitorKindHttp, Target: "https://api.example.com/health", Settings: Settings{ExpectedStatus: []int{204, 200, 200}}})
	assert.Equal(t, "GET", h.Settings.Method)
	assert.Equal(t, []int{200, 204}, h.Settings.ExpectedStatus)

	_, err := e.svc.CreateMonitor(e.dev.ctx, e.orgID, MonitorInput{Name: "shop", Kind: store.MonitorKindTcp, Target: "db:5432"})
	assert.Equal(t, apperr.CodeMonitorNameTaken, codeOf(t, err))
}

func TestPermissionsAndLifecycle(t *testing.T) {
	e := newEnv(t)
	in := MonitorInput{Name: "db", Kind: store.MonitorKindTcp, Target: "db.internal:5432"}
	_, err := e.svc.CreateMonitor(e.viewer.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.CreateMonitor(e.outsider.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
	m := e.monitor(t, in)

	list, err := e.svc.ListMonitors(e.viewer.ctx, e.orgID, "", "", "")
	require.NoError(t, err)
	assert.Len(t, list, 1)
	_, err = e.svc.GetMonitor(e.outsider.ctx, m.ID)
	assert.Equal(t, apperr.CodeMonitorNotFound, codeOf(t, err))
	_, err = e.svc.UpdateMonitor(e.viewer.ctx, m.ID, m.Version, in)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	// Pause: enabled=false; a stale version conflicts.
	off := false
	in.Enabled = &off
	_, err = e.svc.UpdateMonitor(e.dev.ctx, m.ID, m.Version+1, in)
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))
	m, err = e.svc.UpdateMonitor(e.dev.ctx, m.ID, m.Version, in)
	require.NoError(t, err)
	assert.Equal(t, StatusPaused, m.Status)
	paused, err := e.svc.ListMonitors(e.viewer.ctx, e.orgID, "", "", StatusPaused)
	require.NoError(t, err)
	assert.Len(t, paused, 1)

	require.NoError(t, e.svc.DeleteMonitor(e.dev.ctx, m.ID))
	assert.Equal(t, apperr.CodeMonitorNotFound, codeOf(t, e.svc.DeleteMonitor(e.dev.ctx, m.ID)))

	var actions []string
	rows, err := pgtest.Pool(t).Query(context.Background(), `SELECT action FROM audit_log WHERE resource_id = $1 ORDER BY id`, m.ID.String())
	require.NoError(t, err)
	for rows.Next() {
		var a string
		require.NoError(t, rows.Scan(&a))
		actions = append(actions, a)
	}
	assert.Equal(t, []string{"monitor.create", "monitor.update", "monitor.delete"}, actions)
}

func TestHTTPChecks(t *testing.T) {
	e := newEnv(t)
	var status atomic.Int32
	status.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "OpsHub-Monitor", r.UserAgent())
		switch r.URL.Path {
		case "/slow":
			time.Sleep(1500 * time.Millisecond)
		case "/moved":
			http.Redirect(w, r, "http://10.0.0.1/internal", http.StatusFound)
			return
		}
		w.WriteHeader(int(status.Load()))
		_, _ = io.WriteString(w, "status: all systems operational")
	}))
	t.Cleanup(srv.Close)

	up := e.monitor(t, MonitorInput{Name: "web", Kind: store.MonitorKindHttp, Target: srv.URL + "/health", Settings: Settings{Keyword: "operational"}})
	m := e.check(t, up.ID)
	assert.Equal(t, StatusUp, m.Status, m.LastError)
	require.NotNil(t, m.LastLatencyMs)
	assert.Nil(t, m.DownSince)

	status.Store(503)
	m = e.check(t, up.ID)
	assert.Equal(t, StatusDown, m.Status)
	assert.Equal(t, "unexpected status 503", m.LastError)
	require.NotNil(t, m.DownSince)
	since := *m.DownSince
	m = e.check(t, up.ID)
	assert.Equal(t, since, *m.DownSince, "down since the first failure")
	status.Store(200)
	m = e.check(t, up.ID)
	assert.Equal(t, StatusUp, m.Status)
	assert.Nil(t, m.DownSince)

	kw := e.monitor(t, MonitorInput{Name: "kw", Kind: store.MonitorKindHttp, Target: srv.URL, Settings: Settings{Keyword: "maintenance"}})
	assert.Equal(t, "keyword not found", e.check(t, kw.ID).LastError)

	only201 := e.monitor(t, MonitorInput{Name: "created", Kind: store.MonitorKindHttp, Target: srv.URL, Settings: Settings{ExpectedStatus: []int{201}}})
	assert.Equal(t, "unexpected status 200", e.check(t, only201.ID).LastError)

	// Redirects aren't followed (they could point inside the network); 3xx counts as up.
	moved := e.monitor(t, MonitorInput{Name: "moved", Kind: store.MonitorKindHttp, Target: srv.URL + "/moved"})
	assert.Equal(t, StatusUp, e.check(t, moved.ID).Status)

	slow := e.monitor(t, MonitorInput{Name: "slow", Kind: store.MonitorKindHttp, Target: srv.URL + "/slow", TimeoutMs: 1000})
	assert.Equal(t, "timed out", e.check(t, slow.ID).LastError)

	// The SSRF guard: private addresses need OPSHUB_OUTBOUND_ALLOWED_CIDRS.
	blocked := e.monitor(t, MonitorInput{Name: "internal", Kind: store.MonitorKindHttp, Target: "http://10.1.2.3:8080/"})
	assert.Contains(t, e.check(t, blocked.ID).LastError, "address not allowed")
}

func TestTCPAndSSLChecks(t *testing.T) {
	e := newEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	tcp := e.monitor(t, MonitorInput{Name: "db", Kind: store.MonitorKindTcp, Target: ln.Addr().String()})
	assert.Equal(t, StatusUp, e.check(t, tcp.ID).Status)
	closed := e.monitor(t, MonitorInput{Name: "closed", Kind: store.MonitorKindTcp, Target: "127.0.0.1:1"})
	assert.Contains(t, e.check(t, closed.ID).LastError, "connection failed")

	now := time.Now()
	good := e.ca.Serve(t, []string{"127.0.0.1"}, now.Add(-time.Hour), now.AddDate(0, 3, 0), nil)
	soon := e.ca.Serve(t, []string{"127.0.0.1"}, now.Add(-time.Hour), now.AddDate(0, 0, 5), nil)
	wrong := e.ca.Serve(t, []string{"other.example.com"}, now.Add(-time.Hour), now.AddDate(0, 3, 0), nil)
	for name, c := range map[string]struct {
		target string
		up     bool
		err    string
	}{
		"good":  {good, true, ""},
		"soon":  {soon, false, "certificate expires in 4 days"},
		"wrong": {wrong, false, "certificate is not valid for this name"},
	} {
		m := e.monitor(t, MonitorInput{Name: "tls-" + name, Kind: store.MonitorKindSsl, Target: c.target})
		got := e.check(t, m.ID)
		assert.Equal(t, c.up, got.Status == StatusUp, name)
		assert.Equal(t, c.err, got.LastError, name)
	}
}

func TestDueChecksAndResults(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	m := e.monitor(t, MonitorInput{Name: "web", Kind: store.MonitorKindHttp, Target: srv.URL})
	off := false
	e.monitor(t, MonitorInput{Name: "off", Kind: store.MonitorKindHttp, Target: srv.URL, Enabled: &off})

	// Due right after creation; claimed checks are rescheduled, so a second pass finds nothing.
	n, err := e.svc.CheckDue(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 1)
	got, err := e.svc.GetMonitor(e.viewer.ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusUp, got.Status)
	row, err := e.q.GetMonitor(ctx, m.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(time.Minute), row.NextCheckAt, 5*time.Second)
	var offChecks int
	require.NoError(t, pgtest.Pool(t).QueryRow(ctx, `SELECT count(*) FROM monitor_results r JOIN monitors m ON m.id = r.monitor_id
		WHERE m.organization_id = $1 AND m.name = 'off'`, e.orgID).Scan(&offChecks))
	assert.Zero(t, offChecks, "paused monitors aren't checked")

	// A day of history: 1 in 4 checks failed.
	start := time.Now().UTC().Truncate(time.Minute).Add(-24 * time.Hour)
	for i := range 24 * 60 {
		ts := start.Add(time.Duration(i) * time.Minute)
		if ts.Before(time.Date(ts.Year(), ts.Month(), 1, 0, 0, 0, 0, time.UTC)) && time.Now().UTC().Day() == 1 {
			continue // no partition for last month on a fresh database
		}
		_, err := pgtest.Pool(t).Exec(ctx, `INSERT INTO monitor_results (monitor_id, ts, up, latency_ms) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			m.ID, ts, i%4 != 0, 100+i%50)
		require.NoError(t, err)
	}
	res, err := e.svc.GetResults(e.viewer.ctx, m.ID, ResultsQuery{})
	require.NoError(t, err)
	assert.Equal(t, "raw", res.Source)
	assert.Equal(t, 300, res.StepSeconds)
	assert.LessOrEqual(t, len(res.Points), 300)
	require.NotNil(t, res.Uptime)
	assert.InDelta(t, 75, *res.Uptime, 1)
	assert.Len(t, res.Recent, RecentChecks)
	require.NotNil(t, res.Points[0].LatencyAvg)

	list, err := e.svc.ListMonitors(e.viewer.ctx, e.orgID, "", "", "")
	require.NoError(t, err)
	for _, l := range list {
		if l.ID == m.ID {
			require.NotNil(t, l.Uptime24h)
			assert.InDelta(t, 75, *l.Uptime24h, 1)
		}
	}

	_, err = e.svc.GetResults(e.viewer.ctx, m.ID, ResultsQuery{From: time.Now(), To: time.Now().Add(-time.Hour)})
	assert.Equal(t, []string{"from:before"}, fieldsOf(t, err))
	res, err = e.svc.GetResults(e.viewer.ctx, m.ID, ResultsQuery{From: time.Now().Add(-90 * 24 * time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "hourly", res.Source)

	// Rollups and partitions.
	require.NoError(t, e.svc.Maintain(ctx))
	var hours int
	require.NoError(t, pgtest.Pool(t).QueryRow(ctx, `SELECT count(*) FROM monitor_results_hourly WHERE monitor_id = $1`, m.ID).Scan(&hours))
	assert.GreaterOrEqual(t, hours, 2)
	var parts int
	require.NoError(t, pgtest.Pool(t).QueryRow(ctx, `SELECT count(*) FROM pg_inherits WHERE inhparent = 'monitor_results'::regclass`).Scan(&parts))
	assert.GreaterOrEqual(t, parts, 3)
}
