package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/store"
)

// UnavailableError means a claimed job can't be given its secrets; the job must fail with
// Reason (a pipeline failure reason) and Message in its log.
type UnavailableError struct {
	Reason  string
	Message string
}

func (e *UnavailableError) Error() string { return "secrets unavailable: " + e.Message }

// JobSecrets is what a runner receives with a job.
type JobSecrets struct {
	Values map[string]string
	// Masks are the values to hide in the job's log.
	Masks []string
	// MasksEnc is Masks sealed for the job token row, so the API can mask stored output too.
	MasksEnc []byte
}

func masksAAD(jobID uuid.UUID) []byte { return []byte("job-masks:" + jobID.String()) }

// ForJob decrypts the secrets a job lists, inside the claim transaction, and audits one
// secret.read per secret. Pull-request runs and names without a secret return
// *UnavailableError (the gate checked both when the job became ready; this covers changes
// since).
func (s *Service) ForJob(ctx context.Context, q *store.Queries, j store.PipelineJob, runnerID uuid.UUID) (JobSecrets, error) {
	out := JobSecrets{Values: map[string]string{}, Masks: []string{}}
	var js spec.Job
	if err := json.Unmarshal(j.Spec, &js); err != nil {
		return out, err
	}
	if len(js.Secrets) == 0 {
		return out, nil
	}
	run, err := q.GetRun(ctx, j.RunID)
	if err != nil {
		return out, err
	}
	if run.Trigger == store.RunTriggerPullRequest {
		return out, &UnavailableError{Reason: pipeline.ReasonSecretsNotAllowed, Message: "Pull-request runs don't receive secrets."}
	}
	var envID *uuid.UUID
	if j.Environment != nil {
		env, err := q.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: run.ProjectID, Name: *j.Environment})
		if err != nil && !database.IsNoRows(err) {
			return out, err
		}
		if err == nil {
			envID = &env.Environment.ID
		}
	}
	rows, err := q.ResolveJobSecrets(ctx, store.ResolveJobSecretsParams{ProjectID: run.ProjectID, Names: js.Secrets, EnvironmentID: envID})
	if err != nil {
		return out, err
	}
	var missing []string
	for _, n := range js.Secrets {
		if !slices.ContainsFunc(rows, func(r store.ResolveJobSecretsRow) bool { return r.Name == n }) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return out, &UnavailableError{Reason: pipeline.ReasonSecretNotFound,
			Message: fmt.Sprintf("No secret named %s for this job (project-wide or its environment).", strings.Join(missing, ", "))}
	}
	for _, r := range rows {
		v, err := s.keys.OpenEnvelope(crypto.Envelope{Ciphertext: r.Ciphertext, Nonce: r.Nonce, DEKEnc: r.DekEnc}, aad(r.ID, r.Version))
		if err != nil {
			// Unreadable: its master key is no longer configured. Never log the value or key.
			s.logger.ErrorContext(ctx, "secret cannot be decrypted", "secret_id", r.ID, "version", r.Version)
			return out, &UnavailableError{Reason: pipeline.ReasonSecretNotFound,
				Message: fmt.Sprintf("Secret %s can't be decrypted: its master key is no longer configured.", r.Name)}
		}
		out.Values[r.Name] = string(v)
		out.Masks = append(out.Masks, maskValues(string(v))...)
		if err := audit.Record(ctx, q, audit.Entry{
			OrganizationID: &run.OrganizationID, ProjectID: &run.ProjectID, Action: "secret.read", ResourceType: "secret",
			ResourceID: r.ID.String(), ActorType: "runner",
			Metadata: map[string]any{
				"name": r.Name, "version": r.Version, "run_id": run.ID.String(), "job_id": j.ID.String(),
				"runner_id": runnerID.String(),
			},
		}); err != nil {
			return out, err
		}
	}
	raw, err := json.Marshal(out.Masks)
	if err != nil {
		return out, err
	}
	if out.MasksEnc, err = s.keys.Encrypt(raw, masksAAD(j.ID)); err != nil {
		return out, err
	}
	return out, nil
}

// maskValues is what to hide for a value: the whole value, and each line of a multi-line
// value (tools often print one line of a key or certificate).
func maskValues(v string) []string {
	out := []string{v}
	if strings.Contains(v, "\n") {
		for line := range strings.SplitSeq(v, "\n") {
			if line = strings.TrimSpace(line); len(line) >= 4 && !slices.Contains(out, line) {
				out = append(out, line)
			}
		}
	}
	return out
}

// JobMasks returns the values to mask in a running job's output (empty when it has none).
func (s *Service) JobMasks(ctx context.Context, q *store.Queries, jobID uuid.UUID) (pipeline.Masker, error) {
	enc, err := q.GetJobMasks(ctx, jobID)
	if database.IsNoRows(err) || (err == nil && len(enc) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err := s.keys.Decrypt(enc, masksAAD(jobID))
	if err != nil {
		return nil, errors.New("job masks cannot be decrypted")
	}
	var masks []string
	if err := json.Unmarshal(raw, &masks); err != nil {
		return nil, err
	}
	return masks, nil
}
