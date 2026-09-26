// Package logs implements searchable logs (Module 10): services send NDJSON lines with
// organization ingest tokens, pipeline job and deployment output is copied in by database
// triggers, and members search everything with full-text queries. Day partitions are dropped
// after the operator's retention.
package logs

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// Config holds operator settings.
type Config struct {
	// RetentionDays is OPSHUB_LOG_RETENTION_DAYS (1–365).
	RetentionDays int
}

// Service manages ingest tokens, ingests lines and searches them.
type Service struct {
	pool     *pgxpool.Pool
	cfg      Config
	logger   *slog.Logger
	now      func() time.Time
	observer func(accepted, rejected int)
}

// SetIngestObserver reports each ingest request's accepted and rejected line counts (metrics).
func (s *Service) SetIngestObserver(f func(accepted, rejected int)) { s.observer = f }

func NewService(pool *pgxpool.Pool, cfg Config, logger *slog.Logger) *Service {
	if cfg.RetentionDays < 1 || cfg.RetentionDays > 365 {
		cfg.RetentionDays = 30
	}
	return &Service{pool: pool, cfg: cfg, logger: logger, now: time.Now}
}

func (s *Service) inTx(ctx context.Context, fn func(q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(store.New(tx)) })
}

// ServicePattern is a service name: lowercase, digits, ".", "_", "-" and "/".
var ServicePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,99}$`)

// IngestToken is the API representation; Token is set only when it is created.
type IngestToken struct {
	ID            uuid.UUID  `json:"id"`
	Name          string     `json:"name"`
	Service       string     `json:"service"`
	TokenPrefix   string     `json:"token_prefix"`
	Token         string     `json:"token,omitempty"`
	CreatedByName *string    `json:"created_by_name"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

func errTokenNotFound() *apperr.Error {
	return apperr.New(apperr.CodeIngestTokenNotFound, http.StatusNotFound, "ingest token not found")
}

func errTokenInvalid() *apperr.Error {
	return apperr.New(apperr.CodeIngestTokenInvalid, http.StatusUnauthorized, "ingest token is invalid or revoked")
}

// TokenInput is POST /orgs/{id}/log-ingest-tokens.
type TokenInput struct {
	Name    string `json:"name"`
	Service string `json:"service"`
}

// ListTokens returns an organization's ingest tokens (logs.manage).
func (s *Service) ListTokens(ctx context.Context, orgID uuid.UUID) ([]IngestToken, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.LogsManage); err != nil {
		return nil, err
	}
	rows, err := q.ListIngestTokens(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]IngestToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, IngestToken{
			ID: r.ID, Name: r.Name, Service: r.Service, TokenPrefix: r.TokenPrefix, CreatedByName: r.CreatedByName,
			LastUsedAt: r.LastUsedAt, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// CreateToken issues an ingest token for one service; the token is shown once (logs.manage).
func (s *Service) CreateToken(ctx context.Context, orgID uuid.UUID, in TokenInput) (IngestToken, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Service = strings.TrimSpace(in.Service)
	var fields []apperr.FieldError
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 {
		fields = append(fields, apperr.FieldError{Field: "name", Rule: "range", Param: "1-100"})
	}
	if !ServicePattern.MatchString(in.Service) {
		fields = append(fields, apperr.FieldError{Field: "service", Rule: "pattern"})
	}
	var out IngestToken
	err := s.inTx(ctx, func(q *store.Queries) error {
		m, err := authz.Require(ctx, q, orgID, authz.LogsManage)
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			return apperr.Validation(fields)
		}
		prefix := strings.ToLower(crypto.RandomBase32(8))
		token := authn.IngestTokenPrefix + prefix + "_" + crypto.RandomToken(32)
		t, err := q.CreateIngestToken(ctx, store.CreateIngestTokenParams{
			OrganizationID: orgID, Name: in.Name, Service: in.Service, TokenHash: crypto.HashToken(token),
			TokenPrefix: authn.IngestTokenPrefix + prefix, CreatedBy: &m.UserID,
		})
		if database.IsUniqueViolation(err, "log_ingest_tokens_organization_id_name_key") {
			return apperr.New(apperr.CodeIngestTokenNameTaken, http.StatusConflict, "an ingest token with this name exists")
		}
		if err != nil {
			return err
		}
		out = IngestToken{ID: t.ID, Name: t.Name, Service: t.Service, TokenPrefix: t.TokenPrefix, Token: token, CreatedAt: t.CreatedAt}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "log_token.create", ResourceType: "log_ingest_token", ResourceID: t.ID.String(),
			After: map[string]any{"name": t.Name, "service": t.Service, "prefix": t.TokenPrefix},
		})
	})
	return out, err
}

// RevokeToken deletes an ingest token; senders using it get 401 (logs.manage).
func (s *Service) RevokeToken(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		t, err := q.GetIngestToken(ctx, id)
		if database.IsNoRows(err) {
			return errTokenNotFound()
		}
		if err != nil {
			return err
		}
		if _, err := authz.Require(ctx, q, t.OrganizationID, authz.LogsManage); err != nil {
			if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
				return errTokenNotFound()
			}
			return err
		}
		if _, err := q.DeleteIngestToken(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &t.OrganizationID, Action: "log_token.revoke", ResourceType: "log_ingest_token", ResourceID: id.String(),
			Before: map[string]any{"name": t.Name, "service": t.Service, "prefix": t.TokenPrefix},
		})
	})
}

// Authenticate resolves an ingest token.
func (s *Service) Authenticate(ctx context.Context, token string) (store.LogIngestToken, error) {
	if !strings.HasPrefix(token, authn.IngestTokenPrefix) {
		return store.LogIngestToken{}, errTokenInvalid()
	}
	t, err := store.New(s.pool).GetIngestTokenByHash(ctx, crypto.HashToken(token))
	if database.IsNoRows(err) {
		return t, errTokenInvalid()
	}
	return t, err
}

// Maintain creates upcoming day partitions and drops expired ones (hourly).
func (s *Service) Maintain(ctx context.Context) error {
	return store.New(s.pool).MaintainLogPartitions(ctx, int32(s.cfg.RetentionDays)) // #nosec G115 -- 1–365
}
