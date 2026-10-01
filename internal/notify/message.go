package notify

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/store"
)

// Details is what an alert rule observed, stored on the alert (alerts.details) and used to
// write messages. Only the fields of the rule's kind are set.
type Details struct {
	Value     *float64   `json:"value,omitempty"`     // latency ms, metric percent
	Threshold *float64   `json:"threshold,omitempty"` // the rule's limit
	Metric    string     `json:"metric,omitempty"`    // cpu | mem | disk
	Error     string     `json:"error,omitempty"`     // monitor or certificate check error
	Since     *time.Time `json:"since,omitempty"`     // last heartbeat (agent offline)
	NotAfter  *time.Time `json:"not_after,omitempty"` // certificate expiry
	Days      *int       `json:"days,omitempty"`      // days until expiry
}

// Events.
const (
	EventFiring   = "firing"
	EventResolved = "resolved"
)

// Message is one rendered notification.
type Message struct {
	Event    string
	Locale   string
	Severity string // translated
	Title    string
	Summary  string
	Extra    string // "resolved after …" or "still firing …"
	URL      string
}

// Text is the plain-text form used by chat channels.
func (m Message) Text() string {
	icon := map[string]string{EventFiring: "🔴", EventResolved: "✅"}[m.Event]
	var b strings.Builder
	if icon != "" {
		b.WriteString(icon + " ")
	}
	if m.Severity != "" {
		fmt.Fprintf(&b, "[%s] ", m.Severity)
	}
	b.WriteString(m.Title)
	for _, line := range []string{m.Summary, m.Extra, m.URL} {
		if line != "" {
			b.WriteString("\n" + line)
		}
	}
	return b.String()
}

// alertURL links to the alert in the web app.
func (s *Service) alertURL(orgSlug string, alertID uuid.UUID) string {
	if s.cfg.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/o/" + orgSlug + "/monitoring/alerts/" + alertID.String()
}

func formatNumber(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// formatDuration is a compact, language-neutral duration ("2h 5m", "45s").
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// render writes an alert message in locale. step > 0 marks an escalation.
func (s *Service) render(locale string, a store.Alert, orgSlug, event string, step int) Message {
	t := func(id string, data map[string]any) string { return s.bundle.T(locale, id, data) }
	var d Details
	_ = json.Unmarshal(a.Details, &d)
	m := Message{Event: event, Locale: locale, Severity: t("notify.alert.severity."+string(a.Severity), nil), URL: s.alertURL(orgSlug, a.ID)}
	if event == EventResolved {
		m.Title = t("notify.alert.resolved.title", map[string]any{"Rule": a.RuleName})
	} else {
		m.Title = t("notify.alert.firing.title", map[string]any{"Rule": a.RuleName})
	}
	data := map[string]any{"Subject": a.SubjectName, "Error": d.Error}
	if d.Value != nil {
		data["Value"] = formatNumber(*d.Value)
	}
	if d.Threshold != nil {
		data["Threshold"] = formatNumber(*d.Threshold)
	}
	switch a.RuleKind {
	case store.AlertRuleKindMonitorDown:
		if d.Error == "" {
			m.Summary = t("notify.alert.summary.monitor_down_noerror", data)
		} else {
			m.Summary = t("notify.alert.summary.monitor_down", data)
		}
	case store.AlertRuleKindMonitorLatency:
		m.Summary = t("notify.alert.summary.monitor_latency", data)
	case store.AlertRuleKindAssetMetric:
		data["Metric"] = t("notify.alert.metric."+d.Metric, nil)
		m.Summary = t("notify.alert.summary.asset_metric", data)
	case store.AlertRuleKindAssetOffline:
		if d.Since != nil {
			data["Since"] = d.Since.UTC().Format("2006-01-02 15:04 UTC")
		}
		m.Summary = t("notify.alert.summary.asset_offline", data)
	case store.AlertRuleKindCertificate:
		if d.Error != "" {
			m.Summary = t("notify.alert.summary.certificate_failed", data)
		} else {
			if d.Days != nil {
				data["Days"] = *d.Days
			}
			if d.NotAfter != nil {
				data["Date"] = d.NotAfter.UTC().Format("2006-01-02")
			}
			if d.Days != nil && *d.Days < 0 {
				m.Summary = t("notify.alert.summary.certificate_expired", data)
			} else {
				m.Summary = t("notify.alert.summary.certificate_expiring", data)
			}
		}
	}
	switch {
	case event == EventResolved && a.StartedAt != nil && a.ResolvedAt != nil:
		m.Extra = t("notify.alert.resolved.after", map[string]any{"Duration": formatDuration(a.ResolvedAt.Sub(*a.StartedAt))})
	case event == EventFiring && step > 0:
		m.Extra = t("notify.alert.escalated", map[string]any{"Step": step + 1})
	}
	return m
}

// WebhookPayload is the JSON body posted to webhook channels.
type WebhookPayload struct {
	Event string          `json:"event"` // alert.firing | alert.resolved | test
	Text  string          `json:"text"`
	Alert *WebhookAlert   `json:"alert,omitempty"`
	Org   json.RawMessage `json:"organization"`
}

// WebhookAlert is the alert as sent to webhooks.
type WebhookAlert struct {
	ID          uuid.UUID  `json:"id"`
	Rule        string     `json:"rule"`
	RuleID      *uuid.UUID `json:"rule_id"`
	Kind        string     `json:"kind"`
	Severity    string     `json:"severity"`
	Status      string     `json:"status"`
	SubjectType string     `json:"subject_type"`
	SubjectID   uuid.UUID  `json:"subject_id"`
	Subject     string     `json:"subject"`
	Labels      []string   `json:"labels"`
	Details     Details    `json:"details"`
	StartedAt   *time.Time `json:"started_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
	URL         string     `json:"url"`
}
