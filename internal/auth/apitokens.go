package auth

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// APIToken is a personal API token's metadata (the secret is never returned again).
type APIToken struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func toAPIToken(t store.ApiToken) APIToken {
	return APIToken{ID: t.ID, Name: t.Name, Prefix: t.TokenPrefix, Scopes: t.Scopes, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt}
}

// CreateAPITokenInput is POST /me/tokens.
type CreateAPITokenInput struct {
	Name          string   `json:"name" validate:"required,min=1,max=100"`
	Scopes        []string `json:"scopes" validate:"required,min=1,dive,oneof=api:read api:write"`
	ExpiresInDays *int     `json:"expires_in_days" validate:"omitempty,min=1,max=365"`
}

// CreatedAPIToken includes the secret, shown exactly once.
type CreatedAPIToken struct {
	APIToken
	Token string `json:"token"`
}

// CreateAPIToken issues "ohp_<8-char id>_<secret>"; only its SHA-256 hash is stored.
func (s *Service) CreateAPIToken(ctx context.Context, in CreateAPITokenInput) (CreatedAPIToken, error) {
	p, err := principal(ctx)
	if err != nil {
		return CreatedAPIToken{}, err
	}
	scopes := slices.Compact(slices.Sorted(slices.Values(in.Scopes)))
	prefix := authn.APITokenPrefix + strings.ToLower(crypto.RandomBase32(8))
	token := prefix + "_" + crypto.RandomToken(32)
	var exp *time.Time
	if in.ExpiresInDays != nil {
		t := s.now().Add(time.Duration(*in.ExpiresInDays) * 24 * time.Hour)
		exp = &t
	}
	var out CreatedAPIToken
	err = s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		row, err := q.CreateAPIToken(ctx, store.CreateAPITokenParams{
			UserID: p.UserID, Name: in.Name, TokenPrefix: prefix, TokenHash: crypto.HashToken(token), Scopes: scopes, ExpiresAt: exp,
		})
		if err != nil {
			return err
		}
		out = CreatedAPIToken{APIToken: toAPIToken(row), Token: token}
		return audit.Record(ctx, q, audit.Entry{
			Action: "token.create", ResourceType: "api_token", ResourceID: row.ID.String(),
			After: map[string]any{"name": row.Name, "prefix": prefix, "scopes": scopes, "expires_at": exp},
		})
	})
	return out, err
}

// ListAPITokens lists the caller's active tokens, newest first.
func (s *Service) ListAPITokens(ctx context.Context, page pagination.Params) (pagination.Page[APIToken], error) {
	p, err := principal(ctx)
	if err != nil {
		return pagination.Page[APIToken]{}, err
	}
	params := store.ListAPITokensParams{UserID: p.UserID, PageSize: page.FetchSize()}
	var cur timeIDCursor
	if ok, err := page.Decode(&cur); err != nil {
		return pagination.Page[APIToken]{}, err
	} else if ok {
		params.CursorCreatedAt, params.CursorID = &cur.CreatedAt, &cur.ID
	}
	rows, err := store.New(s.pool).ListAPITokens(ctx, params)
	if err != nil {
		return pagination.Page[APIToken]{}, err
	}
	return pagination.Build(rows, page.Limit, toAPIToken,
		func(r store.ApiToken) any { return timeIDCursor{CreatedAt: r.CreatedAt, ID: r.ID} }), nil
}

// RevokeAPIToken revokes one of the caller's tokens.
func (s *Service) RevokeAPIToken(ctx context.Context, id uuid.UUID) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *store.Queries, _ pgx.Tx) error {
		_, err := q.RevokeAPIToken(ctx, store.RevokeAPITokenParams{ID: id, UserID: p.UserID})
		if database.IsNoRows(err) {
			return apperr.New(apperr.CodeTokenNotFound, http.StatusNotFound, "token not found")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "token.revoke", ResourceType: "api_token", ResourceID: id.String()})
	})
}

// LookupAPIToken implements authn.APITokenLookup.
func (s *Service) LookupAPIToken(ctx context.Context, hash []byte) (uuid.UUID, uuid.UUID, []string, error) {
	q := store.New(s.pool)
	row, err := q.GetActiveAPITokenByHash(ctx, hash)
	if err != nil {
		return uuid.Nil, uuid.Nil, nil, err
	}
	if err := q.TouchAPIToken(ctx, row.ID); err != nil {
		s.logger.WarnContext(ctx, "touch api token", "error", err)
	}
	return row.UserID, row.ID, row.Scopes, nil
}
