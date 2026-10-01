package seed

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

// TestShowcase seeds everything twice and checks the showcase organization has every state
// its pages can show, that the 2FA account works, and that the second run changes nothing.
func TestShowcase(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{7}, 32)}})
	require.NoError(t, err)
	opts := func(out *bytes.Buffer) Options {
		return Options{
			Password: "angkor-wat-sunrise-2026", Out: out, Keys: keys, LogRetentionDays: 30,
			Hasher: authn.NewHasher(authn.Argon2Params{MemoryKiB: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}),
		}
	}
	var out bytes.Buffer
	require.NoError(t, Run(ctx, pool, opts(&out)))
	t.Log(out.String())

	var org uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, "SELECT id FROM organizations WHERE slug = $1", ShowcaseOrgSlug).Scan(&org))
	// distinct returns the distinct values of a column for the showcase organization.
	distinct := func(query string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, query, org)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			require.NoError(t, rows.Scan(&v))
			out = append(out, v)
		}
		require.NoError(t, rows.Err())
		return out
	}
	count := func(query string) int {
		t.Helper()
		var n int
		require.NoError(t, pool.QueryRow(ctx, query, org).Scan(&n))
		return n
	}

	assert.ElementsMatch(t, []string{"owner", "admin", "developer", "viewer"}, distinct("SELECT DISTINCT role::text FROM organization_members WHERE organization_id = $1"))
	assert.Equal(t, 8, count("SELECT count(*) FROM organization_members WHERE organization_id = $1"))
	assert.Equal(t, 3, count("SELECT count(*) FROM invitations WHERE organization_id = $1"))
	assert.Equal(t, 3, count("SELECT count(*) FROM teams WHERE organization_id = $1"))
	assert.Equal(t, 2, count("SELECT count(*) FROM repositories r JOIN projects p ON p.id = r.project_id WHERE p.organization_id = $1"))
	assert.ElementsMatch(t, []string{"queued", "running", "waiting", "succeeded", "failed", "canceled"},
		distinct("SELECT DISTINCT status::text FROM pipeline_runs WHERE organization_id = $1"))
	assert.ElementsMatch(t, []string{"push", "pull_request", "tag", "manual", "schedule"},
		distinct("SELECT DISTINCT trigger::text FROM pipeline_runs WHERE organization_id = $1"))
	assert.Positive(t, count("SELECT count(*) FROM job_approvals a JOIN pipeline_jobs j ON j.id = a.job_id WHERE j.organization_id = $1 AND a.decision = 'rejected'"))
	assert.Positive(t, count("SELECT count(*) FROM pipeline_runs WHERE organization_id = $1 AND rerun_of IS NOT NULL"))
	assert.Equal(t, 4, count("SELECT count(*) FROM runners WHERE organization_id = $1"))
	assert.ElementsMatch(t, []string{"ssh", "docker", "kubernetes"}, distinct("SELECT DISTINCT kind::text FROM deploy_targets WHERE organization_id = $1"))
	assert.ElementsMatch(t, []string{"succeeded", "failed"}, distinct("SELECT DISTINCT status::text FROM deployments WHERE organization_id = $1"))
	assert.Positive(t, count("SELECT count(*) FROM deployments WHERE organization_id = $1 AND rollback_of_id IS NOT NULL"))
	assert.Positive(t, count("SELECT count(*) FROM deployments WHERE organization_id = $1 AND strategy = 'blue_green'"))
	assert.Equal(t, 3, count("SELECT max(s.current_version) FROM secrets s JOIN projects p ON p.id = s.project_id WHERE p.organization_id = $1"))
	assert.ElementsMatch(t, []string{"server", "cluster", "database", "domain"}, distinct("SELECT DISTINCT kind::text FROM infra_assets WHERE organization_id = $1"))
	assert.Equal(t, 4, count("SELECT count(*) FROM ssl_certificates c JOIN infra_assets a ON a.id = c.asset_id WHERE a.organization_id = $1"))
	assert.Positive(t, count("SELECT count(*) FROM monitor_results_hourly h JOIN monitors m ON m.id = h.monitor_id WHERE m.organization_id = $1"))
	assert.Positive(t, count("SELECT count(*) FROM asset_metrics_hourly h JOIN infra_assets a ON a.id = h.asset_id WHERE a.organization_id = $1"))
	assert.ElementsMatch(t, []string{"email", "slack", "telegram", "webhook"}, distinct("SELECT DISTINCT kind::text FROM notification_channels WHERE organization_id = $1"))
	assert.ElementsMatch(t, []string{"monitor_down", "monitor_latency", "asset_metric", "asset_offline", "certificate"},
		distinct("SELECT DISTINCT kind::text FROM alert_rules WHERE organization_id = $1"))
	assert.ElementsMatch(t, []string{"pending", "firing", "resolved"}, distinct("SELECT DISTINCT status::text FROM alerts WHERE organization_id = $1"))
	assert.Positive(t, count("SELECT count(*) FROM alerts WHERE organization_id = $1 AND status = 'firing' AND acknowledged_at IS NOT NULL"))
	assert.ElementsMatch(t, []string{"fired", "notified", "notify_failed", "escalated", "acknowledged", "silenced", "resolved"},
		distinct("SELECT DISTINCT e.kind FROM alert_events e JOIN alerts a ON a.id = e.alert_id WHERE a.organization_id = $1"))
	assert.Equal(t, 4, count("SELECT count(*) FROM silences WHERE organization_id = $1"))
	assert.ElementsMatch(t, []string{"debug", "info", "warn", "error"}, distinct("SELECT DISTINCT level::text FROM log_entries WHERE organization_id = $1"))
	assert.ElementsMatch(t, []string{"service", "job", "deployment"}, distinct("SELECT DISTINCT source::text FROM log_entries WHERE organization_id = $1"))
	assert.GreaterOrEqual(t, count("SELECT count(*) FROM audit_log WHERE organization_id = $1 AND created_at < now() - interval '1 day'"), 25)

	// The 2FA account: the printed key is the stored one and the printed codes are valid.
	var secure uuid.UUID
	var enc []byte
	require.NoError(t, pool.QueryRow(ctx, "SELECT id, totp_secret_enc FROM users WHERE email = $1 AND totp_enabled_at IS NOT NULL",
		ShowcaseTwoFactorEmail).Scan(&secure, &enc))
	secret, err := keys.Decrypt(enc, []byte("totp:"+secure.String()))
	require.NoError(t, err)
	assert.Contains(t, out.String(), string(secret))
	m := regexp.MustCompile(`recovery codes: (.+)`).FindStringSubmatch(out.String())
	require.Len(t, m, 2)
	codes := strings.Fields(m[1])
	require.Len(t, codes, 10)
	var unused int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM user_recovery_codes WHERE user_id = $1 AND used_at IS NULL AND code_hash = ANY($2)",
		secure, [][]byte{crypto.HashToken(strings.ReplaceAll(codes[1], "-", "")), crypto.HashToken(strings.ReplaceAll(codes[9], "-", ""))}).Scan(&unused))
	assert.Equal(t, 2, unused)

	// Sign-in still opens angkor-tech: it sorts first for every demo user.
	var first string
	require.NoError(t, pool.QueryRow(ctx, `SELECT o.slug FROM organizations o JOIN organization_members m ON m.organization_id = o.id
		JOIN users u ON u.id = m.user_id WHERE u.email = 'dev@demo.opshub.local' ORDER BY o.name, o.id LIMIT 1`).Scan(&first))
	assert.Equal(t, DemoOrgSlug, first)

	// A second run leaves everything as it was.
	runs := count("SELECT count(*) FROM pipeline_runs WHERE organization_id = $1")
	out.Reset()
	require.NoError(t, Run(ctx, pool, opts(&out)))
	assert.Contains(t, out.String(), "skipping the showcase")
	assert.Equal(t, runs, count("SELECT count(*) FROM pipeline_runs WHERE organization_id = $1"))
}
