// Package secret implements write-only project secrets (Module 8): versions sealed with
// envelope encryption, rotation that destroys old values, and injection into pipeline jobs
// when a runner claims them, audited as secret.read. No endpoint ever returns a value.
package secret

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/store"
)

// Limits.
const (
	MaxPerProject  = 500
	MaxValueBytes  = 64 << 10
	MaxDescription = 500
)

// Service manages secrets.
type Service struct {
	pool   *pgxpool.Pool
	keys   *crypto.KeyRing
	logger *slog.Logger
}

func NewService(pool *pgxpool.Pool, keys *crypto.KeyRing, logger *slog.Logger) *Service {
	return &Service{pool: pool, keys: keys, logger: logger}
}

func (s *Service) inTx(ctx context.Context, fn func(q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(store.New(tx)) })
}

// Secret is the API representation: metadata only.
type Secret struct {
	ID              uuid.UUID  `json:"id"`
	ProjectID       uuid.UUID  `json:"project_id"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	EnvironmentID   *uuid.UUID `json:"environment_id"`
	EnvironmentName *string    `json:"environment_name"`
	// Protected: the secret reaches a protected environment (project-wide secrets reach them all).
	Protected      bool      `json:"protected"`
	CanManage      bool      `json:"can_manage"`
	CurrentVersion int32     `json:"current_version"`
	Version        int32     `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	RotatedAt      time.Time `json:"rotated_at"`
}

// VersionInfo is one entry of a secret's history.
type VersionInfo struct {
	Version       int32      `json:"version"`
	Current       bool       `json:"current"`
	CreatedAt     time.Time  `json:"created_at"`
	CreatedBy     *uuid.UUID `json:"created_by"`
	CreatedByName *string    `json:"created_by_name"`
	// DestroyedAt is when the value was destroyed (by a rotation or deletion).
	DestroyedAt *time.Time `json:"destroyed_at"`
}

func errNotFound() *apperr.Error {
	return apperr.New(apperr.CodeSecretNotFound, http.StatusNotFound, "secret not found")
}

func errNameTaken() *apperr.Error {
	return apperr.New(apperr.CodeSecretNameTaken, http.StatusConflict, "a secret with this name exists in this scope")
}

// asNotFound hides a project the caller can't see behind the secret's own 404.
func asNotFound(err error) error {
	if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeProjectNotFound {
		return errNotFound()
	}
	return err
}

// canManage: Admins and Owners manage every secret; Developers only those of an
// unprotected environment (a project-wide secret reaches protected environments too).
func canManage(role authz.Role, envScoped, protected bool) bool {
	return authz.AtLeast(role, authz.Admin) || (envScoped && !protected)
}

func toSecret(row store.GetSecretRow, role authz.Role) Secret {
	x := row.Secret
	protected := row.Protected || x.EnvironmentID == nil
	return Secret{
		ID: x.ID, ProjectID: x.ProjectID, Name: x.Name, Description: x.Description,
		EnvironmentID: x.EnvironmentID, EnvironmentName: row.EnvironmentName, Protected: protected,
		CanManage: canManage(role, x.EnvironmentID != nil, row.Protected), CurrentVersion: x.CurrentVersion,
		Version: x.Version, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt, RotatedAt: row.RotatedAt,
	}
}

func aad(id uuid.UUID, version int32) []byte {
	return fmt.Appendf(nil, "secret:%s:%d", id, version)
}

func validValue(field, v string) *apperr.FieldError {
	switch {
	case v == "":
		return &apperr.FieldError{Field: field, Rule: "required"}
	case len(v) > MaxValueBytes:
		return &apperr.FieldError{Field: field, Rule: "max_bytes", Param: fmt.Sprint(MaxValueBytes)}
	case !utf8.ValidString(v) || strings.ContainsRune(v, 0):
		// Values become environment variables: no NUL bytes.
		return &apperr.FieldError{Field: field, Rule: "text"}
	}
	return nil
}

// seal stores a new version's value.
func (s *Service) seal(ctx context.Context, q *store.Queries, id uuid.UUID, version int32, value string, by *uuid.UUID) error {
	e, err := s.keys.SealEnvelope([]byte(value), aad(id, version))
	if err != nil {
		return err
	}
	return q.InsertSecretVersion(ctx, store.InsertSecretVersionParams{
		SecretID: id, Version: version, Ciphertext: e.Ciphertext, Nonce: e.Nonce, DekEnc: e.DEKEnc, KekID: &e.KEKID, CreatedBy: by,
	})
}

// load fetches a secret and checks the action on its project. For changes, Developers are
// further limited to unprotected environments.
func (s *Service) load(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.GetSecretRow, project.Access, error) {
	row, err := q.GetSecret(ctx, id)
	if database.IsNoRows(err) {
		return row, project.Access{}, errNotFound()
	}
	if err != nil {
		return row, project.Access{}, err
	}
	acc, err := project.Authorize(ctx, q, row.Secret.ProjectID, a)
	if err != nil {
		return row, acc, asNotFound(err)
	}
	if a != authz.SecretList && !canManage(acc.Role, row.Secret.EnvironmentID != nil, row.Protected) {
		return row, acc, apperr.Forbidden()
	}
	return row, acc, nil
}

func auditEntry(acc project.Access, action string, x store.Secret, meta map[string]any) audit.Entry {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["name"] = x.Name
	if x.EnvironmentID != nil {
		meta["environment_id"] = x.EnvironmentID.String()
	}
	return audit.Entry{
		OrganizationID: &acc.Project.OrganizationID, ProjectID: &acc.Project.ID, Action: action,
		ResourceType: "secret", ResourceID: x.ID.String(), Metadata: meta,
	}
}

// Filter narrows a project's secret list.
type Filter struct {
	EnvironmentID *uuid.UUID
	ProjectWide   bool // only secrets for every environment
}

// List returns a project's secrets (secret.list).
func (s *Service) List(ctx context.Context, projectID uuid.UUID, f Filter) ([]Secret, error) {
	q := store.New(s.pool)
	acc, err := project.Authorize(ctx, q, projectID, authz.SecretList)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSecrets(ctx, store.ListSecretsParams{ProjectID: projectID, EnvironmentID: f.EnvironmentID, ProjectWide: f.ProjectWide})
	if err != nil {
		return nil, err
	}
	out := make([]Secret, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSecret(store.GetSecretRow(r), acc.Role))
	}
	return out, nil
}

// Get returns one secret's metadata (secret.list).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Secret, error) {
	row, acc, err := s.load(ctx, store.New(s.pool), id, authz.SecretList)
	if err != nil {
		return Secret{}, err
	}
	return toSecret(row, acc.Role), nil
}

// CreateInput is POST /projects/{id}/secrets. Value is write-only.
type CreateInput struct {
	Name          string     `json:"name"`
	EnvironmentID *uuid.UUID `json:"environment_id"`
	Description   string     `json:"description"`
	Value         string     `json:"value"`
}

// Create adds a secret with its first version (secret.create).
func (s *Service) Create(ctx context.Context, projectID uuid.UUID, in CreateInput) (Secret, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	var fields []apperr.FieldError
	switch {
	case !spec.SecretNamePattern.MatchString(in.Name):
		fields = append(fields, apperr.FieldError{Field: "name", Rule: "secret_name"})
	case strings.HasPrefix(in.Name, "OPSHUB_"):
		fields = append(fields, apperr.FieldError{Field: "name", Rule: "reserved", Param: "OPSHUB_"})
	}
	if utf8.RuneCountInString(in.Description) > MaxDescription {
		fields = append(fields, apperr.FieldError{Field: "description", Rule: "max", Param: fmt.Sprint(MaxDescription)})
	}
	if fe := validValue("value", in.Value); fe != nil {
		fields = append(fields, *fe)
	}
	var out Secret
	err := s.inTx(ctx, func(q *store.Queries) error {
		acc, err := project.Authorize(ctx, q, projectID, authz.SecretCreate)
		if err != nil {
			return err
		}
		protected := false
		var envName *string
		if in.EnvironmentID != nil {
			env, err := q.GetEnvironment(ctx, *in.EnvironmentID)
			switch {
			case database.IsNoRows(err) || (err == nil && env.Environment.ProjectID != projectID):
				fields = append(fields, apperr.FieldError{Field: "environment_id", Rule: "exists"})
			case err != nil:
				return err
			default:
				protected, envName = env.Protected, &env.Environment.Name
			}
		}
		if len(fields) > 0 {
			return apperr.Validation(fields)
		}
		if !canManage(acc.Role, in.EnvironmentID != nil, protected) {
			return apperr.Forbidden()
		}
		n, err := q.CountSecrets(ctx, projectID)
		if err != nil {
			return err
		}
		if n >= MaxPerProject {
			return apperr.New(apperr.CodeSecretLimit, http.StatusConflict, "this project has the maximum number of secrets").
				WithDetails(map[string]any{"max": MaxPerProject})
		}
		x, err := q.CreateSecret(ctx, store.CreateSecretParams{
			ProjectID: projectID, EnvironmentID: in.EnvironmentID, Name: in.Name, Description: in.Description, CreatedBy: &acc.UserID,
		})
		if database.IsUniqueViolation(err, "secrets_scope_name_key") {
			return errNameTaken()
		}
		if err != nil {
			return err
		}
		if err := s.seal(ctx, q, x.ID, 1, in.Value, &acc.UserID); err != nil {
			return err
		}
		out = toSecret(store.GetSecretRow{Secret: x, EnvironmentName: envName, Protected: protected, RotatedAt: x.CreatedAt}, acc.Role)
		return audit.Record(ctx, q, auditEntry(acc, string(authz.SecretCreate), x, map[string]any{"version": 1}))
	})
	return out, err
}

// UpdateInput is PATCH /secrets/{id}: the description only (the value changes by rotation).
type UpdateInput struct {
	Description string `json:"description"`
}

// Update changes the description (secret.update, If-Match).
func (s *Service) Update(ctx context.Context, id uuid.UUID, version int32, in UpdateInput) (Secret, error) {
	in.Description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(in.Description) > MaxDescription {
		return Secret{}, apperr.Validation([]apperr.FieldError{{Field: "description", Rule: "max", Param: fmt.Sprint(MaxDescription)}})
	}
	var out Secret
	err := s.inTx(ctx, func(q *store.Queries) error {
		row, acc, err := s.load(ctx, q, id, authz.SecretUpdate)
		if err != nil {
			return err
		}
		x, err := q.UpdateSecretDescription(ctx, store.UpdateSecretDescriptionParams{ID: id, Description: in.Description, ExpectedVersion: version})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if err != nil {
			return err
		}
		e := auditEntry(acc, string(authz.SecretUpdate), x, nil)
		e.Before, e.After = map[string]any{"description": row.Secret.Description}, map[string]any{"description": x.Description}
		row.Secret = x
		out = toSecret(row, acc.Role)
		return audit.Record(ctx, q, e)
	})
	return out, err
}

// RotateInput is POST /secrets/{id}/versions.
type RotateInput struct {
	Value string `json:"value"`
}

// Rotate stores a new value as the next version and destroys every older value
// (secret.rotate). Jobs claimed from now on receive the new value.
func (s *Service) Rotate(ctx context.Context, id uuid.UUID, in RotateInput) (Secret, error) {
	if fe := validValue("value", in.Value); fe != nil {
		return Secret{}, apperr.Validation([]apperr.FieldError{*fe})
	}
	var out Secret
	err := s.inTx(ctx, func(q *store.Queries) error {
		row, acc, err := s.load(ctx, q, id, authz.SecretRotate)
		if err != nil {
			return err
		}
		x, err := q.BumpSecretVersion(ctx, store.BumpSecretVersionParams{ID: id, ExpectedVersion: row.Secret.Version})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if err != nil {
			return err
		}
		if err := s.seal(ctx, q, id, x.CurrentVersion, in.Value, &acc.UserID); err != nil {
			return err
		}
		if _, err := q.DestroySecretValues(ctx, store.DestroySecretValuesParams{SecretID: id, BeforeVersion: x.CurrentVersion}); err != nil {
			return err
		}
		fresh, err := q.GetSecret(ctx, id)
		if err != nil {
			return err
		}
		out = toSecret(fresh, acc.Role)
		return audit.Record(ctx, q, auditEntry(acc, string(authz.SecretRotate), x, map[string]any{"version": x.CurrentVersion}))
	})
	return out, err
}

// Delete soft-deletes a secret and destroys every value (secret.delete). Its history stays.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		row, acc, err := s.load(ctx, q, id, authz.SecretDelete)
		if err != nil {
			return err
		}
		n, err := q.SoftDeleteSecret(ctx, store.SoftDeleteSecretParams{ID: id, ExpectedVersion: row.Secret.Version})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.VersionConflict()
		}
		if _, err := q.DestroySecretValues(ctx, store.DestroySecretValuesParams{SecretID: id}); err != nil {
			return err
		}
		return audit.Record(ctx, q, auditEntry(acc, string(authz.SecretDelete), row.Secret, nil))
	})
}

// Versions lists a secret's history (secret.list).
func (s *Service) Versions(ctx context.Context, id uuid.UUID) ([]VersionInfo, error) {
	q := store.New(s.pool)
	row, _, err := s.load(ctx, q, id, authz.SecretList)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSecretVersions(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]VersionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, VersionInfo{
			Version: r.Version, Current: r.Version == row.Secret.CurrentVersion, CreatedAt: r.CreatedAt,
			CreatedBy: r.CreatedBy, CreatedByName: r.CreatedByName, DestroyedAt: r.DestroyedAt,
		})
	}
	return out, nil
}
