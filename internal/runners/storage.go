package runners

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/blob"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
)

// DefaultArtifactExpireDays matches the pipeline file's default (`expire_in`, docs/pipelines.md).
const DefaultArtifactExpireDays = 7

func errArtifactNotFound() *apperr.Error {
	return apperr.New(apperr.CodeArtifactNotFound, http.StatusNotFound, "artifact not found")
}

func errCacheNotFound() *apperr.Error {
	return apperr.New(apperr.CodeCacheNotFound, http.StatusNotFound, "no cache entry for this key")
}

func errTooLarge(limit int64) *apperr.Error {
	return apperr.New(apperr.CodePayloadTooLarge, http.StatusRequestEntityTooLarge, "upload too large").
		WithDetails(map[string]any{"max_bytes": limit})
}

// Artifact is the API representation of a job's artifact archive.
type Artifact struct {
	ID        uuid.UUID `json:"id"`
	JobID     uuid.UUID `json:"job_id"`
	JobName   string    `json:"job_name,omitempty"`
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func toArtifact(a store.Artifact, jobName string) Artifact {
	return Artifact{
		ID: a.ID, JobID: a.JobID, JobName: jobName, Name: a.Name, SizeBytes: a.SizeBytes,
		SHA256: hex.EncodeToString(a.Sha256), ExpiresAt: a.ExpiresAt, CreatedAt: a.CreatedAt,
	}
}

// Download is an opened blob with its metadata.
type Download struct {
	Body   io.ReadCloser
	Size   int64
	Name   string
	SHA256 string
}

func (s *Service) open(ctx context.Context, key string, nf func() *apperr.Error) (io.ReadCloser, int64, error) {
	body, size, err := s.blobs.Get(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, 0, nf()
	}
	return body, size, err
}

// ---- user API ----

// ListJobArtifacts lists a job's unexpired artifacts (run.view).
func (s *Service) ListJobArtifacts(ctx context.Context, jobID uuid.UUID) ([]Artifact, error) {
	q := store.New(s.pool)
	j, err := q.GetJob(ctx, jobID)
	if database.IsNoRows(err) {
		return nil, apperr.New(apperr.CodeJobNotFound, http.StatusNotFound, "job not found")
	}
	if err != nil {
		return nil, err
	}
	if _, err := project.Authorize(ctx, q, j.ProjectID, authz.RunView); err != nil {
		if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeProjectNotFound {
			return nil, apperr.New(apperr.CodeJobNotFound, http.StatusNotFound, "job not found")
		}
		return nil, err
	}
	rows, err := q.ListJobArtifacts(ctx, jobID)
	if err != nil {
		return nil, err
	}
	out := make([]Artifact, 0, len(rows))
	for _, a := range rows {
		out = append(out, toArtifact(a, j.Name))
	}
	return out, nil
}

// OpenArtifact opens an artifact for download (run.view on its project).
func (s *Service) OpenArtifact(ctx context.Context, id uuid.UUID) (Download, error) {
	q := store.New(s.pool)
	a, err := q.GetArtifact(ctx, id)
	if database.IsNoRows(err) {
		return Download{}, errArtifactNotFound()
	}
	if err != nil {
		return Download{}, err
	}
	if _, err := project.Authorize(ctx, q, a.ProjectID, authz.RunView); err != nil {
		if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeProjectNotFound {
			return Download{}, errArtifactNotFound()
		}
		return Download{}, err
	}
	body, size, err := s.open(ctx, a.StorageKey, errArtifactNotFound)
	if err != nil {
		return Download{}, err
	}
	return Download{Body: body, Size: size, Name: a.Name, SHA256: hex.EncodeToString(a.Sha256)}, nil
}

// ---- runner API (job token) ----

// limitedReader fails once more than n bytes have been read, so a runner never unpacks a
// silently truncated archive.
type limitedReader struct {
	io.ReadCloser
	left int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.ReadCloser.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return n, errors.New("source archive exceeds the size limit")
	}
	return n, err
}

// Source streams the job's commit as a gzip-compressed tarball from the connected
// repository. Without a repository it returns REPOSITORY_NOT_FOUND and the runner starts
// from an empty workspace.
func (s *Service) Source(ctx context.Context, j store.PipelineJob) (io.ReadCloser, error) {
	q := store.New(s.pool)
	r, err := q.GetRun(ctx, j.RunID)
	if err != nil {
		return nil, err
	}
	c, _, err := s.projects.GitClient(ctx, q, j.ProjectID)
	if err != nil {
		return nil, err
	}
	body, err := c.Archive(ctx, r.CommitSha)
	if err != nil {
		return nil, project.GitError(err)
	}
	return &limitedReader{ReadCloser: body, left: s.cfg.SourceMaxBytes}, nil
}

var artifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

// UploadArtifact stores the job's artifact archive (a gzip-compressed tar). A job has at
// most one; it expires after the job's artifacts.expire_days.
func (s *Service) UploadArtifact(ctx context.Context, j store.PipelineJob, name string, body io.Reader) (Artifact, error) {
	if name == "" {
		name = j.Name + ".tar.gz"
	}
	if !artifactNamePattern.MatchString(name) {
		return Artifact{}, apperr.Validation([]apperr.FieldError{{Field: "name", Rule: "pattern"}})
	}
	var js spec.Job
	if err := json.Unmarshal(j.Spec, &js); err != nil {
		return Artifact{}, err
	}
	days := DefaultArtifactExpireDays
	if js.Artifacts != nil && js.Artifacts.ExpireDays > 0 {
		days = js.Artifacts.ExpireDays
	}
	// A fresh key per upload: a re-sent upload never overwrites the recorded blob.
	key := fmt.Sprintf("artifacts/%s/%s-%s.tar.gz", j.ProjectID, j.ID, strings.ToLower(crypto.RandomBase32(8)))
	size, sum, err := s.blobs.Put(ctx, key, body, s.cfg.ArtifactMaxBytes)
	if errors.Is(err, blob.ErrTooLarge) {
		return Artifact{}, errTooLarge(s.cfg.ArtifactMaxBytes)
	}
	if err != nil {
		return Artifact{}, err
	}
	a, err := store.New(s.pool).InsertArtifact(ctx, store.InsertArtifactParams{
		JobID: j.ID, ProjectID: j.ProjectID, OrganizationID: j.OrganizationID, Name: name, SizeBytes: size,
		Sha256: sum, StorageKey: key, ExpiresAt: s.now().AddDate(0, 0, days),
	})
	if err != nil {
		s.deleteBlob(ctx, key)
		if database.IsNoRows(err) {
			return Artifact{}, apperr.New(apperr.CodeConflict, http.StatusConflict, "this job already has artifacts")
		}
		return Artifact{}, err
	}
	return toArtifact(a, j.Name), nil
}

// Dependencies lists the artifacts of the jobs this job needs, from the latest successful
// attempt of each.
func (s *Service) Dependencies(ctx context.Context, j store.PipelineJob) ([]Artifact, error) {
	names := j.Needs
	if names == nil {
		names = []string{}
	}
	rows, err := store.New(s.pool).DependencyArtifacts(ctx, store.DependencyArtifactsParams{RunID: j.RunID, Names: names})
	if err != nil {
		return nil, err
	}
	out := make([]Artifact, 0, len(rows))
	for _, r := range rows {
		out = append(out, toArtifact(store.Artifact{
			ID: r.ID, JobID: r.JobID, ProjectID: r.ProjectID, OrganizationID: r.OrganizationID, Name: r.Name,
			SizeBytes: r.SizeBytes, Sha256: r.Sha256, StorageKey: r.StorageKey, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
		}, r.JobName))
	}
	return out, nil
}

// OpenDependency opens one of Dependencies' artifacts.
func (s *Service) OpenDependency(ctx context.Context, j store.PipelineJob, artifactID uuid.UUID) (Download, error) {
	deps, err := s.Dependencies(ctx, j)
	if err != nil {
		return Download{}, err
	}
	for _, d := range deps {
		if d.ID != artifactID {
			continue
		}
		a, err := store.New(s.pool).GetArtifact(ctx, d.ID)
		if database.IsNoRows(err) {
			return Download{}, errArtifactNotFound()
		}
		if err != nil {
			return Download{}, err
		}
		body, size, err := s.open(ctx, a.StorageKey, errArtifactNotFound)
		if err != nil {
			return Download{}, err
		}
		return Download{Body: body, Size: size, Name: a.Name, SHA256: d.SHA256}, nil
	}
	return Download{}, errArtifactNotFound()
}

var cacheKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

func validCacheKey(key string) error {
	if !cacheKeyPattern.MatchString(key) || strings.Trim(key, ".") == "" {
		return apperr.Validation([]apperr.FieldError{{Field: "key", Rule: "pattern"}})
	}
	return nil
}

// GetCache opens the project's cache archive for key.
func (s *Service) GetCache(ctx context.Context, j store.PipelineJob, key string) (Download, error) {
	if err := validCacheKey(key); err != nil {
		return Download{}, err
	}
	e, err := store.New(s.pool).GetCacheEntry(ctx, store.GetCacheEntryParams{ProjectID: j.ProjectID, Key: key})
	if database.IsNoRows(err) {
		return Download{}, errCacheNotFound()
	}
	if err != nil {
		return Download{}, err
	}
	body, size, err := s.open(ctx, e.StorageKey, errCacheNotFound)
	if err != nil {
		return Download{}, err
	}
	return Download{Body: body, Size: size, Name: key + ".tar.gz", SHA256: hex.EncodeToString(e.Sha256)}, nil
}

// PutCache stores (or replaces) the project's cache archive for key. Each upload gets a new
// blob so concurrent readers of the old one are unaffected; the old blob is then removed.
func (s *Service) PutCache(ctx context.Context, j store.PipelineJob, key string, body io.Reader) error {
	if err := validCacheKey(key); err != nil {
		return err
	}
	blobKey := fmt.Sprintf("cache/%s/%s", j.ProjectID, strings.ToLower(crypto.RandomBase32(26)))
	size, sum, err := s.blobs.Put(ctx, blobKey, body, s.cfg.CacheMaxBytes)
	if errors.Is(err, blob.ErrTooLarge) {
		return errTooLarge(s.cfg.CacheMaxBytes)
	}
	if err != nil {
		return err
	}
	prev, err := store.New(s.pool).UpsertCacheEntry(ctx, store.UpsertCacheEntryParams{
		ProjectID: j.ProjectID, Key: key, StorageKey: blobKey, SizeBytes: size, Sha256: sum,
	})
	if err != nil {
		s.deleteBlob(ctx, blobKey)
		return err
	}
	if prev != "" {
		s.deleteBlob(ctx, prev)
	}
	return nil
}

func (s *Service) deleteBlob(ctx context.Context, key string) {
	if err := s.blobs.Delete(context.WithoutCancel(ctx), key); err != nil && !errors.Is(err, blob.ErrNotFound) {
		s.logger.WarnContext(ctx, "delete blob failed", "key", key, "error", err)
	}
}

// ---- housekeeping ----

// Housekeep fails jobs of runners that stopped sending heartbeats, removes expired
// artifacts and tokens, and evicts each project's least recently used cache entries beyond
// its quota. It runs every 30 seconds.
func (s *Service) Housekeep(ctx context.Context) error {
	q := store.New(s.pool)
	lost, err := q.LostRunnerJobs(ctx, int32(OfflineAfter/time.Second))
	if err != nil {
		return err
	}
	for _, id := range lost {
		if err := s.pipelines.FailJob(ctx, id, pipeline.ReasonRunnerLost); err != nil {
			return err
		}
	}
	expired, err := q.ExpiredArtifacts(ctx)
	if err != nil {
		return err
	}
	evicted, err := q.EvictCache(ctx, s.cfg.CacheQuotaBytes)
	if err != nil {
		return err
	}
	for _, key := range append(expired, evicted...) {
		s.deleteBlob(ctx, key)
	}
	if _, err := q.DeleteExpiredJobTokens(ctx); err != nil {
		return err
	}
	_, err = q.DeleteExpiredRegistrationTokens(ctx)
	return err
}
