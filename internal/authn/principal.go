package authn

import (
	"context"
	"slices"

	"github.com/google/uuid"
)

// PrincipalKind distinguishes interactive sessions from personal API tokens.
type PrincipalKind int

const (
	KindSession PrincipalKind = iota + 1
	KindAPIToken
)

// API token scopes.
const (
	ScopeRead  = "api:read"
	ScopeWrite = "api:write"
)

// AllScopes are the scopes a personal API token may carry.
var AllScopes = []string{ScopeRead, ScopeWrite}

// Principal is the authenticated caller of a request.
type Principal struct {
	Kind      PrincipalKind
	UserID    uuid.UUID
	SessionID uuid.UUID // KindSession only
	TokenID   uuid.UUID // KindAPIToken only
	Scopes    []string  // KindAPIToken only
}

// IsSession reports whether the caller signed in interactively (not an API token).
func (p Principal) IsSession() bool { return p.Kind == KindSession }

// CanWrite reports whether the principal may perform mutating requests.
func (p Principal) CanWrite() bool {
	return p.Kind == KindSession || slices.Contains(p.Scopes, ScopeWrite)
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the request principal, if authenticated.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
