// Package runners manages runner agents (Module 5): one-time registration tokens, runner
// and per-job credentials, the runner-facing job API on top of the pipeline lifecycle,
// source archives, artifacts and the pipeline cache.
//
// Credentials: a registration token (`ohr_reg_…`, one hour, single use) becomes a runner
// token (`ohr_…`); every claimed job gets a job token (`ohj_…`) that is valid only for that
// job and until its timeout plus ten minutes. Only SHA-256 hashes are stored.
package runners

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/blob"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/secret"
	"github.com/opshub/opshub/internal/store"
)

// Timing.
const (
	RegistrationTTL   = time.Hour
	HeartbeatInterval = 10 * time.Second
	// A runner missing three heartbeats is offline; its running jobs fail as runner_lost.
	OfflineAfter  = 3 * HeartbeatInterval
	jobTokenGrace = 10 * time.Minute
)

// Config holds storage limits.
type Config struct {
	ArtifactMaxBytes int64
	CacheMaxBytes    int64
	CacheQuotaBytes  int64
	SourceMaxBytes   int64
}

// Service implements runner management and the runner API.
type Service struct {
	pool      *pgxpool.Pool
	pipelines *pipeline.Service
	projects  *project.Service
	blobs     blob.Store
	cfg       Config
	logger    *slog.Logger
	now       func() time.Time
	secrets   SecretProvider
}

// SecretProvider gives claimed jobs their secrets (Module 8, secret.Service).
type SecretProvider interface {
	ForJob(ctx context.Context, q *store.Queries, j store.PipelineJob, runnerID uuid.UUID) (secret.JobSecrets, error)
	JobMasks(ctx context.Context, q *store.Queries, jobID uuid.UUID) (pipeline.Masker, error)
}

// SetSecrets installs the secrets module. Without it, jobs that list secrets fail when
// claimed (the pipeline gate normally stops them first).
func (s *Service) SetSecrets(p SecretProvider) { s.secrets = p }

func NewService(pool *pgxpool.Pool, pipelines *pipeline.Service, projects *project.Service, blobs blob.Store, cfg Config, logger *slog.Logger) *Service {
	return &Service{pool: pool, pipelines: pipelines, projects: projects, blobs: blobs, cfg: cfg, logger: logger, now: time.Now}
}

func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx, q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, store.New(tx)) })
}

// Runner is the API representation of a runner.
type Runner struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	Labels         []string   `json:"labels"`
	Version        string     `json:"version"`
	OS             string     `json:"os"`
	Arch           string     `json:"arch"`
	MaxConcurrency int32      `json:"max_concurrency"`
	Status         string     `json:"status"` // online | offline | disabled
	RunningJobs    int64      `json:"running_jobs"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
	TokenPrefix    string     `json:"token_prefix"`
	RowVersion     int32      `json:"row_version"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (s *Service) status(r store.Runner) string {
	switch {
	case r.DisabledAt != nil:
		return "disabled"
	case r.LastSeenAt != nil && s.now().Sub(*r.LastSeenAt) < OfflineAfter:
		return "online"
	}
	return "offline"
}

func (s *Service) toRunner(r store.Runner, running int64) Runner {
	labels := r.Labels
	if labels == nil {
		labels = []string{}
	}
	return Runner{
		ID: r.ID, Name: r.Name, Labels: labels, Version: r.Version, OS: r.Os, Arch: r.Arch, MaxConcurrency: r.MaxConcurrency,
		Status: s.status(r), RunningJobs: running, LastSeenAt: r.LastSeenAt, TokenPrefix: r.TokenPrefix, RowVersion: r.RowVersion,
		CreatedAt: r.CreatedAt,
	}
}

func errRunnerNotFound() *apperr.Error {
	return apperr.New(apperr.CodeRunnerNotFound, http.StatusNotFound, "runner not found")
}

var labelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// normalizeLabels validates, lowercases and de-duplicates runner labels.
func normalizeLabels(in []string) ([]string, error) {
	out := []string{}
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if !labelPattern.MatchString(l) {
			return nil, apperr.Validation([]apperr.FieldError{{Field: "labels", Rule: "pattern"}})
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	if len(out) > 20 {
		return nil, apperr.Validation([]apperr.FieldError{{Field: "labels", Rule: "max", Param: "20"}})
	}
	slices.Sort(out)
	return out, nil
}

// ListRunners lists the organization's runners (runner.view).
func (s *Service) ListRunners(ctx context.Context, orgID uuid.UUID) ([]Runner, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.RunnerView); err != nil {
		return nil, err
	}
	rows, err := q.ListRunners(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Runner, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toRunner(store.Runner{
			ID: r.ID, OrganizationID: r.OrganizationID, Name: r.Name, Labels: r.Labels, TokenHash: r.TokenHash,
			TokenPrefix: r.TokenPrefix, Version: r.Version, Os: r.Os, Arch: r.Arch, MaxConcurrency: r.MaxConcurrency,
			LastSeenAt: r.LastSeenAt, DisabledAt: r.DisabledAt, CreatedBy: r.CreatedBy, RowVersion: r.RowVersion,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}, r.RunningJobs))
	}
	return out, nil
}

// RegistrationToken is shown once to the person registering a runner.
type RegistrationToken struct {
	Token     string    `json:"token"`
	Labels    []string  `json:"labels"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RegistrationInput is POST /orgs/{id}/runner-registration-tokens.
type RegistrationInput struct {
	Labels []string `json:"labels"`
}

// CreateRegistrationToken issues a one-time token to register a runner (runner.manage).
// Labels given here are added to the runner's own.
func (s *Service) CreateRegistrationToken(ctx context.Context, orgID uuid.UUID, in RegistrationInput) (RegistrationToken, error) {
	labels, err := normalizeLabels(in.Labels)
	if err != nil {
		return RegistrationToken{}, err
	}
	var out RegistrationToken
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		m, err := authz.Require(ctx, q, orgID, authz.RunnerManage)
		if err != nil {
			return err
		}
		token := authn.RegistrationTokenPrefix + crypto.RandomToken(32)
		row, err := q.CreateRegistrationToken(ctx, store.CreateRegistrationTokenParams{
			OrganizationID: orgID, TokenHash: crypto.HashToken(token), Labels: labels, CreatedBy: &m.UserID,
			ExpiresAt: s.now().Add(RegistrationTTL),
		})
		if err != nil {
			return err
		}
		out = RegistrationToken{Token: token, Labels: labels, ExpiresAt: row.ExpiresAt}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "runner.registration_token", ResourceType: "runner_registration_token",
			ResourceID: row.ID.String(), After: map[string]any{"labels": labels, "expires_at": row.ExpiresAt},
		})
	})
	return out, err
}

func (s *Service) loadRunner(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.Runner, error) {
	r, err := q.GetRunner(ctx, id)
	if database.IsNoRows(err) {
		return r, errRunnerNotFound()
	}
	if err != nil {
		return r, err
	}
	if _, err := authz.Require(ctx, q, r.OrganizationID, a); err != nil {
		if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
			return r, errRunnerNotFound()
		}
		return r, err
	}
	return r, nil
}

// UpdateInput is PATCH /runners/{id}.
type UpdateInput struct {
	Name           string   `json:"name" validate:"required,min=1,max=100"`
	Labels         []string `json:"labels"`
	MaxConcurrency int32    `json:"max_concurrency" validate:"min=1,max=64"`
	Disabled       bool     `json:"disabled"`
}

// UpdateRunner renames, relabels, limits or disables a runner (runner.manage, If-Match).
// A disabled runner can't take new jobs; running ones continue.
func (s *Service) UpdateRunner(ctx context.Context, id uuid.UUID, version int32, in UpdateInput) (Runner, error) {
	labels, err := normalizeLabels(in.Labels)
	if err != nil {
		return Runner{}, err
	}
	var out Runner
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		before, err := s.loadRunner(ctx, q, id, authz.RunnerManage)
		if err != nil {
			return err
		}
		r, err := q.UpdateRunner(ctx, store.UpdateRunnerParams{
			ID: id, RowVersion: version, Name: strings.TrimSpace(in.Name), Labels: labels,
			MaxConcurrency: in.MaxConcurrency, Disabled: in.Disabled,
		})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if err != nil {
			return err
		}
		running, err := q.CountRunningJobs(ctx, &id)
		if err != nil {
			return err
		}
		out = s.toRunner(r, running)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &r.OrganizationID, Action: "runner.update", ResourceType: "runner", ResourceID: id.String(),
			Before: map[string]any{"name": before.Name, "labels": before.Labels, "max_concurrency": before.MaxConcurrency, "disabled": before.DisabledAt != nil},
			After:  map[string]any{"name": r.Name, "labels": r.Labels, "max_concurrency": r.MaxConcurrency, "disabled": r.DisabledAt != nil},
		})
	})
	return out, err
}

// DeleteRunner removes a runner and revokes its token (runner.manage). Its running jobs
// fail as runner_lost.
func (s *Service) DeleteRunner(ctx context.Context, id uuid.UUID) error {
	var orphaned []uuid.UUID
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		r, err := s.loadRunner(ctx, q, id, authz.RunnerManage)
		if err != nil {
			return err
		}
		if orphaned, err = q.RunningJobsForRunner(ctx, &id); err != nil {
			return err
		}
		if _, err := q.DeleteRunner(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &r.OrganizationID, Action: "runner.delete", ResourceType: "runner", ResourceID: id.String(),
			Before: map[string]any{"name": r.Name, "labels": r.Labels},
		})
	})
	if err != nil {
		return err
	}
	for _, jobID := range orphaned {
		if err := s.pipelines.FailJob(ctx, jobID, pipeline.ReasonRunnerLost); err != nil {
			return err
		}
	}
	return nil
}
