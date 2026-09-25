package runners

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/secret"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

const secretJob = `version: 1
stages: [build]
jobs:
  build:
    stage: build
    image: alpine:3
    secrets: [NPM_TOKEN]
    variables:
      NPM_TOKEN: not-the-secret
    steps: [npm publish]
`

func TestClaimInjectsAndMasksSecrets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: []byte(strings.Repeat("s", 32))}})
	require.NoError(t, err)
	secrets := secret.NewService(pgtest.Pool(t), keys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.svc.SetSecrets(secrets)
	_, err = secrets.Create(e.admin.ctx, e.projectID, secret.CreateInput{Name: "NPM_TOKEN", Value: "npm_s3cr3t-value"})
	require.NoError(t, err)
	setPipeline(t, e, secretJob)
	r, _ := e.register(t, "builder", nil, 1)

	e.trigger(t)
	a := e.claim(t, r)
	require.NotNil(t, a)
	assert.Equal(t, map[string]string{"NPM_TOKEN": "npm_s3cr3t-value"}, a.Secrets)
	assert.Equal(t, []string{"npm_s3cr3t-value"}, a.Masks)
	assert.Equal(t, "not-the-secret", a.Variables["NPM_TOKEN"], "variables are sent as written; the runner lets the secret win (jobEnv)")

	// The API masks what it stores, even if the runner didn't.
	j := e.job(t, a)
	require.NoError(t, e.svc.AppendLog(ctx, j, LogInput{Seq: 0, Content: "token=npm_s3cr3t-value ok\n"}))
	page, err := e.pipelines.Logs(e.admin.ctx, j.ID, -1, 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "token=•••••• ok\n", page.Items[0].Content)

	var reads int
	require.NoError(t, pgtest.Pool(t).QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'secret.read' AND metadata->>'job_id' = $1
		AND metadata->>'runner_id' = $2`, j.ID.String(), r.ID.String()).Scan(&reads))
	assert.Equal(t, 1, reads)
}

func TestClaimFailsJobWhoseSecretWasDeleted(t *testing.T) {
	e := newEnv(t)
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: []byte(strings.Repeat("s", 32))}})
	require.NoError(t, err)
	secrets := secret.NewService(pgtest.Pool(t), keys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.svc.SetSecrets(secrets)
	s, err := secrets.Create(e.admin.ctx, e.projectID, secret.CreateInput{Name: "NPM_TOKEN", Value: "npm_s3cr3t-value"})
	require.NoError(t, err)
	setPipeline(t, e, secretJob)
	r, _ := e.register(t, "builder", nil, 1)

	run := e.trigger(t)
	require.NoError(t, secrets.Delete(e.admin.ctx, s.ID)) // after the job was queued
	assert.Nil(t, e.claim(t, r), "nothing to run")

	jobs, err := e.q.CurrentJobs(context.Background(), run.ID)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	status, reason := e.jobStatus(t, jobs[0].ID)
	assert.Equal(t, store.JobStatusFailed, status)
	assert.Equal(t, pipeline.ReasonSecretNotFound, reason)
	page, err := e.pipelines.Logs(e.admin.ctx, jobs[0].ID, -1, 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	assert.Contains(t, page.Items[0].Content, "NPM_TOKEN")
}

func TestClaimWithoutSecretsModule(t *testing.T) {
	e := newEnv(t)
	setPipeline(t, e, secretJob)
	r, _ := e.register(t, "builder", nil, 1)
	run := e.trigger(t)
	// The gate already fails the job: no such secret exists.
	status, reason := e.jobStatus(t, run.Jobs[0].ID)
	assert.Equal(t, store.JobStatusFailed, status)
	assert.Equal(t, pipeline.ReasonSecretNotFound, reason)
	assert.Nil(t, e.claim(t, r))
}
