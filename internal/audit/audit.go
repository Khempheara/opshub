// Package audit records security-relevant and mutating actions in the append-only
// audit_log table. Entries are written inside the same transaction as the change they
// describe, so an action is never committed without its audit record.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/store"
)

// Entry describes one audited action. Before/After/Metadata must never contain secrets.
type Entry struct {
	OrganizationID *uuid.UUID
	// ProjectID is stored in metadata ("project_id") for project-scoped events.
	ProjectID    *uuid.UUID
	Action       string // "<resource>.<verb>", e.g. "auth.login", "org.create"
	ResourceType string
	ResourceID   string
	Before       any
	After        any
	Metadata     map[string]any
	// ActorUserID overrides the request principal (e.g. login, where no principal exists yet).
	ActorUserID *uuid.UUID
	// ActorType overrides the derived actor type ("system" for background jobs).
	ActorType string
}

type userAgentKey struct{}

// Middleware stores the request's User-Agent for Record.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userAgentKey{}, r.UserAgent())))
	})
}

// RequestIP returns the resolved client IP of the current request, if any.
func RequestIP(ctx context.Context) *netip.Addr {
	if a := middleware.GetClientIPAddr(ctx); a.IsValid() {
		return &a
	}
	return nil
}

// RequestUserAgent returns the current request's User-Agent (truncated to 512 bytes).
func RequestUserAgent(ctx context.Context) string {
	ua, _ := ctx.Value(userAgentKey{}).(string)
	if len(ua) > 512 {
		ua = ua[:512]
	}
	return ua
}

// Record writes e using q (a transaction-bound store).
func Record(ctx context.Context, q *store.Queries, e Entry) error {
	actorType, actorID := "system", e.ActorUserID
	if p, ok := authn.PrincipalFrom(ctx); ok {
		actorType = "user"
		if p.Kind == authn.KindAPIToken {
			actorType = "api_token"
			if e.Metadata == nil {
				e.Metadata = map[string]any{}
			}
			e.Metadata["api_token_id"] = p.TokenID.String()
		}
		if actorID == nil {
			actorID = &p.UserID
		}
	} else if actorID != nil {
		actorType = "user"
	}
	if e.ActorType != "" {
		actorType = e.ActorType
	}
	if e.ProjectID != nil {
		if e.Metadata == nil {
			e.Metadata = map[string]any{}
		}
		e.Metadata["project_id"] = e.ProjectID.String()
	}
	before, err := marshalOptional(e.Before)
	if err != nil {
		return err
	}
	after, err := marshalOptional(e.After)
	if err != nil {
		return err
	}
	meta := []byte("{}")
	if len(e.Metadata) > 0 {
		if meta, err = json.Marshal(e.Metadata); err != nil {
			return fmt.Errorf("audit metadata: %w", err)
		}
	}
	var resourceID *string
	if e.ResourceID != "" {
		resourceID = &e.ResourceID
	}
	ua := RequestUserAgent(ctx)
	var uaPtr *string
	if ua != "" {
		uaPtr = &ua
	}
	_, err = q.InsertAuditLog(ctx, store.InsertAuditLogParams{
		OrganizationID: e.OrganizationID,
		ActorUserID:    actorID,
		ActorType:      actorType,
		Action:         e.Action,
		ResourceType:   e.ResourceType,
		ResourceID:     resourceID,
		Ip:             RequestIP(ctx),
		UserAgent:      uaPtr,
		Before:         before,
		After:          after,
		Metadata:       meta,
	})
	if err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}

func marshalOptional(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit payload: %w", err)
	}
	return b, nil
}
