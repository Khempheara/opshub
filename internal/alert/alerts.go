package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/monitor"
	"github.com/opshub/opshub/internal/notify"
	"github.com/opshub/opshub/internal/store"
)

// MaxList is the most alerts one list returns.
const MaxList = 100

// Alert is the API representation.
type Alert struct {
	ID                 uuid.UUID           `json:"id"`
	RuleID             *uuid.UUID          `json:"rule_id"`
	RuleName           string              `json:"rule_name"`
	RuleKind           store.AlertRuleKind `json:"rule_kind"`
	Severity           store.AlertSeverity `json:"severity"`
	SubjectType        string              `json:"subject_type"`
	SubjectID          uuid.UUID           `json:"subject_id"`
	SubjectName        string              `json:"subject_name"`
	SubjectLabels      []string            `json:"subject_labels"`
	Status             store.AlertStatus   `json:"status"`
	Details            notify.Details      `json:"details"`
	StartedAt          *time.Time          `json:"started_at"`
	ResolvedAt         *time.Time          `json:"resolved_at"`
	AcknowledgedAt     *time.Time          `json:"acknowledged_at"`
	AcknowledgedByName *string             `json:"acknowledged_by_name"`
	// Silenced: an active silence mutes this alert's notifications now.
	Silenced bool `json:"silenced"`
}

// Event is one entry of an alert's timeline.
type Event struct {
	At          time.Time  `json:"at"`
	Kind        string     `json:"kind"`
	ChannelID   *uuid.UUID `json:"channel_id"`
	ChannelName string     `json:"channel_name"`
	UserName    *string    `json:"user_name"`
	Detail      string     `json:"detail"`
}

// AlertDetail is GET /alerts/{id}.
type AlertDetail struct {
	Alert
	Events []Event `json:"events"`
}

func toAlert(a store.Alert, ackName *string, active []store.Silence) Alert {
	out := Alert{
		ID: a.ID, RuleID: a.RuleID, RuleName: a.RuleName, RuleKind: a.RuleKind, Severity: a.Severity, SubjectType: a.SubjectType,
		SubjectID: a.SubjectID, SubjectName: a.SubjectName, SubjectLabels: a.SubjectLabels, Status: a.Status, StartedAt: a.StartedAt,
		ResolvedAt: a.ResolvedAt, AcknowledgedAt: a.AcknowledgedAt, AcknowledgedByName: ackName,
	}
	if out.SubjectLabels == nil {
		out.SubjectLabels = []string{}
	}
	_ = json.Unmarshal(a.Details, &out.Details)
	if a.Status == store.AlertStatusFiring {
		out.Silenced = slices.ContainsFunc(active, func(sl store.Silence) bool { return silenceMatches(sl, a) })
	}
	return out
}

func errAlertNotFound() *apperr.Error {
	return apperr.New(apperr.CodeAlertNotFound, http.StatusNotFound, "alert not found")
}

// AlertFilter narrows the alert list.
type AlertFilter struct {
	Status   *store.AlertStatus
	Severity *store.AlertSeverity
	Before   *time.Time // for paging: alerts that started before this time
}

// ListAlerts returns firing alerts first, then resolved ones, newest first (monitor.view).
func (s *Service) ListAlerts(ctx context.Context, orgID uuid.UUID, f AlertFilter) ([]Alert, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.MonitorView); err != nil {
		return nil, err
	}
	rows, err := q.ListAlerts(ctx, store.ListAlertsParams{OrganizationID: orgID, Status: f.Status, Severity: f.Severity, Before: f.Before, MaxAlerts: MaxList})
	if err != nil {
		return nil, err
	}
	active, err := q.ActiveSilences(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Alert, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAlert(r.Alert, r.AcknowledgedByName, active))
	}
	return out, nil
}

func (s *Service) loadAlert(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.GetAlertRow, authz.Membership, error) {
	row, err := q.GetAlert(ctx, id)
	if database.IsNoRows(err) || (err == nil && row.Alert.Status == store.AlertStatusPending) {
		return row, authz.Membership{}, errAlertNotFound()
	}
	if err != nil {
		return row, authz.Membership{}, err
	}
	m, err := authz.Require(ctx, q, row.Alert.OrganizationID, a)
	return row, m, orgHidden(err, errAlertNotFound)
}

// GetAlert returns an alert with its timeline (monitor.view).
func (s *Service) GetAlert(ctx context.Context, id uuid.UUID) (AlertDetail, error) {
	q := store.New(s.pool)
	row, _, err := s.loadAlert(ctx, q, id, authz.MonitorView)
	if err != nil {
		return AlertDetail{}, err
	}
	active, err := q.ActiveSilences(ctx, row.Alert.OrganizationID)
	if err != nil {
		return AlertDetail{}, err
	}
	events, err := q.ListAlertEvents(ctx, id)
	if err != nil {
		return AlertDetail{}, err
	}
	out := AlertDetail{Alert: toAlert(row.Alert, row.AcknowledgedByName, active), Events: make([]Event, 0, len(events))}
	for _, e := range events {
		out.Events = append(out.Events, Event{At: e.At, Kind: e.Kind, ChannelID: e.ChannelID, ChannelName: e.ChannelName, UserName: e.UserName, Detail: e.Detail})
	}
	return out, nil
}

// Acknowledge stops a firing alert's escalation (alert.ack). It keeps firing until its
// condition clears.
func (s *Service) Acknowledge(ctx context.Context, id uuid.UUID) (AlertDetail, error) {
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		row, m, err := s.loadAlert(ctx, q, id, authz.AlertAck)
		if err != nil {
			return err
		}
		if _, err := q.AcknowledgeAlert(ctx, store.AcknowledgeAlertParams{ID: id, UserID: &m.UserID}); err != nil {
			if database.IsNoRows(err) {
				return apperr.New(apperr.CodeAlertNotFiring, http.StatusConflict, "the alert isn't firing or is already acknowledged")
			}
			return err
		}
		if err := q.InsertAlertEvent(ctx, store.InsertAlertEventParams{AlertID: id, Kind: "acknowledged", UserID: &m.UserID}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &row.Alert.OrganizationID, Action: "alert.ack", ResourceType: "alert", ResourceID: id.String(),
			Metadata: map[string]any{"rule": row.Alert.RuleName, "subject": row.Alert.SubjectName},
		})
	})
	if err != nil {
		return AlertDetail{}, err
	}
	return s.GetAlert(ctx, id)
}

// Silence is the API representation.
type Silence struct {
	ID            uuid.UUID            `json:"id"`
	RuleID        *uuid.UUID           `json:"rule_id"`
	RuleName      *string              `json:"rule_name"`
	SubjectID     *uuid.UUID           `json:"subject_id"`
	Label         *string              `json:"label"`
	Severity      *store.AlertSeverity `json:"severity"`
	Comment       string               `json:"comment"`
	StartsAt      time.Time            `json:"starts_at"`
	EndsAt        time.Time            `json:"ends_at"`
	Active        bool                 `json:"active"`
	CreatedByName *string              `json:"created_by_name"`
	CreatedAt     time.Time            `json:"created_at"`
}

func errSilenceNotFound() *apperr.Error {
	return apperr.New(apperr.CodeSilenceNotFound, http.StatusNotFound, "silence not found")
}

// SilenceInput is POST /orgs/{id}/silences. At least one matcher is required; StartsAt
// defaults to now.
type SilenceInput struct {
	RuleID    *uuid.UUID           `json:"rule_id"`
	SubjectID *uuid.UUID           `json:"subject_id"`
	Label     *string              `json:"label"`
	Severity  *store.AlertSeverity `json:"severity"`
	Comment   string               `json:"comment"`
	StartsAt  *time.Time           `json:"starts_at"`
	EndsAt    time.Time            `json:"ends_at"`
}

// MaxSilence is the longest a silence can last.
const MaxSilence = 90 * 24 * time.Hour

// ListSilences returns active and upcoming silences, and expired ones when asked
// (monitor.view).
func (s *Service) ListSilences(ctx context.Context, orgID uuid.UUID, includeExpired bool) ([]Silence, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.MonitorView); err != nil {
		return nil, err
	}
	rows, err := q.ListSilences(ctx, store.ListSilencesParams{OrganizationID: orgID, IncludeExpired: includeExpired})
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]Silence, 0, len(rows))
	for _, r := range rows {
		out = append(out, Silence{
			ID: r.ID, RuleID: r.RuleID, RuleName: r.RuleName, SubjectID: r.SubjectID, Label: r.Label, Severity: r.Severity,
			Comment: r.Comment, StartsAt: r.StartsAt, EndsAt: r.EndsAt, Active: !r.StartsAt.After(now) && r.EndsAt.After(now),
			CreatedByName: r.CreatedByName, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// CreateSilence mutes matching alerts between StartsAt and EndsAt (monitor.manage).
func (s *Service) CreateSilence(ctx context.Context, orgID uuid.UUID, in SilenceInput) (Silence, error) {
	var out Silence
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		m, err := authz.Require(ctx, q, orgID, authz.MonitorManage)
		if err != nil {
			return err
		}
		now := s.now()
		var fields []apperr.FieldError
		add := func(field, rule, param string) {
			fields = append(fields, apperr.FieldError{Field: field, Rule: rule, Param: param})
		}
		starts := now
		if in.StartsAt != nil {
			starts = *in.StartsAt
		}
		switch {
		case !in.EndsAt.After(starts) || !in.EndsAt.After(now):
			add("ends_at", "after", "starts_at")
		case in.EndsAt.Sub(starts) > MaxSilence:
			add("ends_at", "max_range", "90d")
		}
		if in.Label != nil {
			l := strings.ToLower(strings.TrimSpace(*in.Label))
			switch {
			case l == "":
				in.Label = nil
			case !monitor.LabelPattern.MatchString(l):
				add("label", "pattern", "")
			default:
				in.Label = &l
			}
		}
		if in.Severity != nil && !slices.Contains([]store.AlertSeverity{store.AlertSeverityInfo, store.AlertSeverityWarning, store.AlertSeverityCritical}, *in.Severity) {
			add("severity", "oneof", "info warning critical")
		}
		in.Comment = strings.TrimSpace(in.Comment)
		if utf8.RuneCountInString(in.Comment) > 500 {
			add("comment", "max", "500")
		}
		if in.RuleID == nil && in.SubjectID == nil && in.Label == nil && in.Severity == nil {
			add("matchers", "required", "")
		}
		if in.RuleID != nil {
			r, err := q.GetAlertRule(ctx, *in.RuleID)
			if err != nil && !database.IsNoRows(err) {
				return err
			}
			if err != nil || r.OrganizationID != orgID {
				add("rule_id", "exists", "")
			}
		}
		if in.SubjectID != nil {
			ok, err := subjectExists(ctx, q, orgID, *in.SubjectID)
			if err != nil {
				return err
			}
			if !ok {
				add("subject_id", "exists", "")
			}
		}
		if len(fields) > 0 {
			return apperr.Validation(fields)
		}
		sl, err := q.CreateSilence(ctx, store.CreateSilenceParams{
			OrganizationID: orgID, RuleID: in.RuleID, SubjectID: in.SubjectID, Label: in.Label, Severity: in.Severity,
			Comment: in.Comment, StartsAt: starts, EndsAt: in.EndsAt, CreatedBy: &m.UserID,
		})
		if err != nil {
			return err
		}
		out = Silence{
			ID: sl.ID, RuleID: sl.RuleID, SubjectID: sl.SubjectID, Label: sl.Label, Severity: sl.Severity, Comment: sl.Comment,
			StartsAt: sl.StartsAt, EndsAt: sl.EndsAt, Active: !sl.StartsAt.After(now), CreatedAt: sl.CreatedAt,
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "silence.create", ResourceType: "silence", ResourceID: sl.ID.String(),
			After: map[string]any{"rule_id": sl.RuleID, "subject_id": sl.SubjectID, "label": sl.Label, "severity": sl.Severity,
				"comment": sl.Comment, "starts_at": sl.StartsAt, "ends_at": sl.EndsAt},
		})
	})
	return out, err
}

func subjectExists(ctx context.Context, q *store.Queries, orgID, id uuid.UUID) (bool, error) {
	if m, err := q.GetMonitor(ctx, id); err == nil {
		return m.OrganizationID == orgID, nil
	} else if !database.IsNoRows(err) {
		return false, err
	}
	a, err := q.GetAsset(ctx, id)
	if database.IsNoRows(err) {
		return false, nil
	}
	return err == nil && a.OrganizationID == orgID, err
}

// ExpireSilence ends a silence now (monitor.manage).
func (s *Service) ExpireSilence(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		sl, err := q.GetSilence(ctx, id)
		if database.IsNoRows(err) {
			return errSilenceNotFound()
		}
		if err != nil {
			return err
		}
		if _, err := authz.Require(ctx, q, sl.OrganizationID, authz.MonitorManage); err != nil {
			return orgHidden(err, errSilenceNotFound)
		}
		if _, err := q.ExpireSilence(ctx, id); err != nil {
			if database.IsNoRows(err) {
				return errSilenceNotFound() // already over
			}
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &sl.OrganizationID, Action: "silence.expire", ResourceType: "silence", ResourceID: id.String(),
		})
	})
}
