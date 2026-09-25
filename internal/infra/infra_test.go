package infra

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/store"
)

func TestAssets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	in := AssetInput{Kind: store.AssetKindServer, Name: " web-1 ", Address: "10.0.0.5", Tags: []string{"Prod", "web", "prod"},
		Metadata: map[string]string{"provider": "hetzner"}}

	_, err := e.svc.CreateAsset(e.viewer.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.CreateAsset(e.outsider.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
	_, err = e.svc.CreateAsset(e.dev.ctx, e.orgID, AssetInput{Kind: store.AssetKindDomain, Name: "", Address: "not a domain",
		Tags: []string{"Bad Tag"}, Metadata: map[string]string{"bad key!": "x"}, TLSPort: 70000})
	assert.ElementsMatch(t, []string{"name:range", "address:domain", "tags:pattern", "metadata.bad key!:pattern", "tls_port:range"}, fieldsOf(t, err))
	_, err = e.svc.CreateAsset(e.dev.ctx, e.orgID, AssetInput{Name: "x"})
	assert.Equal(t, []string{"kind:required"}, fieldsOf(t, err))

	a := e.asset(t, in)
	assert.Equal(t, "web-1", a.Name)
	assert.Equal(t, []string{"prod", "web"}, a.Tags)
	assert.Equal(t, StatusNoAgent, a.Status)
	require.NotNil(t, a.Agent)
	assert.False(t, a.Agent.Installed)
	_, err = e.svc.CreateAsset(e.dev.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeAssetNameTaken, codeOf(t, err))

	for kind, addr := range map[store.AssetKind]string{
		store.AssetKindDatabase: "db.internal:5432", store.AssetKindCluster: "https://k8s.internal:6443", store.AssetKindDomain: "Shop.Example.COM.",
	} {
		b := e.asset(t, AssetInput{Kind: kind, Name: string(kind) + "-1", Address: addr})
		if kind == store.AssetKindDomain {
			assert.Equal(t, "shop.example.com", b.Address, "domains are normalized")
			assert.Equal(t, StatusUnchecked, b.Status)
			assert.Equal(t, int32(443), b.TLSPort)
		} else {
			assert.Equal(t, StatusInventory, b.Status)
		}
	}

	// Filters.
	list, err := e.svc.ListAssets(e.viewer.ctx, e.orgID, AssetFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 4)
	list, err = e.svc.ListAssets(e.viewer.ctx, e.orgID, AssetFilter{Kind: "server", Tag: "PROD"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	list, err = e.svc.ListAssets(e.viewer.ctx, e.orgID, AssetFilter{Search: "db.int"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	list, err = e.svc.ListAssets(e.viewer.ctx, e.orgID, AssetFilter{Search: "%"})
	require.NoError(t, err)
	assert.Empty(t, list, "wildcards are literal")
	list, err = e.svc.ListAssets(e.viewer.ctx, e.orgID, AssetFilter{Status: StatusInventory})
	require.NoError(t, err)
	assert.Len(t, list, 2)
	_, err = e.svc.ListAssets(e.viewer.ctx, e.orgID, AssetFilter{Kind: "printer"})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))

	// Update with If-Match; other tenants see nothing.
	_, err = e.svc.GetAsset(e.outsider.ctx, a.ID)
	assert.Equal(t, apperr.CodeAssetNotFound, codeOf(t, err))
	_, err = e.svc.UpdateAsset(e.dev.ctx, a.ID, a.Version+1, in)
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))
	up, err := e.svc.UpdateAsset(e.dev.ctx, a.ID, a.Version, AssetInput{Name: "web-01", Address: "web-01.internal", Description: "Primary"})
	require.NoError(t, err)
	assert.Equal(t, "web-01", up.Name)
	assert.Equal(t, store.AssetKindServer, up.Kind, "the kind can't change")
	assert.Empty(t, up.Tags)

	require.NoError(t, e.svc.DeleteAsset(e.dev.ctx, a.ID))
	_, err = e.svc.GetAsset(e.dev.ctx, a.ID)
	assert.Equal(t, apperr.CodeAssetNotFound, codeOf(t, err))

	var n int
	require.NoError(t, e.svc.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action LIKE 'asset.%'`, e.orgID).Scan(&n))
	assert.Equal(t, 6, n) // 4 creates, update, delete
}

func TestAgentAndMetrics(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	srv := e.asset(t, AssetInput{Kind: store.AssetKindServer, Name: "web-1"})
	dom := e.asset(t, AssetInput{Kind: store.AssetKindDomain, Name: "shop", Address: "shop.example.com"})

	_, err := e.svc.IssueAgentToken(e.dev.ctx, dom.ID)
	assert.Equal(t, apperr.CodeAgentNotSupported, codeOf(t, err))
	_, err = e.svc.IssueAgentToken(e.viewer.ctx, srv.ID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	tok, err := e.svc.IssueAgentToken(e.dev.ctx, srv.ID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(tok.Token, "ohi_"))
	got, err := e.svc.GetAsset(e.viewer.ctx, srv.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPending, got.Status)
	assert.Equal(t, tok.TokenPrefix, got.Agent.TokenPrefix)
	edited, version := got.UpdatedAt, got.Version

	a, err := e.svc.AuthenticateAgent(ctx, tok.Token)
	require.NoError(t, err)
	for _, bad := range []string{"", "ohi_nope", "ohr_x_y"} {
		_, err := e.svc.AuthenticateAgent(ctx, bad)
		assert.Equal(t, apperr.CodeAgentTokenInvalid, codeOf(t, err))
	}
	res, err := e.svc.RecordHeartbeat(ctx, a, Heartbeat{Version: "1.0.0", Hostname: "web-1.internal", OS: "linux", Arch: "amd64",
		CPU: f32(40), Mem: f32(60), Disk: f32(80), Load: f32(0.5)})
	require.NoError(t, err)
	assert.Equal(t, 30, res.IntervalSeconds)
	got, err = e.svc.GetAsset(e.viewer.ctx, srv.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusOnline, got.Status)
	assert.Equal(t, "web-1.internal", got.Agent.Hostname)
	// Heartbeats are not edits: they leave version and updated_at alone.
	assert.Equal(t, version, got.Version)
	assert.True(t, edited.Equal(got.UpdatedAt), "heartbeat changed updated_at")
	require.NotNil(t, got.Metrics)
	assert.InDelta(t, 60, *got.Metrics.Mem, 0.01)

	// Rotating the token revokes the old one.
	tok2, err := e.svc.IssueAgentToken(e.dev.ctx, srv.ID)
	require.NoError(t, err)
	_, err = e.svc.AuthenticateAgent(ctx, tok.Token)
	assert.Equal(t, apperr.CodeAgentTokenInvalid, codeOf(t, err))
	_, err = e.svc.AuthenticateAgent(ctx, tok2.Token)
	require.NoError(t, err)

	// A server silent for longer than 90 s is offline.
	e.svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	got, err = e.svc.GetAsset(e.viewer.ctx, srv.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusOffline, got.Status)
	e.svc.now = time.Now

	// A day of samples (one per minute, CPU rising with the hour) and a sample without CPU.
	now := time.Now().UTC().Truncate(time.Minute)
	for i := 0; i < 24*60; i += 1 {
		ts := now.Add(-time.Duration(i) * time.Minute)
		_, err := e.svc.pool.Exec(ctx, `INSERT INTO asset_metrics (asset_id, ts, cpu_pct, mem_pct, disk_pct) VALUES ($1, $2, $3, 50, 70)
			ON CONFLICT DO NOTHING`, srv.ID, ts, float32(ts.Hour()))
		require.NoError(t, err)
	}
	series, err := e.svc.Metrics(e.viewer.ctx, srv.ID, MetricsQuery{})
	require.NoError(t, err)
	assert.Equal(t, "raw", series.Source)
	assert.Equal(t, 30, series.StepSeconds, "one hour → 30 s steps")
	series, err = e.svc.Metrics(e.viewer.ctx, srv.ID, MetricsQuery{From: now.Add(-24 * time.Hour), To: now})
	require.NoError(t, err)
	assert.Equal(t, 300, series.StepSeconds, "a day → 5 minute steps")
	assert.LessOrEqual(t, len(series.Points), 300)
	require.NotEmpty(t, series.Points)
	p := series.Points[0]
	require.NotNil(t, p.Mem)
	assert.InDelta(t, 50, *p.Mem, 0.01)
	series, err = e.svc.Metrics(e.viewer.ctx, srv.ID, MetricsQuery{From: now.Add(-time.Hour), To: now, Step: time.Hour})
	require.NoError(t, err)
	require.Len(t, series.Points, 2, "a one-hour step over a boundary")

	_, err = e.svc.Metrics(e.viewer.ctx, srv.ID, MetricsQuery{From: now, To: now.Add(-time.Hour)})
	assert.Equal(t, []string{"from:before"}, fieldsOf(t, err))
	_, err = e.svc.Metrics(e.viewer.ctx, srv.ID, MetricsQuery{From: now.Add(-24 * time.Hour), To: now, Step: 10 * time.Second})
	assert.Equal(t, []string{"step:range"}, fieldsOf(t, err), "too many points")
	_, err = e.svc.Metrics(e.outsider.ctx, srv.ID, MetricsQuery{})
	assert.Equal(t, apperr.CodeAssetNotFound, codeOf(t, err))

	// Maintenance rolls whole hours up; older ranges are served from the rollups.
	require.NoError(t, e.svc.Maintain(ctx))
	var hours int
	require.NoError(t, e.svc.pool.QueryRow(ctx, `SELECT count(*) FROM asset_metrics_hourly WHERE asset_id = $1`, srv.ID).Scan(&hours))
	assert.Equal(t, 3, hours, "the last three whole hours")
	_, err = e.svc.pool.Exec(ctx, `INSERT INTO asset_metrics_hourly (asset_id, hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_avg, disk_max, samples)
		VALUES ($1, $2, 10, 20, NULL, NULL, 30, 40, 60)`, srv.ID, now.Add(-60*24*time.Hour).Truncate(time.Hour))
	require.NoError(t, err)
	series, err = e.svc.Metrics(e.viewer.ctx, srv.ID, MetricsQuery{From: now.Add(-90 * 24 * time.Hour), To: now})
	require.NoError(t, err)
	assert.Equal(t, "hourly", series.Source)
	assert.GreaterOrEqual(t, series.StepSeconds, 3600)
	var old *Point
	for i := range series.Points {
		if series.Points[i].T.Before(now.Add(-50 * 24 * time.Hour)) {
			old = &series.Points[i]
		}
	}
	require.NotNil(t, old)
	assert.InDelta(t, 10, *old.CPU, 0.01)
	assert.Nil(t, old.Mem, "no data stays null")
}

func TestMetricPartitions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// An old partition (as if left from long ago) is dropped; upcoming ones exist.
	_, err := e.svc.pool.Exec(ctx, `CREATE TABLE asset_metrics_200001 PARTITION OF asset_metrics FOR VALUES FROM ('2000-01-01') TO ('2000-02-01')`)
	require.NoError(t, err)
	require.NoError(t, e.svc.Maintain(ctx))
	var names []string
	rows, err := e.svc.pool.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent WHERE p.relname = 'asset_metrics' ORDER BY 1`)
	require.NoError(t, err)
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Err())
	assert.NotContains(t, names, "asset_metrics_200001")
	next := time.Now().AddDate(0, 2, 0)
	assert.Contains(t, names, "asset_metrics_"+next.Format("200601"))
}

func TestCertificates(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	e.hosts["good.example.com"] = e.ca.serve(t, []string{"good.example.com"}, now.Add(-time.Hour), now.AddDate(0, 6, 0))
	e.hosts["soon.example.com"] = e.ca.serve(t, []string{"soon.example.com"}, now.Add(-time.Hour), now.Add(10*24*time.Hour))
	e.hosts["old.example.com"] = e.ca.serve(t, []string{"old.example.com"}, now.AddDate(-1, 0, 0), now.Add(-24*time.Hour))
	e.hosts["wrong.example.com"] = e.ca.serve(t, []string{"other.example.org"}, now.Add(-time.Hour), now.AddDate(0, 6, 0))
	stranger := newCA(t)
	e.hosts["self.example.com"] = stranger.serve(t, []string{"self.example.com"}, now.Add(-time.Hour), now.AddDate(0, 6, 0))

	ids := map[string]uuid.UUID{}
	for _, name := range []string{"good", "soon", "old", "wrong", "self", "down"} {
		ids[name] = e.asset(t, AssetInput{Kind: store.AssetKindDomain, Name: name, Address: name + ".example.com"}).ID
	}
	server := e.asset(t, AssetInput{Kind: store.AssetKindServer, Name: "web"})
	_, err := e.svc.CheckCertificate(e.dev.ctx, server.ID)
	assert.Equal(t, apperr.CodeBadRequest, codeOf(t, err))
	_, err = e.svc.CheckCertificate(e.viewer.ctx, ids["good"])
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	// The scheduled job checks everything due.
	require.NoError(t, e.svc.CheckDue(context.Background()))
	want := map[string]string{
		"good": StatusValid, "soon": StatusExpiring, "old": StatusExpired, "wrong": StatusError, "self": StatusError, "down": StatusError,
	}
	for name, status := range want {
		c, err := e.svc.GetCertificate(e.viewer.ctx, ids[name])
		require.NoError(t, err)
		require.NotNil(t, c, name)
		assert.Equal(t, status, c.Status, name)
		a, err := e.svc.GetAsset(e.viewer.ctx, ids[name])
		require.NoError(t, err)
		assert.Equal(t, status, a.Status, name)
	}
	good, _ := e.svc.GetCertificate(e.viewer.ctx, ids["good"])
	assert.Equal(t, []string{"good.example.com"}, good.DNSNames)
	assert.Contains(t, good.Issuer, "Test CA")
	assert.Len(t, good.Fingerprint, 64)
	assert.InDelta(t, 182, *good.DaysLeft, 3)
	wrong, _ := e.svc.GetCertificate(e.viewer.ctx, ids["wrong"])
	assert.Equal(t, "certificate is not valid for this name", wrong.Error)
	self, _ := e.svc.GetCertificate(e.viewer.ctx, ids["self"])
	assert.Equal(t, "certificate is not signed by a trusted authority", self.Error)
	down, _ := e.svc.GetCertificate(e.viewer.ctx, ids["down"])
	assert.Contains(t, down.Error, "connection failed")
	assert.Nil(t, down.NotAfter)

	// Nothing is due again right away.
	due, err := e.q.DueCertificateChecks(context.Background())
	require.NoError(t, err)
	assert.Empty(t, due)

	// The list, soonest expiry first; "expiring within" keeps expiring and failing ones.
	all, err := e.svc.ListCertificates(e.viewer.ctx, e.orgID, 0)
	require.NoError(t, err)
	assert.Len(t, all, 6)
	soon, err := e.svc.ListCertificates(e.viewer.ctx, e.orgID, 30*24*time.Hour)
	require.NoError(t, err)
	var names []string
	for _, c := range soon {
		names = append(names, c.AssetName)
	}
	assert.ElementsMatch(t, []string{"soon", "old", "wrong", "self", "down"}, names)
	_, err = e.svc.ListCertificates(e.outsider.ctx, e.orgID, 0)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))

	// A failed recheck keeps the known expiry; changing the address resets the check.
	delete(e.hosts, "good.example.com")
	c, err := e.svc.CheckCertificate(e.dev.ctx, ids["good"])
	require.NoError(t, err)
	assert.Contains(t, c.Error, "connection failed")
	require.NotNil(t, c.NotAfter, "the last known expiry is kept")
	a, err := e.svc.GetAsset(e.dev.ctx, ids["good"])
	require.NoError(t, err)
	_, err = e.svc.UpdateAsset(e.dev.ctx, a.ID, a.Version, AssetInput{Name: "good", Address: "new.example.com"})
	require.NoError(t, err)
	cert, err := e.svc.GetCertificate(e.viewer.ctx, ids["good"])
	require.NoError(t, err)
	assert.Nil(t, cert)
}
