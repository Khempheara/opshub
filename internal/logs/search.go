package logs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/store"
)

// Search limits.
const (
	DefaultRange  = time.Hour
	MaxRange      = 31 * 24 * time.Hour
	DefaultLimit  = 200
	MaxLimit      = 500
	maxQueryRunes = 200
)

// Query is GET /orgs/{id}/logs. Empty fields are unset.
type Query struct {
	From    string // RFC 3339; default To − 1 h
	To      string // RFC 3339; default now (+ the accepted clock skew)
	Q       string // search text
	Source  string // service | job | deployment
	Service string
	Level   string // minimum level
	Before  string // cursor: older lines than this one
	After   string // cursor: newer lines than this one (follow mode)
	Limit   string
}

var (
	validSources = map[store.LogSource]bool{store.LogSourceService: true, store.LogSourceJob: true, store.LogSourceDeployment: true}
	validLevels  = map[store.LogLevel]bool{store.LogLevelDebug: true, store.LogLevelInfo: true, store.LogLevelWarn: true, store.LogLevelError: true}
)

// Entry is one log line.
type Entry struct {
	ID         uuid.UUID       `json:"id"`
	Ts         time.Time       `json:"ts"`
	Source     string          `json:"source"`
	ProjectID  *uuid.UUID      `json:"project_id"`
	Service    string          `json:"service"`
	Level      string          `json:"level"`
	Message    string          `json:"message"`
	Attributes json.RawMessage `json:"attributes"`
}

// Result is a page of lines, newest first. NextCursor pages to older lines; NewestCursor is
// passed as "after" to fetch only lines newer than this page (follow mode).
type Result struct {
	Items        []Entry `json:"items"`
	NextCursor   *string `json:"next_cursor"`
	NewestCursor *string `json:"newest_cursor"`
}

type cursorKey struct {
	Ts time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

func encodeCursor(ts time.Time, id uuid.UUID) *string {
	raw, _ := json.Marshal(cursorKey{Ts: ts, ID: id})
	c := base64.RawURLEncoding.EncodeToString(raw)
	return &c
}

func decodeCursor(field, s string) (*cursorKey, *apperr.FieldError) {
	if s == "" {
		return nil, nil
	}
	var k cursorKey
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(raw, &k) != nil || k.ID == uuid.Nil {
		return nil, &apperr.FieldError{Field: field, Rule: "cursor"}
	}
	return &k, nil
}

// hasKhmer reports whether s contains Khmer script. Khmer is written without spaces between
// words, so full-text tokens would be whole phrases; such queries use a substring match.
func hasKhmer(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Khmer, r) {
			return true
		}
	}
	return false
}

// splitWords splits a full-text query at the same characters the search column does.
var splitWords = strings.NewReplacer("/", " ", ":", " ", "=", " ", ".", " ")

// likeEscape escapes LIKE wildcards (the query uses ESCAPE '\').
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// parseQuery validates a query into search parameters (everything except org and visibility).
func (s *Service) parseQuery(in Query) (store.SearchLogsParams, int, error) {
	var (
		p      store.SearchLogsParams
		fields []apperr.FieldError
		limit  = DefaultLimit
		now    = s.now()
	)
	p.ToTs = now.Add(MaxSkew)
	if in.To != "" {
		t, err := time.Parse(time.RFC3339Nano, in.To)
		if err != nil {
			fields = append(fields, apperr.FieldError{Field: "to", Rule: "datetime"})
		}
		p.ToTs = t
	}
	p.FromTs = p.ToTs.Add(-DefaultRange)
	if in.From != "" {
		t, err := time.Parse(time.RFC3339Nano, in.From)
		if err != nil {
			fields = append(fields, apperr.FieldError{Field: "from", Rule: "datetime"})
		}
		p.FromTs = t
	}
	if !p.FromTs.Before(p.ToTs) || p.ToTs.Sub(p.FromTs) > MaxRange {
		fields = append(fields, apperr.FieldError{Field: "from", Rule: "range", Param: "31d"})
	}
	if q := strings.TrimSpace(in.Q); q != "" {
		switch {
		case len([]rune(q)) > maxQueryRunes:
			fields = append(fields, apperr.FieldError{Field: "q", Rule: "max", Param: strconv.Itoa(maxQueryRunes)})
		case hasKhmer(q):
			c := likeEscape(q)
			p.Contains = &c
		default:
			fts := splitWords.Replace(q)
			p.Fts = &fts
		}
	}
	if in.Source != "" {
		src := store.LogSource(in.Source)
		if !validSources[src] {
			fields = append(fields, apperr.FieldError{Field: "source", Rule: "oneof", Param: "service job deployment"})
		}
		p.Source = &src
	}
	if in.Service != "" {
		svc := in.Service
		p.Service = &svc
	}
	if in.Level != "" {
		l := store.LogLevel(in.Level)
		if !validLevels[l] {
			fields = append(fields, apperr.FieldError{Field: "level", Rule: "oneof", Param: "debug info warn error"})
		}
		p.MinLevel = &l
	}
	before, fe := decodeCursor("before", in.Before)
	if fe != nil {
		fields = append(fields, *fe)
	} else if before != nil {
		p.BeforeTs, p.BeforeID = &before.Ts, &before.ID
	}
	after, fe := decodeCursor("after", in.After)
	if fe != nil {
		fields = append(fields, *fe)
	} else if after != nil {
		p.AfterTs, p.AfterID = &after.Ts, &after.ID
	}
	if in.Limit != "" {
		n, err := strconv.Atoi(in.Limit)
		if err != nil || n < 1 || n > MaxLimit {
			fields = append(fields, apperr.FieldError{Field: "limit", Rule: "range", Param: "1-" + strconv.Itoa(MaxLimit)})
		} else {
			limit = n
		}
	}
	if len(fields) > 0 {
		return p, 0, apperr.Validation(fields)
	}
	p.MaxRows = int32(limit + 1) // #nosec G115 -- 1..MaxLimit
	return p, limit, nil
}

// visibility returns whether a member sees every project and, if not, which ones.
func visibility(ctx context.Context, q *store.Queries, m authz.Membership) (bool, []uuid.UUID, error) {
	_, seeAll := authz.InheritedProjectRole(m.Role)
	ids, err := q.VisibleProjectIDs(ctx, store.VisibleProjectIDsParams{OrganizationID: m.OrganizationID, SeeAll: seeAll, UserID: m.UserID})
	return seeAll, ids, err
}

// Search returns log lines, newest first (logs.view). Job and deployment lines are shown
// only for projects the caller can see; service lines to every member.
func (s *Service) Search(ctx context.Context, orgID uuid.UUID, in Query) (Result, error) {
	q := store.New(s.pool)
	m, err := authz.Require(ctx, q, orgID, authz.LogsView)
	if err != nil {
		return Result{}, err
	}
	p, limit, err := s.parseQuery(in)
	if err != nil {
		return Result{}, err
	}
	p.OrganizationID = orgID
	if p.SeeAll, p.ProjectIds, err = visibility(ctx, q, m); err != nil {
		return Result{}, err
	}
	rows, err := q.SearchLogs(ctx, p)
	if err != nil {
		return Result{}, err
	}
	res := Result{Items: make([]Entry, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			res.NextCursor = encodeCursor(last.Ts, last.ID)
			break
		}
		attrs := json.RawMessage(r.Attributes)
		if len(attrs) == 0 {
			attrs = json.RawMessage(`{}`)
		}
		res.Items = append(res.Items, Entry{
			ID: r.ID, Ts: r.Ts, Source: string(r.Source), ProjectID: r.ProjectID, Service: r.Service,
			Level: string(r.Level), Message: r.Message, Attributes: attrs,
		})
	}
	switch {
	case len(res.Items) > 0:
		res.NewestCursor = encodeCursor(res.Items[0].Ts, res.Items[0].ID)
	case in.After != "":
		res.NewestCursor = &in.After // nothing new: keep following from the same line
	}
	return res, nil
}

// Services lists service names seen in the last day that the caller can see (logs.view).
func (s *Service) Services(ctx context.Context, orgID uuid.UUID) ([]string, error) {
	q := store.New(s.pool)
	m, err := authz.Require(ctx, q, orgID, authz.LogsView)
	if err != nil {
		return nil, err
	}
	seeAll, ids, err := visibility(ctx, q, m)
	if err != nil {
		return nil, err
	}
	return q.RecentLogServices(ctx, store.RecentLogServicesParams{OrganizationID: orgID, SeeAll: seeAll, ProjectIds: ids})
}
