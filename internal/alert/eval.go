package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/infra"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/notify"
	"github.com/opshub/opshub/internal/store"
)

// Evaluation timing.
const (
	EvaluateEvery = 30 * time.Second
	// silencedRecheck is when a silenced step is tried again.
	silencedRecheck = time.Minute
	escalationBatch = 100
)

// observation is a subject for which a rule's condition holds now.
type observation struct {
	subjectType string
	name        string
	labels      []string
	details     notify.Details
}

// subjects caches one organization's monitors, servers and certificates for an evaluation.
type subjects struct {
	monitors []store.EvalMonitorsRow
	servers  []store.EvalServersRow
	certs    []store.EvalCertificatesRow
}

func loadSubjects(ctx context.Context, q *store.Queries, orgID uuid.UUID) (*subjects, error) {
	var s subjects
	var err error
	if s.monitors, err = q.EvalMonitors(ctx, orgID); err != nil {
		return nil, err
	}
	if s.servers, err = q.EvalServers(ctx, orgID); err != nil {
		return nil, err
	}
	if s.certs, err = q.EvalCertificates(ctx, orgID); err != nil {
		return nil, err
	}
	return &s, nil
}

func matches(r store.AlertRule, id uuid.UUID, labels []string) bool {
	if r.TargetID != nil && *r.TargetID != id {
		return false
	}
	return r.Label == nil || slices.Contains(labels, *r.Label)
}

func f64(v float64) *float64 { return &v }

// observe returns the subjects for which r's condition holds at now.
func observe(r store.AlertRule, s *subjects, now time.Time) map[uuid.UUID]observation {
	out := map[uuid.UUID]observation{}
	switch r.Kind {
	case store.AlertRuleKindMonitorDown, store.AlertRuleKindMonitorLatency:
		for _, m := range s.monitors {
			if !m.Enabled || m.LastUp == nil || !matches(r, m.ID, m.Labels) {
				continue
			}
			o := observation{subjectType: "monitor", name: m.Name, labels: m.Labels}
			switch {
			case r.Kind == store.AlertRuleKindMonitorDown && !*m.LastUp:
				o.details.Error = m.LastError
				out[m.ID] = o
			case r.Kind == store.AlertRuleKindMonitorLatency && *m.LastUp && m.LastLatencyMs != nil && r.Threshold != nil &&
				float64(*m.LastLatencyMs) > *r.Threshold:
				o.details.Value, o.details.Threshold = f64(float64(*m.LastLatencyMs)), r.Threshold
				out[m.ID] = o
			}
		}
	case store.AlertRuleKindAssetMetric, store.AlertRuleKindAssetOffline:
		for _, a := range s.servers {
			if !a.HasAgent || a.LastHeartbeatAt == nil || !matches(r, a.ID, a.Tags) {
				continue // never reported: waiting for the agent, not offline
			}
			online := now.Sub(*a.LastHeartbeatAt) < infra.OfflineAfter
			o := observation{subjectType: "asset", name: a.Name, labels: a.Tags}
			if r.Kind == store.AlertRuleKindAssetOffline {
				if !online {
					o.details.Since = a.LastHeartbeatAt
					out[a.ID] = o
				}
				continue
			}
			if !online || r.Metric == nil || r.Threshold == nil {
				continue // stale numbers say nothing about now
			}
			var m infra.Metrics
			if json.Unmarshal(a.LastMetrics, &m) != nil {
				continue
			}
			v := map[string]*float32{"cpu": m.CPU, "mem": m.Mem, "disk": m.Disk}[*r.Metric]
			if v != nil && float64(*v) > *r.Threshold {
				o.details.Value, o.details.Threshold, o.details.Metric = f64(float64(*v)), r.Threshold, *r.Metric
				out[a.ID] = o
			}
		}
	case store.AlertRuleKindCertificate:
		for _, c := range s.certs {
			if !matches(r, c.ID, c.Tags) || r.Threshold == nil {
				continue
			}
			o := observation{subjectType: "asset", name: c.Name, labels: c.Tags}
			switch {
			case c.Error != "":
				o.details.Error = c.Error
				out[c.ID] = o
			case c.NotAfter != nil && c.NotAfter.Sub(now) < time.Duration(*r.Threshold*24)*time.Hour:
				days := int(c.NotAfter.Sub(now).Hours() / 24)
				o.details.Days, o.details.NotAfter = &days, c.NotAfter
				out[c.ID] = o
			}
		}
	}
	return out
}

// Evaluate checks every enabled rule and updates its alerts. It runs every 30 seconds.
func (s *Service) Evaluate(ctx context.Context) error {
	q := store.New(s.pool)
	rules, err := q.EnabledAlertRules(ctx)
	if err != nil {
		return err
	}
	cache := map[uuid.UUID]*subjects{}
	for _, r := range rules {
		subj, ok := cache[r.OrganizationID]
		if !ok {
			if subj, err = loadSubjects(ctx, q, r.OrganizationID); err != nil {
				return err
			}
			cache[r.OrganizationID] = subj
		}
		obs := observe(r, subj, s.now())
		if err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error { return s.apply(ctx, tx, q, r, obs) }); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
	}
	return nil
}

// apply moves a rule's alerts to match what was observed.
func (s *Service) apply(ctx context.Context, tx pgx.Tx, q *store.Queries, r store.AlertRule, obs map[uuid.UUID]observation) error {
	open, err := q.OpenAlertsForRule(ctx, &r.ID)
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]store.Alert{}
	for _, a := range open {
		byID[a.SubjectID] = a
	}
	now := s.now()
	for id, o := range obs {
		details, _ := json.Marshal(o.details)
		a, exists := byID[id]
		if !exists {
			a, err = q.InsertPendingAlert(ctx, store.InsertPendingAlertParams{
				OrganizationID: r.OrganizationID, RuleID: &r.ID, RuleName: r.Name, RuleKind: r.Kind, Severity: r.Severity,
				SubjectType: o.subjectType, SubjectID: id, SubjectName: o.name, SubjectLabels: o.labels, Details: details,
			})
			if database.IsNoRows(err) {
				continue // inserted concurrently
			}
			if err != nil {
				return err
			}
		} else if err := q.UpdateAlertObservation(ctx, store.UpdateAlertObservationParams{
			ID: a.ID, Details: details, SubjectName: o.name, SubjectLabels: o.labels, Severity: r.Severity, RuleName: r.Name,
		}); err != nil {
			return err
		}
		if a.Status == store.AlertStatusPending && now.Sub(a.PendingSince) >= time.Duration(r.ForSeconds)*time.Second {
			if _, err := q.FireAlert(ctx, a.ID); err != nil {
				return err
			}
			if err := q.InsertAlertEvent(ctx, store.InsertAlertEventParams{AlertID: a.ID, Kind: "fired", Detail: string(details)}); err != nil {
				return err
			}
		}
	}
	for id, a := range byID {
		if _, still := obs[id]; still {
			continue
		}
		if a.Status == store.AlertStatusPending {
			if err := q.DeletePendingAlert(ctx, a.ID); err != nil {
				return err
			}
			continue
		}
		if err := s.resolve(ctx, tx, q, a, ""); err != nil {
			return err
		}
	}
	return nil
}

// resolve closes a firing alert and tells every channel that received it (unless silenced).
func (s *Service) resolve(ctx context.Context, tx pgx.Tx, q *store.Queries, a store.Alert, why string) error {
	if _, err := q.ResolveAlert(ctx, a.ID); err != nil {
		if database.IsNoRows(err) {
			return nil
		}
		return err
	}
	if err := q.InsertAlertEvent(ctx, store.InsertAlertEventParams{AlertID: a.ID, Kind: "resolved", Detail: why}); err != nil {
		return err
	}
	silenced, err := s.silenced(ctx, q, a)
	if err != nil || silenced {
		return err
	}
	channels, err := q.NotifiedChannelIDs(ctx, a.ID)
	if err != nil {
		return err
	}
	for _, c := range channels {
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.NotifyArgs{AlertID: a.ID, ChannelID: c, Event: notify.EventResolved}, nil); err != nil {
			return err
		}
	}
	return nil
}

// silenced reports whether an active silence matches the alert.
func (s *Service) silenced(ctx context.Context, q *store.Queries, a store.Alert) (bool, error) {
	active, err := q.ActiveSilences(ctx, a.OrganizationID)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(active, func(sl store.Silence) bool { return silenceMatches(sl, a) }), nil
}

// silenceMatches: every matcher that is set must match.
func silenceMatches(sl store.Silence, a store.Alert) bool {
	switch {
	case sl.RuleID != nil && (a.RuleID == nil || *sl.RuleID != *a.RuleID):
		return false
	case sl.SubjectID != nil && *sl.SubjectID != a.SubjectID:
		return false
	case sl.Label != nil && !slices.Contains(a.SubjectLabels, *sl.Label):
		return false
	case sl.Severity != nil && *sl.Severity != a.Severity:
		return false
	}
	return true
}

// Escalate sends the escalation steps that are due. Silenced alerts are retried every minute,
// so their notifications go out if the silence ends while they still fire.
func (s *Service) Escalate(ctx context.Context) error {
	return s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		due, err := q.DueEscalations(ctx, escalationBatch)
		if err != nil {
			return err
		}
		now := s.now()
		for _, a := range due {
			var plan []Step
			if a.RuleID != nil {
				r, err := q.GetAlertRule(ctx, *a.RuleID)
				if err != nil && !database.IsNoRows(err) {
					return err
				}
				plan = steps(r.Escalation)
			}
			step := int(a.NextStep)
			if step >= len(plan) {
				if err := q.AdvanceAlertStep(ctx, store.AdvanceAlertStepParams{ID: a.ID, NextStep: a.NextStep}); err != nil {
					return err
				}
				continue
			}
			silenced, err := s.silenced(ctx, q, a)
			if err != nil {
				return err
			}
			if silenced {
				last, err := q.LastAlertEventKind(ctx, a.ID)
				if err != nil && !database.IsNoRows(err) {
					return err
				}
				if last != "silenced" {
					if err := q.InsertAlertEvent(ctx, store.InsertAlertEventParams{AlertID: a.ID, Kind: "silenced"}); err != nil {
						return err
					}
				}
				retry := now.Add(silencedRecheck)
				if err := q.AdvanceAlertStep(ctx, store.AdvanceAlertStepParams{ID: a.ID, NextStep: a.NextStep, NextStepAt: &retry}); err != nil {
					return err
				}
				continue
			}
			if step > 0 {
				if err := q.InsertAlertEvent(ctx, store.InsertAlertEventParams{AlertID: a.ID, Kind: "escalated", Detail: fmt.Sprintf("step %d", step+1)}); err != nil {
					return err
				}
			}
			for _, c := range plan[step].ChannelIDs {
				if _, err := s.jobs.InsertTx(ctx, tx, jobs.NotifyArgs{AlertID: a.ID, ChannelID: c, Event: notify.EventFiring, Step: step}, nil); err != nil {
					return err
				}
			}
			var next *time.Time
			if step+1 < len(plan) && a.StartedAt != nil {
				t := a.StartedAt.Add(time.Duration(plan[step+1].AfterMinutes) * time.Minute)
				next = &t
			}
			if err := q.AdvanceAlertStep(ctx, store.AdvanceAlertStepParams{ID: a.ID, NextStep: int32(step + 1), NextStepAt: next}); err != nil { // #nosec G115 -- at most MaxSteps
				return err
			}
		}
		return nil
	})
}
