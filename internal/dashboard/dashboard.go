// Package dashboard computes the organization dashboard (Module 12): pipeline statistics and
// the four DORA metrics, on request from runs, deployments and alerts. Members see the
// projects they can view; alerts are organization-wide.
package dashboard

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/store"
)

// Range limits.
const (
	DefaultRange = 30 * 24 * time.Hour
	MaxRange     = 366 * 24 * time.Hour
	// Ranges up to this long are shown per day, longer ones per week.
	DailyUpTo = 92 * 24 * time.Hour
)

// Service computes dashboard figures.
type Service struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool, now: time.Now} }

// Query is the dashboard filter. Empty fields are unset.
type Query struct {
	From        string // RFC 3339; default To − 30 days
	To          string // RFC 3339; default now
	Project     string // project id
	Environment string // environment id; default: every production environment
	TZ          string // IANA time zone for daily/weekly periods; default UTC
}

// Range is the resolved range, echoed in responses.
type Range struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Bucket string    `json:"bucket"` // day or week
	TZ     string    `json:"tz"`
}

// scope is a validated query with the caller's project visibility.
type scope struct {
	orgID       uuid.UUID
	rng         Range
	loc         *time.Location
	seeAll      bool
	projectIDs  []uuid.UUID
	project     *uuid.UUID
	environment *uuid.UUID
}

func fieldErr(field, rule string) apperr.FieldError {
	return apperr.FieldError{Field: field, Rule: rule}
}

// parse validates q (everything but visibility).
func (s *Service) parse(q Query) (scope, error) {
	var (
		sc     scope
		fields []apperr.FieldError
	)
	sc.rng.To = s.now().UTC()
	if q.To != "" {
		t, err := time.Parse(time.RFC3339Nano, q.To)
		if err != nil {
			fields = append(fields, fieldErr("to", "datetime"))
		}
		sc.rng.To = t.UTC()
	}
	sc.rng.From = sc.rng.To.Add(-DefaultRange)
	if q.From != "" {
		t, err := time.Parse(time.RFC3339Nano, q.From)
		if err != nil {
			fields = append(fields, fieldErr("from", "datetime"))
		}
		sc.rng.From = t.UTC()
	}
	if !sc.rng.From.Before(sc.rng.To) || sc.rng.To.Sub(sc.rng.From) > MaxRange {
		fields = append(fields, apperr.FieldError{Field: "from", Rule: "range", Param: "366d"})
	}
	sc.rng.Bucket = "day"
	if sc.rng.To.Sub(sc.rng.From) > DailyUpTo {
		sc.rng.Bucket = "week"
	}
	sc.rng.TZ = "UTC"
	if q.TZ != "" {
		sc.rng.TZ = q.TZ
	}
	loc, err := time.LoadLocation(sc.rng.TZ)
	if err != nil || sc.rng.TZ == "Local" {
		fields = append(fields, fieldErr("tz", "timezone"))
	}
	sc.loc = loc
	for _, f := range []struct {
		name, v string
		dst     **uuid.UUID
	}{{"project", q.Project, &sc.project}, {"environment", q.Environment, &sc.environment}} {
		if f.v == "" {
			continue
		}
		id, err := uuid.Parse(f.v)
		if err != nil {
			fields = append(fields, fieldErr(f.name, "uuid"))
			continue
		}
		*f.dst = &id
	}
	if len(fields) > 0 {
		return sc, apperr.Validation(fields)
	}
	return sc, nil
}

func errProjectNotFound() *apperr.Error {
	return apperr.New(apperr.CodeProjectNotFound, http.StatusNotFound, "project not found")
}

// resolve checks org membership and the caller's project visibility. A project the caller
// can't see answers 404, like everywhere else.
func (s *Service) resolve(ctx context.Context, q *store.Queries, orgID uuid.UUID, in Query) (scope, error) {
	m, err := authz.Require(ctx, q, orgID, authz.OrgView)
	if err != nil {
		return scope{}, err
	}
	sc, err := s.parse(in)
	if err != nil {
		return scope{}, err
	}
	sc.orgID = orgID
	_, sc.seeAll = authz.InheritedProjectRole(m.Role)
	ids, err := q.VisibleProjectIDs(ctx, store.VisibleProjectIDsParams{OrganizationID: orgID, SeeAll: sc.seeAll, UserID: m.UserID})
	if err != nil {
		return scope{}, err
	}
	sc.projectIDs = ids
	if sc.project != nil && !slices.Contains(ids, *sc.project) {
		return scope{}, errProjectNotFound()
	}
	return sc, nil
}

// seconds turns a query's -1 ("nothing to measure") into null.
func seconds(v float64) *float64 {
	if v < 0 {
		return nil
	}
	return &v
}

// ratio is part/whole, or null without a whole.
func ratio(part, whole int32) *float64 {
	if whole <= 0 {
		return nil
	}
	r := float64(part) / float64(whole)
	return &r
}

// periods lists every period start (YYYY-MM-DD, in loc) from from to to: days, or weeks
// starting on Monday like PostgreSQL's date_trunc('week').
func periods(r Range, loc *time.Location) []string {
	start := r.From.In(loc)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	step := 1
	if r.Bucket == "week" {
		offset := (int(start.Weekday()) + 6) % 7 // days since Monday
		start = start.AddDate(0, 0, -offset)
		step = 7
	}
	var out []string
	for d := start; d.Before(r.To); d = d.AddDate(0, 0, step) {
		out = append(out, d.Format(time.DateOnly))
	}
	return out
}
