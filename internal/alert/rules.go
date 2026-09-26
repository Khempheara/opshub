// Package alert implements alert rules, alerts, silences and escalation (Module 9). Rules
// watch uptime monitors and the infrastructure of Module 7 (server metrics, agents,
// certificates). A River job evaluates them every 30 seconds: a condition that holds for
// the rule's duration fires an alert, which notifies the channels of each escalation step
// until someone acknowledges it; it resolves by itself when the condition clears.
package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/monitor"
	"github.com/opshub/opshub/internal/store"
)

// Limits.
const (
	MaxSteps        = 5
	MaxStepChannels = 10
	MaxForSeconds   = 86400
)

// Service manages rules, alerts and silences and evaluates the rules.
type Service struct {
	pool   *pgxpool.Pool
	jobs   jobs.Inserter
	logger *slog.Logger
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, inserter jobs.Inserter, logger *slog.Logger) *Service {
	return &Service{pool: pool, jobs: inserter, logger: logger, now: time.Now}
}

func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx, q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, store.New(tx)) })
}

// Step is one escalation step: notify these channels after this many minutes of firing
// without an acknowledgement. The first step is at 0 minutes.
type Step struct {
	AfterMinutes int         `json:"after_minutes"`
	ChannelIDs   []uuid.UUID `json:"channel_ids"`
}

// Rule is the API representation.
type Rule struct {
	ID         uuid.UUID           `json:"id"`
	Name       string              `json:"name"`
	Kind       store.AlertRuleKind `json:"kind"`
	TargetID   *uuid.UUID          `json:"target_id"`
	Label      *string             `json:"label"`
	Threshold  *float64            `json:"threshold"`
	Metric     *string             `json:"metric"`
	ForSeconds int32               `json:"for_seconds"`
	Severity   store.AlertSeverity `json:"severity"`
	Escalation []Step              `json:"escalation"`
	Enabled    bool                `json:"enabled"`
	Firing     int32               `json:"firing"`
	Version    int32               `json:"version"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

func steps(raw []byte) []Step {
	out := []Step{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func toRule(r store.AlertRule, firing int32) Rule {
	return Rule{
		ID: r.ID, Name: r.Name, Kind: r.Kind, TargetID: r.TargetID, Label: r.Label, Threshold: r.Threshold, Metric: r.Metric,
		ForSeconds: r.ForSeconds, Severity: r.Severity, Escalation: steps(r.Escalation), Enabled: r.Enabled, Firing: firing,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func errRuleNotFound() *apperr.Error {
	return apperr.New(apperr.CodeRuleNotFound, http.StatusNotFound, "alert rule not found")
}

func errRuleNameTaken() *apperr.Error {
	return apperr.New(apperr.CodeRuleNameTaken, http.StatusConflict, "an alert rule with this name exists")
}

// RuleInput is POST /orgs/{id}/alert-rules and PATCH /alert-rules/{id} (kind is fixed).
type RuleInput struct {
	Name       string              `json:"name"`
	Kind       store.AlertRuleKind `json:"kind"`
	TargetID   *uuid.UUID          `json:"target_id"`
	Label      *string             `json:"label"`
	Threshold  *float64            `json:"threshold"`
	Metric     *string             `json:"metric"`
	ForSeconds int32               `json:"for_seconds"`
	Severity   store.AlertSeverity `json:"severity"`
	Escalation []Step              `json:"escalation"`
	Enabled    *bool               `json:"enabled"`
}

// thresholds per kind: the unit's range, required or not allowed.
var thresholds = map[store.AlertRuleKind][2]float64{
	store.AlertRuleKindMonitorLatency: {1, 60000}, // ms
	store.AlertRuleKindAssetMetric:    {1, 100},   // percent
	store.AlertRuleKindCertificate:    {1, 365},   // days
}

// normalize validates in for kind against the organization's monitors, assets and channels.
func (in *RuleInput) normalize(ctx context.Context, q *store.Queries, orgID uuid.UUID, kind store.AlertRuleKind) ([]apperr.FieldError, error) {
	var fields []apperr.FieldError
	add := func(field, rule, param string) {
		fields = append(fields, apperr.FieldError{Field: field, Rule: rule, Param: param})
	}
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 {
		add("name", "range", "1-100")
	}
	switch kind {
	case store.AlertRuleKindMonitorDown, store.AlertRuleKindMonitorLatency, store.AlertRuleKindAssetMetric,
		store.AlertRuleKindAssetOffline, store.AlertRuleKindCertificate:
	default:
		add("kind", "oneof", "monitor_down monitor_latency asset_metric asset_offline certificate")
		return fields, nil
	}
	if in.Severity == "" {
		in.Severity = store.AlertSeverityWarning
	}
	if !slices.Contains([]store.AlertSeverity{store.AlertSeverityInfo, store.AlertSeverityWarning, store.AlertSeverityCritical}, in.Severity) {
		add("severity", "oneof", "info warning critical")
	}
	if in.ForSeconds < 0 || in.ForSeconds > MaxForSeconds {
		add("for_seconds", "range", fmt.Sprintf("0-%d", MaxForSeconds))
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
	// Threshold and metric by kind.
	if r, ok := thresholds[kind]; ok {
		switch {
		case in.Threshold == nil:
			add("threshold", "required", "")
		case math.IsNaN(*in.Threshold) || *in.Threshold < r[0] || *in.Threshold > r[1]:
			add("threshold", "range", fmt.Sprintf("%g-%g", r[0], r[1]))
		}
	} else {
		in.Threshold = nil
	}
	if kind == store.AlertRuleKindAssetMetric {
		if in.Metric == nil || !slices.Contains([]string{"cpu", "mem", "disk"}, *in.Metric) {
			add("metric", "oneof", "cpu mem disk")
		}
	} else {
		in.Metric = nil
	}
	// The target must be a monitor or asset of the right kind in this organization.
	if in.TargetID != nil {
		ok, err := targetExists(ctx, q, orgID, kind, *in.TargetID)
		if err != nil {
			return nil, err
		}
		if !ok {
			add("target_id", "exists", "")
		}
	}
	// Escalation.
	if len(in.Escalation) > MaxSteps {
		add("escalation", "max", fmt.Sprint(MaxSteps))
	}
	if in.Escalation == nil {
		in.Escalation = []Step{}
	}
	var channels []uuid.UUID
	for i, st := range in.Escalation {
		p := fmt.Sprintf("escalation[%d]", i)
		switch {
		case i == 0 && st.AfterMinutes != 0:
			add(p+".after_minutes", "first_step_immediate", "")
		case i > 0 && (st.AfterMinutes <= in.Escalation[i-1].AfterMinutes || st.AfterMinutes > 1440):
			add(p+".after_minutes", "increasing", "1440")
		}
		if len(st.ChannelIDs) == 0 || len(st.ChannelIDs) > MaxStepChannels {
			add(p+".channel_ids", "range", fmt.Sprintf("1-%d", MaxStepChannels))
		}
		for _, c := range st.ChannelIDs {
			if !slices.Contains(channels, c) {
				channels = append(channels, c)
			}
		}
	}
	if len(channels) > 0 {
		found, err := q.OrgChannelIDs(ctx, store.OrgChannelIDsParams{OrganizationID: orgID, Ids: channels})
		if err != nil {
			return nil, err
		}
		for i, st := range in.Escalation {
			for j, c := range st.ChannelIDs {
				if !slices.Contains(found, c) {
					add(fmt.Sprintf("escalation[%d].channel_ids[%d]", i, j), "exists", "")
				}
			}
		}
	}
	if in.Enabled == nil {
		t := true
		in.Enabled = &t
	}
	return fields, nil
}

func targetExists(ctx context.Context, q *store.Queries, orgID uuid.UUID, kind store.AlertRuleKind, id uuid.UUID) (bool, error) {
	switch kind {
	case store.AlertRuleKindMonitorDown, store.AlertRuleKindMonitorLatency:
		m, err := q.GetMonitor(ctx, id)
		if database.IsNoRows(err) {
			return false, nil
		}
		return err == nil && m.OrganizationID == orgID, err
	default:
		a, err := q.GetAsset(ctx, id)
		if database.IsNoRows(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		want := store.AssetKindServer
		if kind == store.AlertRuleKindCertificate {
			want = store.AssetKindDomain
		}
		return a.OrganizationID == orgID && a.Kind == want, nil
	}
}

func ruleAudit(r store.AlertRule) map[string]any {
	return map[string]any{
		"name": r.Name, "kind": r.Kind, "target_id": r.TargetID, "label": r.Label, "threshold": r.Threshold, "metric": r.Metric,
		"for_seconds": r.ForSeconds, "severity": r.Severity, "escalation": json.RawMessage(r.Escalation), "enabled": r.Enabled,
	}
}

// ListRules returns an organization's rules with their firing counts (monitor.view).
func (s *Service) ListRules(ctx context.Context, orgID uuid.UUID) ([]Rule, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.MonitorView); err != nil {
		return nil, err
	}
	rows, err := q.ListAlertRules(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRule(store.AlertRule{
			ID: r.ID, OrganizationID: r.OrganizationID, Name: r.Name, Kind: r.Kind, TargetID: r.TargetID, Label: r.Label,
			Threshold: r.Threshold, Metric: r.Metric, ForSeconds: r.ForSeconds, Severity: r.Severity, Escalation: r.Escalation,
			Enabled: r.Enabled, Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}, r.Firing))
	}
	return out, nil
}

// orgHidden maps "organization not found" to the resource's own 404.
func orgHidden(err error, nf func() *apperr.Error) error {
	if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
		return nf()
	}
	return err
}

func (s *Service) loadRule(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.AlertRule, authz.Membership, error) {
	r, err := q.GetAlertRule(ctx, id)
	if database.IsNoRows(err) {
		return r, authz.Membership{}, errRuleNotFound()
	}
	if err != nil {
		return r, authz.Membership{}, err
	}
	m, err := authz.Require(ctx, q, r.OrganizationID, a)
	return r, m, orgHidden(err, errRuleNotFound)
}

// GetRule returns one rule (monitor.view).
func (s *Service) GetRule(ctx context.Context, id uuid.UUID) (Rule, error) {
	r, _, err := s.loadRule(ctx, store.New(s.pool), id, authz.MonitorView)
	if err != nil {
		return Rule{}, err
	}
	return toRule(r, 0), nil
}

// CreateRule adds a rule (monitor.manage).
func (s *Service) CreateRule(ctx context.Context, orgID uuid.UUID, in RuleInput) (Rule, error) {
	var out Rule
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		m, err := authz.Require(ctx, q, orgID, authz.MonitorManage)
		if err != nil {
			return err
		}
		fields, err := in.normalize(ctx, q, orgID, in.Kind)
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			return apperr.Validation(fields)
		}
		esc, _ := json.Marshal(in.Escalation)
		r, err := q.CreateAlertRule(ctx, store.CreateAlertRuleParams{
			OrganizationID: orgID, Name: in.Name, Kind: in.Kind, TargetID: in.TargetID, Label: in.Label, Threshold: in.Threshold,
			Metric: in.Metric, ForSeconds: in.ForSeconds, Severity: in.Severity, Escalation: esc, Enabled: *in.Enabled, CreatedBy: &m.UserID,
		})
		if database.IsUniqueViolation(err, "alert_rules_organization_id_name_key") {
			return errRuleNameTaken()
		}
		if err != nil {
			return err
		}
		out = toRule(r, 0)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "alert_rule.create", ResourceType: "alert_rule", ResourceID: r.ID.String(), After: ruleAudit(r),
		})
	})
	return out, err
}

// UpdateRule replaces a rule (monitor.manage, If-Match). Disabling it resolves its alerts.
func (s *Service) UpdateRule(ctx context.Context, id uuid.UUID, version int32, in RuleInput) (Rule, error) {
	var out Rule
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		before, _, err := s.loadRule(ctx, q, id, authz.MonitorManage)
		if err != nil {
			return err
		}
		fields, err := in.normalize(ctx, q, before.OrganizationID, before.Kind)
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			return apperr.Validation(fields)
		}
		esc, _ := json.Marshal(in.Escalation)
		r, err := q.UpdateAlertRule(ctx, store.UpdateAlertRuleParams{
			ID: id, Name: in.Name, TargetID: in.TargetID, Label: in.Label, Threshold: in.Threshold, Metric: in.Metric,
			ForSeconds: in.ForSeconds, Severity: in.Severity, Escalation: esc, Enabled: *in.Enabled, ExpectedVersion: version,
		})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if database.IsUniqueViolation(err, "alert_rules_organization_id_name_key") {
			return errRuleNameTaken()
		}
		if err != nil {
			return err
		}
		if !r.Enabled {
			if err := s.closeOpen(ctx, tx, q, r.ID, "rule disabled"); err != nil {
				return err
			}
		}
		out = toRule(r, 0)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &r.OrganizationID, Action: "alert_rule.update", ResourceType: "alert_rule", ResourceID: id.String(),
			Before: ruleAudit(before), After: ruleAudit(r),
		})
	})
	return out, err
}

// DeleteRule removes a rule and resolves its alerts, which stay as history (monitor.manage).
func (s *Service) DeleteRule(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		r, _, err := s.loadRule(ctx, q, id, authz.MonitorManage)
		if err != nil {
			return err
		}
		if err := s.closeOpen(ctx, tx, q, r.ID, "rule deleted"); err != nil {
			return err
		}
		if _, err := q.DeleteAlertRule(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &r.OrganizationID, Action: "alert_rule.delete", ResourceType: "alert_rule", ResourceID: id.String(), Before: ruleAudit(r),
		})
	})
}

// closeOpen drops a rule's pending alerts and resolves its firing ones (telling the channels
// that were notified).
func (s *Service) closeOpen(ctx context.Context, tx pgx.Tx, q *store.Queries, ruleID uuid.UUID, why string) error {
	open, err := q.OpenAlertsForRule(ctx, &ruleID)
	if err != nil {
		return err
	}
	for _, a := range open {
		if a.Status == store.AlertStatusPending {
			if err := q.DeletePendingAlert(ctx, a.ID); err != nil {
				return err
			}
			continue
		}
		if err := s.resolve(ctx, tx, q, a, why); err != nil {
			return err
		}
	}
	return nil
}
