package secret

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

func TestCreateValidationAndScopes(t *testing.T) {
	e := newEnv(t)
	ctx := e.admin.ctx

	for _, c := range []struct {
		in    CreateInput
		field string
		rule  string
	}{
		{CreateInput{Name: "lower", Value: "x"}, "name", "secret_name"},
		{CreateInput{Name: "9LIVES", Value: "x"}, "name", "secret_name"},
		{CreateInput{Name: "OPSHUB_TOKEN", Value: "x"}, "name", "reserved"},
		{CreateInput{Name: "OK"}, "value", "required"},
		{CreateInput{Name: "OK", Value: strings.Repeat("x", MaxValueBytes+1)}, "value", "max_bytes"},
		{CreateInput{Name: "OK", Value: "a\x00b"}, "value", "text"},
		{CreateInput{Name: "OK", Value: "x", Description: strings.Repeat("d", MaxDescription+1)}, "description", "max"},
		{CreateInput{Name: "OK", Value: "x", EnvironmentID: new(uuid.New())}, "environment_id", "exists"},
	} {
		_, err := e.svc.Create(ctx, e.projectID, c.in)
		require.Equal(t, apperr.CodeValidation, codeOf(t, err), c.in.Name)
		ae, _ := apperr.From(err)
		fields, _ := ae.Details["fields"].([]apperr.FieldError)
		require.NotEmpty(t, fields)
		assert.Equal(t, c.field, fields[0].Field, c.rule)
		assert.Equal(t, c.rule, fields[0].Rule)
	}

	all := e.create(t, "DB_URL", nil, "postgres://all")
	assert.Nil(t, all.EnvironmentID)
	assert.True(t, all.Protected, "project-wide secrets reach protected environments")
	assert.Equal(t, int32(1), all.CurrentVersion)
	st := e.create(t, "DB_URL", &e.staging, "postgres://staging")
	assert.Equal(t, "staging", *st.EnvironmentName)
	assert.False(t, st.Protected)
	_, err := e.svc.Create(ctx, e.projectID, CreateInput{Name: "DB_URL", EnvironmentID: &e.staging, Value: "again"})
	assert.Equal(t, apperr.CodeSecretNameTaken, codeOf(t, err))
	_, err = e.svc.Create(ctx, e.projectID, CreateInput{Name: "DB_URL", Value: "again"})
	assert.Equal(t, apperr.CodeSecretNameTaken, codeOf(t, err), "one project-wide secret per name")

	list, err := e.svc.List(e.dev.ctx, e.projectID, Filter{})
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Nil(t, list[0].EnvironmentID, "project-wide first")
	only, err := e.svc.List(e.dev.ctx, e.projectID, Filter{ProjectWide: true})
	require.NoError(t, err)
	assert.Len(t, only, 1)
	only, err = e.svc.List(e.dev.ctx, e.projectID, Filter{EnvironmentID: &e.staging})
	require.NoError(t, err)
	require.Len(t, only, 1)
	assert.Equal(t, st.ID, only[0].ID)

	// The value is stored encrypted: nothing in the table contains it.
	var leaked bool
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM secret_versions WHERE position('postgres://staging'::bytea IN ciphertext) > 0
		 OR position('postgres://staging'::bytea IN dek_enc) > 0)`).Scan(&leaked))
	assert.False(t, leaked)
	var kek string
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), `SELECT kek_id FROM secret_versions WHERE secret_id = $1`, st.ID).Scan(&kek))
	assert.Equal(t, "k1", kek)

	// Deleting an environment hides its secrets.
	require.NoError(t, e.projects.DeleteEnvironment(e.admin.ctx, e.staging))
	_, err = e.svc.Get(ctx, st.ID)
	assert.Equal(t, apperr.CodeSecretNotFound, codeOf(t, err))
}

func TestPermissions(t *testing.T) {
	e := newEnv(t)
	all := e.create(t, "API_KEY", nil, "all-value")
	prod := e.create(t, "API_KEY", &e.production, "prod-value")
	stg := e.create(t, "API_KEY", &e.staging, "staging-value")

	// Viewers and outsiders can't see secrets at all.
	_, err := e.svc.List(e.viewer.ctx, e.projectID, Filter{})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.List(e.outsider.ctx, e.projectID, Filter{})
	assert.Equal(t, apperr.CodeProjectNotFound, codeOf(t, err))
	_, err = e.svc.Get(e.outsider.ctx, stg.ID)
	assert.Equal(t, apperr.CodeSecretNotFound, codeOf(t, err))
	_, err = e.svc.Versions(e.viewer.ctx, stg.ID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	// Developers see everything and manage only unprotected environments' secrets.
	list, err := e.svc.List(e.dev.ctx, e.projectID, Filter{})
	require.NoError(t, err)
	manage := map[uuid.UUID]bool{}
	for _, s := range list {
		manage[s.ID] = s.CanManage
	}
	assert.Equal(t, map[uuid.UUID]bool{all.ID: false, prod.ID: false, stg.ID: true}, manage)

	for _, id := range []uuid.UUID{all.ID, prod.ID} {
		_, err = e.svc.Update(e.dev.ctx, id, 1, UpdateInput{Description: "x"})
		assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
		_, err = e.svc.Rotate(e.dev.ctx, id, RotateInput{Value: "new"})
		assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
		assert.Equal(t, apperr.CodeForbidden, codeOf(t, e.svc.Delete(e.dev.ctx, id)))
	}
	_, err = e.svc.Create(e.dev.ctx, e.projectID, CreateInput{Name: "NEW", Value: "x"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.Create(e.dev.ctx, e.projectID, CreateInput{Name: "NEW", EnvironmentID: &e.production, Value: "x"})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.Create(e.dev.ctx, e.projectID, CreateInput{Name: "NEW", EnvironmentID: &e.staging, Value: "x"})
	require.NoError(t, err)
	_, err = e.svc.Rotate(e.dev.ctx, stg.ID, RotateInput{Value: "staging-2"})
	require.NoError(t, err)

	// Admins manage all of them.
	list, err = e.svc.List(e.admin.ctx, e.projectID, Filter{})
	require.NoError(t, err)
	for _, s := range list {
		assert.True(t, s.CanManage, s.Name)
	}
	_, err = e.svc.Rotate(e.admin.ctx, prod.ID, RotateInput{Value: "prod-2"})
	require.NoError(t, err)
}

func TestUpdateRotateDeleteAndHistory(t *testing.T) {
	e := newEnv(t)
	ctx := e.admin.ctx
	s := e.create(t, "TOKEN", &e.staging, "value-one")

	_, err := e.svc.Update(ctx, s.ID, s.Version+1, UpdateInput{Description: "stale"})
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))
	s, err = e.svc.Update(ctx, s.ID, s.Version, UpdateInput{Description: "  Deploy token  "})
	require.NoError(t, err)
	assert.Equal(t, "Deploy token", s.Description)
	assert.Equal(t, int32(1), s.CurrentVersion)

	_, err = e.svc.Rotate(ctx, s.ID, RotateInput{})
	assert.Equal(t, apperr.CodeValidation, codeOf(t, err))
	s, err = e.svc.Rotate(ctx, s.ID, RotateInput{Value: "value-two"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), s.CurrentVersion)
	s, err = e.svc.Rotate(e.admin.ctx, s.ID, RotateInput{Value: "value-three"})
	require.NoError(t, err)
	assert.Equal(t, int32(3), s.CurrentVersion)

	hist, err := e.svc.Versions(e.dev.ctx, s.ID)
	require.NoError(t, err)
	require.Len(t, hist, 3)
	assert.Equal(t, int32(3), hist[0].Version)
	assert.True(t, hist[0].Current)
	assert.Nil(t, hist[0].DestroyedAt)
	for _, h := range hist[1:] {
		assert.False(t, h.Current)
		assert.NotNil(t, h.DestroyedAt, "rotation destroys older values")
		assert.Equal(t, e.admin.id, *h.CreatedBy)
		assert.NotNil(t, h.CreatedByName)
	}
	var values int
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		`SELECT count(*) FROM secret_versions WHERE secret_id = $1 AND ciphertext IS NOT NULL`, s.ID).Scan(&values))
	assert.Equal(t, 1, values)

	require.NoError(t, e.svc.Delete(ctx, s.ID))
	_, err = e.svc.Get(ctx, s.ID)
	assert.Equal(t, apperr.CodeSecretNotFound, codeOf(t, err))
	assert.Equal(t, apperr.CodeSecretNotFound, codeOf(t, e.svc.Delete(ctx, s.ID)))
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		`SELECT count(*) FROM secret_versions WHERE secret_id = $1 AND ciphertext IS NOT NULL`, s.ID).Scan(&values))
	assert.Zero(t, values, "deletion destroys every value")
	// The name is free again.
	e.create(t, "TOKEN", &e.staging, "reborn")

	// Every change is audited, never with a value.
	rows, err := pgtest.Pool(t).Query(context.Background(),
		`SELECT action, coalesce(before::text, ''), coalesce(after::text, ''), metadata::text FROM audit_log WHERE resource_type = 'secret' AND resource_id = $1 ORDER BY id`, s.ID.String())
	require.NoError(t, err)
	var actions []string
	for rows.Next() {
		var action, before, after, meta string
		require.NoError(t, rows.Scan(&action, &before, &after, &meta))
		actions = append(actions, action)
		for _, v := range []string{"value-one", "value-two", "value-three"} {
			assert.NotContains(t, before+after+meta, v)
		}
		assert.Contains(t, meta, `"name": "TOKEN"`)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"secret.create", "secret.update", "secret.rotate", "secret.rotate", "secret.delete"}, actions)
}

func TestLimit(t *testing.T) {
	e := newEnv(t)
	_, err := pgtest.Pool(t).Exec(context.Background(), `INSERT INTO secrets (project_id, name)
		SELECT $1, 'S_' || g FROM generate_series(1, $2::integer) g`, e.projectID, MaxPerProject)
	require.NoError(t, err)
	_, err = e.svc.Create(e.admin.ctx, e.projectID, CreateInput{Name: "ONE_MORE", Value: "x"})
	assert.Equal(t, apperr.CodeSecretLimit, codeOf(t, err))
}

const secretsPipeline = `version: 1
stages: [build]
jobs:
  plain:
    stage: build
    image: alpine
    steps: [env]
  wants:
    stage: build
    image: alpine
    secrets: [API_KEY, DB_URL]
    steps: [env]
  staged:
    stage: build
    image: alpine
    environment: staging
    secrets: [API_KEY]
    steps: [env]
`

func TestGateAndInjection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Missing names fail the job before any runner sees it, naming what's missing.
	jobs := e.run(t, store.RunTriggerManual, secretsPipeline)
	assert.Equal(t, store.JobStatusQueued, jobs["plain"].Status)
	assert.Equal(t, store.JobStatusFailed, jobs["wants"].Status)
	assert.Equal(t, pipeline.ReasonSecretNotFound, reasonOf(jobs["wants"]))
	chunks, err := e.q.ListLogChunks(ctx, store.ListLogChunksParams{JobID: jobs["wants"].ID, AfterSeq: -1, PageSize: 10})
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
	assert.Contains(t, chunks[0].Content, "API_KEY, DB_URL")

	all := e.create(t, "API_KEY", nil, "all-key")
	stg := e.create(t, "API_KEY", &e.staging, "staging-key")
	e.create(t, "DB_URL", nil, "postgres://u:p@db/app\nline-two-secret")

	jobs = e.run(t, store.RunTriggerManual, secretsPipeline)
	for _, n := range []string{"plain", "wants", "staged"} {
		assert.Equal(t, store.JobStatusQueued, jobs[n].Status, n)
	}
	got, err := e.forJob(t, jobs["plain"])
	require.NoError(t, err)
	assert.Empty(t, got.Values)
	assert.Nil(t, got.MasksEnc)

	got, err = e.forJob(t, jobs["wants"])
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"API_KEY": "all-key", "DB_URL": "postgres://u:p@db/app\nline-two-secret"}, got.Values)
	assert.ElementsMatch(t, []string{"all-key", "postgres://u:p@db/app\nline-two-secret", "postgres://u:p@db/app", "line-two-secret"}, got.Masks)

	got, err = e.forJob(t, jobs["staged"])
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"API_KEY": "staging-key"}, got.Values, "the environment's secret wins")

	// Each decryption is audited as secret.read by the runner.
	var reads int
	require.NoError(t, pgtest.Pool(t).QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'secret.read' AND actor_type = 'runner'
		AND metadata->>'job_id' = $1 AND resource_id = $2`, jobs["staged"].ID.String(), stg.ID.String()).Scan(&reads))
	assert.Equal(t, 1, reads)
	require.NoError(t, pgtest.Pool(t).QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'secret.read' AND resource_id = $1`,
		all.ID.String()).Scan(&reads))
	assert.Equal(t, 1, reads)

	// Masks sealed for the job token row open for that job only (got is the staged job's).
	_, err = pgtest.Pool(t).Exec(ctx, `INSERT INTO job_tokens (job_id, token_hash, expires_at, masks_enc) VALUES ($1, $2, now() + interval '1 hour', $3)`,
		jobs["wants"].ID, []byte(uuid.NewString()), got.MasksEnc)
	require.NoError(t, err)
	_, err = e.svc.JobMasks(ctx, e.q, jobs["wants"].ID)
	require.Error(t, err, "sealed for another job")
	_, err = pgtest.Pool(t).Exec(ctx, `UPDATE job_tokens SET job_id = $1 WHERE job_id = $2`, jobs["staged"].ID, jobs["wants"].ID)
	require.NoError(t, err)
	m, err := e.svc.JobMasks(ctx, e.q, jobs["staged"].ID)
	require.NoError(t, err)
	assert.Equal(t, pipeline.Masker{"staging-key"}, m)
	m, err = e.svc.JobMasks(ctx, e.q, jobs["plain"].ID)
	require.NoError(t, err)
	assert.Nil(t, m)

	// A secret deleted after the job became ready: the claim reports it.
	require.NoError(t, e.svc.Delete(e.admin.ctx, stg.ID))
	got, err = e.forJob(t, jobs["staged"])
	require.NoError(t, err, "falls back to the project-wide secret")
	assert.Equal(t, "all-key", got.Values["API_KEY"])
	require.NoError(t, e.svc.Delete(e.admin.ctx, all.ID))
	_, err = e.forJob(t, jobs["staged"])
	var ue *UnavailableError
	require.ErrorAs(t, err, &ue)
	assert.Equal(t, pipeline.ReasonSecretNotFound, ue.Reason)
}

func TestPullRequestRunsGetNoSecrets(t *testing.T) {
	e := newEnv(t)
	e.create(t, "API_KEY", nil, "k")
	e.create(t, "DB_URL", nil, "d")
	jobs := e.run(t, store.RunTriggerPullRequest, secretsPipeline)
	assert.Equal(t, store.JobStatusQueued, jobs["plain"].Status)
	assert.Equal(t, store.JobStatusFailed, jobs["wants"].Status)
	assert.Equal(t, pipeline.ReasonSecretsNotAllowed, reasonOf(jobs["wants"]))
	// Even if such a job were queued, the claim refuses.
	_, err := pgtest.Pool(t).Exec(context.Background(), `UPDATE pipeline_jobs SET status = 'queued', failure_reason = NULL WHERE id = $1`, jobs["wants"].ID)
	require.NoError(t, err)
	_, err = e.forJob(t, jobs["wants"])
	var ue *UnavailableError
	require.ErrorAs(t, err, &ue)
	assert.Equal(t, pipeline.ReasonSecretsNotAllowed, ue.Reason)
}

func TestUnreadableValue(t *testing.T) {
	e := newEnv(t)
	s := e.create(t, "API_KEY", nil, "k")
	e.create(t, "DB_URL", nil, "d")
	// Corrupt the wrapped DEK, as if its master key were gone.
	_, err := pgtest.Pool(t).Exec(context.Background(), `UPDATE secret_versions SET dek_enc = '\x0102'::bytea WHERE secret_id = $1`, s.ID)
	require.NoError(t, err)
	jobs := e.run(t, store.RunTriggerManual, secretsPipeline)
	_, err = e.forJob(t, jobs["wants"])
	var ue *UnavailableError
	require.ErrorAs(t, err, &ue)
	assert.Contains(t, ue.Message, "can't be decrypted")
}

func TestMaskValues(t *testing.T) {
	assert.Equal(t, []string{"single"}, maskValues("single"))
	assert.Equal(t, []string{"-----BEGIN KEY-----\nabcdef\nab\n-----END KEY-----", "-----BEGIN KEY-----", "abcdef", "-----END KEY-----"},
		maskValues("-----BEGIN KEY-----\nabcdef\nab\n-----END KEY-----"))
	raw, _ := json.Marshal(maskValues("a\nb"))
	assert.JSONEq(t, `["a\nb"]`, string(raw), "short lines aren't masked on their own")
}
