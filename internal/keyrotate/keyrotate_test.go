package keyrotate

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

func ring(t *testing.T, ids ...string) *crypto.KeyRing {
	t.Helper()
	var keys []config.NamedKey
	for _, id := range ids {
		keys = append(keys, config.NamedKey{ID: id, Key: bytes.Repeat([]byte(id[len(id)-1:]), 32)})
	}
	kr, err := crypto.NewKeyRing(keys)
	require.NoError(t, err)
	return kr
}

// fixture writes one encrypted value into every column, sealed with kr.
type fixture struct {
	userID, repoID, targetID, secretID, jobID uuid.UUID
}

func seed(t *testing.T, pool *pgxpool.Pool, kr *crypto.KeyRing) fixture {
	t.Helper()
	ctx := context.Background()
	f := fixture{repoID: uuid.New(), targetID: uuid.New(), secretID: uuid.New()}
	enc := func(v string, aad []byte) []byte {
		b, err := kr.Encrypt([]byte(v), aad)
		require.NoError(t, err)
		return b
	}
	suffix := uuid.NewString()[:8]
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO users (email, display_name, locale, timezone) VALUES ($1, 'Rotator', 'en', 'UTC') RETURNING id`,
		"kr-"+suffix+"@example.com").Scan(&f.userID))
	_, err := pool.Exec(ctx, `UPDATE users SET totp_secret_enc = $2, totp_pending_enc = $3 WHERE id = $1`, f.userID,
		enc("TOTP-ACTIVE", []byte("totp:"+f.userID.String())), enc("TOTP-PENDING", []byte("totp:"+f.userID.String())))
	require.NoError(t, err)
	var orgID, projectID, runID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, 'Keys') RETURNING id`, "kr-"+suffix).Scan(&orgID))
	// Rotation scans whole tables: remove this fixture so tests don't see each other's keys.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, f.userID)
	})
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO projects (organization_id, slug, name) VALUES ($1, 'api', 'API') RETURNING id`, orgID).Scan(&projectID))
	_, err = pool.Exec(ctx, `INSERT INTO repositories (id, project_id, provider, full_name, external_id, web_url, clone_url, default_branch,
		access_token_enc, webhook_secret_enc, webhook_mode) VALUES ($1, $2, 'github', 'acme/api', '1', 'https://x', 'https://x.git', 'main', $3, $4, 'manual')`,
		f.repoID, projectID, enc("ghp_token", f.repoID[:]), enc("whsec", f.repoID[:]))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO deploy_targets (id, organization_id, name, kind, credentials_enc) VALUES ($1, $2, 'web', 'ssh', $3)`,
		f.targetID, orgID, enc(`{"private_key":"k"}`, f.targetID[:]))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO secrets (id, project_id, name, current_version) VALUES ($1, $2, 'TOKEN', 2)`, f.secretID, projectID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO secret_versions (secret_id, version, destroyed_at) VALUES ($1, 1, now())`, f.secretID)
	require.NoError(t, err)
	e, err := kr.SealEnvelope([]byte("secret-value"), fmt.Appendf(nil, "secret:%s:%d", f.secretID, 2))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO secret_versions (secret_id, version, ciphertext, nonce, dek_enc, kek_id) VALUES ($1, 2, $2, $3, $4, $5)`,
		f.secretID, e.Ciphertext, e.Nonce, e.DEKEnc, e.KEKID)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_runs (organization_id, project_id, number, trigger, ref, commit_sha)
		VALUES ($1, $2, 1, 'manual', 'refs/heads/main', 'abc1234') RETURNING id`, orgID, projectID).Scan(&runID))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_jobs (run_id, project_id, organization_id, name, stage, stage_index, condition, spec, timeout_seconds)
		VALUES ($1, $2, $3, 'build', 'build', 0, 'on_success', '{}', 60) RETURNING id`, runID, projectID, orgID).Scan(&f.jobID))
	_, err = pool.Exec(ctx, `INSERT INTO job_tokens (job_id, token_hash, expires_at, masks_enc) VALUES ($1, $2, now() + interval '1 hour', $3)`,
		f.jobID, []byte(suffix), enc(`["secret-value"]`, []byte("job-masks:"+f.jobID.String())))
	require.NoError(t, err)
	return f
}

// readAll decrypts every fixture value with kr.
func readAll(t *testing.T, pool *pgxpool.Pool, kr *crypto.KeyRing, f fixture) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	dec := func(name string, v, aad []byte) {
		pt, err := kr.Decrypt(v, aad)
		require.NoError(t, err, name)
		out[name] = string(pt)
	}
	var a, b []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT totp_secret_enc, totp_pending_enc FROM users WHERE id = $1`, f.userID).Scan(&a, &b))
	dec("totp", a, []byte("totp:"+f.userID.String()))
	dec("totp_pending", b, []byte("totp:"+f.userID.String()))
	require.NoError(t, pool.QueryRow(ctx, `SELECT access_token_enc, webhook_secret_enc FROM repositories WHERE id = $1`, f.repoID).Scan(&a, &b))
	dec("access_token", a, f.repoID[:])
	dec("webhook_secret", b, f.repoID[:])
	require.NoError(t, pool.QueryRow(ctx, `SELECT credentials_enc FROM deploy_targets WHERE id = $1`, f.targetID).Scan(&a))
	dec("credentials", a, f.targetID[:])
	require.NoError(t, pool.QueryRow(ctx, `SELECT masks_enc FROM job_tokens WHERE job_id = $1`, f.jobID).Scan(&a))
	dec("masks", a, []byte("job-masks:"+f.jobID.String()))
	var e crypto.Envelope
	require.NoError(t, pool.QueryRow(ctx, `SELECT ciphertext, nonce, dek_enc, kek_id FROM secret_versions WHERE secret_id = $1 AND version = 2`, f.secretID).
		Scan(&e.Ciphertext, &e.Nonce, &e.DEKEnc, &e.KEKID))
	pt, err := kr.OpenEnvelope(e, fmt.Appendf(nil, "secret:%s:%d", f.secretID, 2))
	require.NoError(t, err)
	out["secret"] = string(pt)
	out["kek_id"] = e.KEKID
	return out
}

func counts(results []Result) map[string][3]int {
	out := map[string][3]int{}
	for _, r := range results {
		out[r.Column] = [3]int{r.Scanned, r.Rewrapped, r.Failed}
	}
	return out
}

func TestRotate(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	f := seed(t, pool, ring(t, "m1"))
	seed(t, pool, ring(t, "m1")) // two rows per column, one per page
	old := batch
	batch = 1
	t.Cleanup(func() { batch = old })

	// Nothing to do while the active key is still the old one.
	_, err := Rotate(ctx, pool, ring(t, "m1"))
	require.NoError(t, err)

	both := ring(t, "m2", "m1")
	results, err := Rotate(ctx, pool, both)
	require.NoError(t, err)
	got := counts(results)
	require.Len(t, got, len(columns))
	for col, c := range got {
		assert.GreaterOrEqual(t, c[1], 2, "%s rewrapped across pages", col)
		assert.Zero(t, c[2], col)
	}

	values := readAll(t, pool, ring(t, "m2"), f)
	assert.Equal(t, map[string]string{
		"totp": "TOTP-ACTIVE", "totp_pending": "TOTP-PENDING", "access_token": "ghp_token", "webhook_secret": "whsec",
		"credentials": `{"private_key":"k"}`, "masks": `["secret-value"]`, "secret": "secret-value", "kek_id": "m2",
	}, values, "readable with the new key alone")

	// Idempotent: a second run re-encrypts nothing.
	results, err = Rotate(ctx, pool, both)
	require.NoError(t, err)
	for col, c := range counts(results) {
		assert.Zero(t, c[1], col)
	}
}

func TestRotateReportsUnreadableRows(t *testing.T) {
	pool := pgtest.Pool(t)
	f := seed(t, pool, ring(t, "m7"))
	// The key that wrote the data isn't configured: nothing can be rewrapped.
	results, err := Rotate(context.Background(), pool, ring(t, "m8"))
	require.ErrorIs(t, err, ErrUnreadable)
	c := counts(results)
	assert.GreaterOrEqual(t, c["deploy_targets.credentials_enc"][2], 1)
	assert.Zero(t, c["deploy_targets.credentials_enc"][1])
	// The rows are untouched and still open with the old key.
	values := readAll(t, pool, ring(t, "m7"), f)
	assert.Equal(t, "m7", values["kek_id"])
}
