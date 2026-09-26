// Package auditlog shows the append-only audit log (Module 11): an organization's entries
// for Owners and Admins with filters and a CSV export, and each user's own account activity.
// Entries are rendered as sentences in English or Khmer; the recorder is internal/audit.
package auditlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
)

// Service reads and exports the audit log.
type Service struct {
	pool   *pgxpool.Pool
	bundle *i18n.Bundle
	logger *slog.Logger
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, bundle *i18n.Bundle, logger *slog.Logger) *Service {
	return &Service{pool: pool, bundle: bundle, logger: logger, now: time.Now}
}

// Actor is who did it. Name and Email are null for OpsHub, runners and deleted users.
type Actor struct {
	Type   string     `json:"type"`
	UserID *uuid.UUID `json:"user_id"`
	Name   *string    `json:"name"`
	Email  *string    `json:"email"`
}

// ProjectRef is the project of a project-scoped entry; Name is null once it is purged.
type ProjectRef struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

// Entry is one audit log entry. Summary is a sentence in the requested language.
type Entry struct {
	ID           uuid.UUID       `json:"id"`
	CreatedAt    time.Time       `json:"created_at"`
	Action       string          `json:"action"`
	Area         string          `json:"area"`
	Summary      string          `json:"summary"`
	Actor        Actor           `json:"actor"`
	ResourceType string          `json:"resource_type"`
	ResourceID   *string         `json:"resource_id"`
	Project      *ProjectRef     `json:"project"`
	IP           *string         `json:"ip"`
	UserAgent    *string         `json:"user_agent"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
	Metadata     json.RawMessage `json:"metadata"`
}

// Filter narrows an organization's entries. Empty fields are unset.
type Filter struct {
	Areas        []string // action prefixes before the dot, e.g. "secret"
	Actions      []string // exact actions, e.g. "secret.read"
	Actor        string   // user id
	Project      string   // project id
	ResourceType string
	ResourceID   string
	From, To     string // RFC 3339; [from, to)
}

const maxFilterValues = 30

var (
	areaPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}\.[a-z0-9_]{1,40}$`)
)

func fieldErr(field, rule string) apperr.FieldError {
	return apperr.FieldError{Field: field, Rule: rule}
}

// params validates f into query parameters (without organization and paging).
func (f Filter) params() (store.SearchAuditLogParams, error) {
	p := store.SearchAuditLogParams{Areas: []string{}, Actions: []string{}}
	var fields []apperr.FieldError
	if len(f.Areas) > maxFilterValues {
		fields = append(fields, apperr.FieldError{Field: "area", Rule: "max", Param: strconv.Itoa(maxFilterValues)})
	}
	for _, a := range f.Areas {
		if !areaPattern.MatchString(a) {
			fields = append(fields, fieldErr("area", "pattern"))
			break
		}
		p.Areas = append(p.Areas, a)
	}
	if len(f.Actions) > maxFilterValues {
		fields = append(fields, apperr.FieldError{Field: "action", Rule: "max", Param: strconv.Itoa(maxFilterValues)})
	}
	for _, a := range f.Actions {
		if !actionPattern.MatchString(a) {
			fields = append(fields, fieldErr("action", "pattern"))
			break
		}
		p.Actions = append(p.Actions, a)
	}
	if f.Actor != "" {
		id, err := uuid.Parse(f.Actor)
		if err != nil {
			fields = append(fields, fieldErr("actor", "uuid"))
		}
		p.ActorUserID = &id
	}
	if f.Project != "" {
		id, err := uuid.Parse(f.Project)
		if err != nil {
			fields = append(fields, fieldErr("project", "uuid"))
		}
		s := id.String()
		p.ProjectID = &s
	}
	if f.ResourceType != "" {
		p.ResourceType = &f.ResourceType
	}
	if f.ResourceID != "" {
		p.ResourceID = &f.ResourceID
	}
	for _, t := range []struct {
		field, v string
		dst      **time.Time
	}{{"from", f.From, &p.FromTs}, {"to", f.To, &p.ToTs}} {
		if t.v == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, t.v)
		if err != nil {
			fields = append(fields, fieldErr(t.field, "datetime"))
			continue
		}
		*t.dst = &ts
	}
	if p.FromTs != nil && p.ToTs != nil && !p.FromTs.Before(*p.ToTs) {
		fields = append(fields, fieldErr("to", "after_from"))
	}
	if len(fields) > 0 {
		return p, apperr.Validation(fields)
	}
	return p, nil
}

// Locale returns loc when it is a supported language, else the request's language.
func Locale(ctx context.Context, loc string) string {
	if l := i18n.Normalize(loc); l != "" && loc != "" {
		return l
	}
	return i18n.LocaleFrom(ctx)
}

type cursorKey struct {
	T time.Time `json:"t"`
	I uuid.UUID `json:"i"`
}

func searchRow(r store.SearchAuditLogRow) row {
	return row{
		ActorType: r.ActorType, ActorUserID: r.ActorUserID, Action: r.Action, ResourceType: r.ResourceType,
		ResourceID: r.ResourceID, Before: r.Before, After: r.After, Metadata: r.Metadata, ActorName: r.ActorName,
		ActorEmail: r.ActorEmail, ProjectName: r.ProjectName, ResourceUserName: r.ResourceUserName,
		ResourceUserEmail: r.ResourceUserEmail,
	}
}

// lookupNames resolves team and user ids that entries mention only by id.
func lookupNames(ctx context.Context, q *store.Queries, rows []row) (names, error) {
	n := names{teams: map[uuid.UUID]string{}, users: map[uuid.UUID]string{}, assets: map[uuid.UUID]string{}}
	var teamIDs, userIDs, assetIDs []uuid.UUID
	for _, r := range rows {
		if r.Action == "asset.agent_token" && r.ResourceID != nil {
			if id, err := uuid.Parse(*r.ResourceID); err == nil {
				assetIDs = append(assetIDs, id)
			}
		}
		if r.ResourceType == "team" && r.ResourceID != nil {
			if id, err := uuid.Parse(*r.ResourceID); err == nil {
				teamIDs = append(teamIDs, id)
			}
		}
		if r.Action == "team.add_member" || r.Action == "team.remove_member" {
			for _, m := range []map[string]any{object(r.After), object(r.Before)} {
				if id, err := uuid.Parse(text(m, "user_id")); err == nil {
					userIDs = append(userIDs, id)
				}
			}
		}
	}
	if len(teamIDs) > 0 {
		ts, err := q.AuditTeamNames(ctx, teamIDs)
		if err != nil {
			return n, err
		}
		for _, t := range ts {
			n.teams[t.ID] = t.Name
		}
	}
	if len(assetIDs) > 0 {
		as, err := q.AuditAssetNames(ctx, assetIDs)
		if err != nil {
			return n, err
		}
		for _, a := range as {
			n.assets[a.ID] = a.Name
		}
	}
	if len(userIDs) > 0 {
		us, err := q.AuditUserNames(ctx, userIDs)
		if err != nil {
			return n, err
		}
		for _, u := range us {
			n.users[u.ID] = first(u.DisplayName, u.Email)
		}
	}
	return n, nil
}

func rawOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

func area(action string) string {
	for i := range len(action) {
		if action[i] == '.' {
			return action[:i]
		}
	}
	return action
}

// entry converts a row for the API, with its sentence in loc.
func (s *Service) entry(loc string, id uuid.UUID, at time.Time, ip *string, ua *string, r row, n names) Entry {
	e := Entry{
		ID: id, CreatedAt: at, Action: r.Action, Area: area(r.Action), Summary: describe(s.bundle, loc, r, n),
		Actor:        Actor{Type: r.ActorType, UserID: r.ActorUserID, Name: r.ActorName},
		ResourceType: r.ResourceType, ResourceID: r.ResourceID, IP: ip, UserAgent: ua,
		Before: rawOrNull(r.Before), After: rawOrNull(r.After), Metadata: rawOrNull(r.Metadata),
	}
	if r.ActorEmail != "" {
		email := r.ActorEmail
		e.Actor.Email = &email
	}
	if pid := text(object(r.Metadata), "project_id"); pid != "" {
		e.Project = &ProjectRef{ID: pid, Name: r.ProjectName}
	}
	if e.Metadata == nil {
		e.Metadata = json.RawMessage(`{}`)
	}
	return e
}

func ipString(r store.SearchAuditLogRow) *string {
	if r.Ip == nil {
		return nil
	}
	s := r.Ip.String()
	return &s
}

// List returns an organization's entries, newest first (audit.view).
func (s *Service) List(ctx context.Context, orgID uuid.UUID, f Filter, pg pagination.Params, loc string) (pagination.Page[Entry], error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.AuditView); err != nil {
		return pagination.Page[Entry]{}, err
	}
	p, err := f.params()
	if err != nil {
		return pagination.Page[Entry]{}, err
	}
	var key cursorKey
	ok, err := pg.Decode(&key)
	if err != nil {
		return pagination.Page[Entry]{}, err
	}
	if ok {
		p.BeforeCreatedAt, p.BeforeID = &key.T, &key.I
	}
	p.OrganizationID, p.MaxRows = &orgID, pg.FetchSize()
	rows, err := q.SearchAuditLog(ctx, p)
	if err != nil {
		return pagination.Page[Entry]{}, err
	}
	return s.page(ctx, q, rows, pg.Limit, loc)
}

func (s *Service) page(ctx context.Context, q *store.Queries, rows []store.SearchAuditLogRow, limit int, loc string) (pagination.Page[Entry], error) {
	conv := make([]row, len(rows))
	for i, r := range rows {
		conv[i] = searchRow(r)
	}
	n, err := lookupNames(ctx, q, conv)
	if err != nil {
		return pagination.Page[Entry]{}, err
	}
	i := 0
	return pagination.Build(rows, limit, func(r store.SearchAuditLogRow) Entry {
		e := s.entry(loc, r.ID, r.CreatedAt, ipString(r), r.UserAgent, conv[i], n)
		i++
		return e
	}, func(r store.SearchAuditLogRow) any { return cursorKey{T: r.CreatedAt, I: r.ID} }), nil
}

// Activity returns the caller's own account events (sign-ins, 2FA, password, tokens,
// sessions), newest first.
func (s *Service) Activity(ctx context.Context, pg pagination.Params, loc string) (pagination.Page[Entry], error) {
	pr, ok := authn.PrincipalFrom(ctx)
	if !ok {
		return pagination.Page[Entry]{}, apperr.Unauthenticated()
	}
	p := store.AccountActivityParams{UserID: &pr.UserID, MaxRows: pg.FetchSize()}
	var key cursorKey
	found, err := pg.Decode(&key)
	if err != nil {
		return pagination.Page[Entry]{}, err
	}
	if found {
		p.BeforeCreatedAt, p.BeforeID = &key.T, &key.I
	}
	q := store.New(s.pool)
	rows, err := q.AccountActivity(ctx, p)
	if err != nil {
		return pagination.Page[Entry]{}, err
	}
	return pagination.Build(rows, pg.Limit, func(r store.AccountActivityRow) Entry {
		var ip *string
		if r.Ip != nil {
			v := r.Ip.String()
			ip = &v
		}
		return s.entry(loc, r.ID, r.CreatedAt, ip, r.UserAgent, row{
			ActorType: r.ActorType, ActorUserID: r.ActorUserID, Action: r.Action, ResourceType: r.ResourceType,
			ResourceID: r.ResourceID, Before: r.Before, After: r.After, Metadata: r.Metadata,
			ActorName: r.ActorName, ActorEmail: r.ActorEmail,
		}, names{})
	}, func(r store.AccountActivityRow) any { return cursorKey{T: r.CreatedAt, I: r.ID} }), nil
}
