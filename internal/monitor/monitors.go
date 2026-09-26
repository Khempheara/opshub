// Package monitor implements uptime monitors (Module 9): HTTP, TCP and SSL checks run by
// OpsHub through the SSRF guard on a schedule, with results kept in monthly partitions and
// hourly rollups for uptime and latency charts.
package monitor

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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
	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/store"
)

// Limits and defaults.
const (
	DefaultInterval   = 60
	DefaultTimeoutMs  = 10000
	DefaultExpiryDays = 14
	MaxLabels         = 20
	MaxKeyword        = 200
)

// Config holds operator settings.
type Config struct {
	// OutboundAllowedCIDRs lets checks reach private networks through the SSRF guard.
	OutboundAllowedCIDRs []netip.Prefix
	// Roots verifies SSL monitors (nil = system roots; tests use their own CA).
	Roots *x509.CertPool
}

// Service manages monitors and runs their checks.
type Service struct {
	pool   *pgxpool.Pool
	cfg    Config
	logger *slog.Logger
	dialer *net.Dialer
	client *http.Client
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, cfg Config, logger *slog.Logger) *Service {
	// Each check bounds itself with the monitor's timeout (at most 30 s).
	opts := safehttp.Options{AllowedCIDRs: cfg.OutboundAllowedCIDRs, Timeout: 31 * time.Second, ResponseHeaderTimeout: 31 * time.Second}
	return &Service{pool: pool, cfg: cfg, logger: logger, dialer: opts.Dialer(), client: safehttp.NewClient(opts), now: time.Now}
}

func (s *Service) inTx(ctx context.Context, fn func(q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(store.New(tx)) })
}

// Settings is a monitor's kind-specific configuration.
type Settings struct {
	Method         string `json:"method,omitempty"`          // http: GET | HEAD
	ExpectedStatus []int  `json:"expected_status,omitempty"` // http: empty = 200–399
	Keyword        string `json:"keyword,omitempty"`         // http: must appear in the body
	ExpiryDays     int    `json:"expiry_days,omitempty"`     // ssl: down when expiring sooner
}

// Statuses.
const (
	StatusUp      = "up"
	StatusDown    = "down"
	StatusPending = "pending" // not checked yet
	StatusPaused  = "paused"
)

// Monitor is the API representation.
type Monitor struct {
	ID              uuid.UUID         `json:"id"`
	Name            string            `json:"name"`
	Kind            store.MonitorKind `json:"kind"`
	Target          string            `json:"target"`
	IntervalSeconds int32             `json:"interval_seconds"`
	TimeoutMs       int32             `json:"timeout_ms"`
	Settings        Settings          `json:"settings"`
	Labels          []string          `json:"labels"`
	Enabled         bool              `json:"enabled"`
	Status          string            `json:"status"`
	LastCheckedAt   *time.Time        `json:"last_checked_at"`
	LastLatencyMs   *int32            `json:"last_latency_ms"`
	LastError       string            `json:"last_error"`
	DownSince       *time.Time        `json:"down_since"`
	// Uptime24h is the share of successful checks in the last 24 hours (null: none yet).
	Uptime24h *float64  `json:"uptime_24h"`
	Version   int32     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func statusOf(m store.Monitor) string {
	switch {
	case !m.Enabled:
		return StatusPaused
	case m.LastUp == nil:
		return StatusPending
	case *m.LastUp:
		return StatusUp
	}
	return StatusDown
}

func toMonitor(m store.Monitor) Monitor {
	out := Monitor{
		ID: m.ID, Name: m.Name, Kind: m.Kind, Target: m.Target, IntervalSeconds: m.IntervalSeconds, TimeoutMs: m.TimeoutMs,
		Labels: m.Labels, Enabled: m.Enabled, Status: statusOf(m), LastCheckedAt: m.LastCheckedAt, LastLatencyMs: m.LastLatencyMs,
		LastError: m.LastError, DownSince: m.DownSince, Version: m.Version, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	_ = json.Unmarshal(m.Config, &out.Settings)
	if out.Labels == nil {
		out.Labels = []string{}
	}
	return out
}

func errNotFound() *apperr.Error {
	return apperr.New(apperr.CodeMonitorNotFound, http.StatusNotFound, "monitor not found")
}

func errNameTaken() *apperr.Error {
	return apperr.New(apperr.CodeMonitorNameTaken, http.StatusConflict, "a monitor with this name exists")
}

// MonitorInput is POST /orgs/{id}/monitors and PATCH /monitors/{id} (kind is fixed).
type MonitorInput struct {
	Name            string            `json:"name"`
	Kind            store.MonitorKind `json:"kind"`
	Target          string            `json:"target"`
	IntervalSeconds int32             `json:"interval_seconds"`
	TimeoutMs       int32             `json:"timeout_ms"`
	Settings        Settings          `json:"settings"`
	Labels          []string          `json:"labels"`
	Enabled         *bool             `json:"enabled"`
}

// LabelPattern matches monitor labels (and asset tags).
var LabelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,62}$`)

var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func validHost(h string) bool {
	if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return true
	}
	if len(h) > 253 || h == "" {
		return false
	}
	for label := range strings.SplitSeq(strings.TrimSuffix(h, "."), ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// splitHostPort accepts host:port, or host alone when def > 0.
func splitHostPort(v string, def int) (string, int, bool) {
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		if def == 0 {
			return "", 0, false
		}
		host, port = v, strconv.Itoa(def)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 || !validHost(host) {
		return "", 0, false
	}
	return host, p, true
}

// normalize validates in for kind and fills defaults.
func (in *MonitorInput) normalize(kind store.MonitorKind) []apperr.FieldError {
	var fields []apperr.FieldError
	add := func(field, rule, param string) {
		fields = append(fields, apperr.FieldError{Field: field, Rule: rule, Param: param})
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Target = strings.TrimSpace(in.Target)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 {
		add("name", "range", "1-100")
	}
	if in.IntervalSeconds == 0 {
		in.IntervalSeconds = DefaultInterval
	}
	if in.TimeoutMs == 0 {
		in.TimeoutMs = DefaultTimeoutMs
	}
	if in.IntervalSeconds < 30 || in.IntervalSeconds > 3600 {
		add("interval_seconds", "range", "30-3600")
	}
	if in.TimeoutMs < 1000 || in.TimeoutMs > 30000 {
		add("timeout_ms", "range", "1000-30000")
	} else if int64(in.TimeoutMs) >= int64(in.IntervalSeconds)*1000 {
		add("timeout_ms", "lt_interval", "")
	}
	set := in.Settings
	in.Settings = Settings{}
	switch kind {
	case store.MonitorKindHttp:
		u, err := url.Parse(in.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(in.Target) > 2048 {
			add("target", "url", "")
		}
		in.Settings.Method = strings.ToUpper(strings.TrimSpace(set.Method))
		if in.Settings.Method == "" {
			in.Settings.Method = http.MethodGet
		}
		if in.Settings.Method != http.MethodGet && in.Settings.Method != http.MethodHead {
			add("settings.method", "oneof", "GET HEAD")
		}
		if len(set.ExpectedStatus) > 20 {
			add("settings.expected_status", "max", "20")
		}
		for i, c := range set.ExpectedStatus {
			if c < 100 || c > 599 {
				add(fmt.Sprintf("settings.expected_status[%d]", i), "range", "100-599")
			} else if !slices.Contains(in.Settings.ExpectedStatus, c) {
				in.Settings.ExpectedStatus = append(in.Settings.ExpectedStatus, c)
			}
		}
		slices.Sort(in.Settings.ExpectedStatus)
		in.Settings.Keyword = set.Keyword
		if utf8.RuneCountInString(set.Keyword) > MaxKeyword {
			add("settings.keyword", "max", strconv.Itoa(MaxKeyword))
		}
		if set.Keyword != "" && in.Settings.Method == http.MethodHead {
			add("settings.keyword", "keyword_needs_get", "")
		}
	case store.MonitorKindTcp:
		if _, _, ok := splitHostPort(in.Target, 0); !ok {
			add("target", "host_port", "")
		}
	case store.MonitorKindSsl:
		host, port, ok := splitHostPort(in.Target, 443)
		if !ok {
			add("target", "host", "")
		} else {
			in.Target = net.JoinHostPort(host, strconv.Itoa(port))
		}
		in.Settings.ExpiryDays = set.ExpiryDays
		if in.Settings.ExpiryDays == 0 {
			in.Settings.ExpiryDays = DefaultExpiryDays
		}
		if in.Settings.ExpiryDays < 1 || in.Settings.ExpiryDays > 90 {
			add("settings.expiry_days", "range", "1-90")
		}
	default:
		add("kind", "oneof", "http tcp ssl")
	}
	labels := []string{}
	for i, l := range in.Labels {
		l = strings.ToLower(strings.TrimSpace(l))
		switch {
		case !LabelPattern.MatchString(l):
			add(fmt.Sprintf("labels[%d]", i), "pattern", "")
		case !slices.Contains(labels, l):
			labels = append(labels, l)
		}
	}
	if len(labels) > MaxLabels {
		add("labels", "max", strconv.Itoa(MaxLabels))
	}
	in.Labels = labels
	if in.Enabled == nil {
		t := true
		in.Enabled = &t
	}
	return fields
}

func monitorAudit(m store.Monitor) map[string]any {
	return map[string]any{
		"name": m.Name, "kind": m.Kind, "target": m.Target, "interval_seconds": m.IntervalSeconds,
		"timeout_ms": m.TimeoutMs, "settings": json.RawMessage(m.Config), "labels": m.Labels, "enabled": m.Enabled,
	}
}

// ListMonitors returns an organization's monitors (monitor.view), optionally filtered by
// label, name or target, and by status.
func (s *Service) ListMonitors(ctx context.Context, orgID uuid.UUID, label, search, status string) ([]Monitor, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.MonitorView); err != nil {
		return nil, err
	}
	p := store.ListMonitorsParams{OrganizationID: orgID}
	if label != "" {
		p.Label = &label
	}
	if search != "" {
		p.Search = &search
	}
	rows, err := q.ListMonitors(ctx, p)
	if err != nil {
		return nil, err
	}
	uptime, err := q.MonitorUptime24h(ctx, orgID)
	if err != nil {
		return nil, err
	}
	up := map[uuid.UUID]float64{}
	for _, u := range uptime {
		if u.Checks > 0 {
			up[u.MonitorID] = float64(u.UpChecks) / float64(u.Checks) * 100
		}
	}
	out := make([]Monitor, 0, len(rows))
	for _, r := range rows {
		m := toMonitor(r)
		if status != "" && m.Status != status {
			continue
		}
		if v, ok := up[r.ID]; ok {
			m.Uptime24h = &v
		}
		out = append(out, m)
	}
	return out, nil
}

// load fetches a monitor and checks the action; other organizations' monitors are hidden.
func (s *Service) load(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.Monitor, authz.Membership, error) {
	m, err := q.GetMonitor(ctx, id)
	if database.IsNoRows(err) {
		return m, authz.Membership{}, errNotFound()
	}
	if err != nil {
		return m, authz.Membership{}, err
	}
	mem, err := authz.Require(ctx, q, m.OrganizationID, a)
	if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
		return m, mem, errNotFound()
	}
	return m, mem, err
}

// GetMonitor returns one monitor (monitor.view).
func (s *Service) GetMonitor(ctx context.Context, id uuid.UUID) (Monitor, error) {
	m, _, err := s.load(ctx, store.New(s.pool), id, authz.MonitorView)
	if err != nil {
		return Monitor{}, err
	}
	return toMonitor(m), nil
}

// CreateMonitor adds a monitor; its first check runs right away (monitor.manage).
func (s *Service) CreateMonitor(ctx context.Context, orgID uuid.UUID, in MonitorInput) (Monitor, error) {
	var out Monitor
	err := s.inTx(ctx, func(q *store.Queries) error {
		mem, err := authz.Require(ctx, q, orgID, authz.MonitorManage)
		if err != nil {
			return err
		}
		if fields := in.normalize(in.Kind); len(fields) > 0 {
			return apperr.Validation(fields)
		}
		cfg, _ := json.Marshal(in.Settings)
		m, err := q.CreateMonitor(ctx, store.CreateMonitorParams{
			OrganizationID: orgID, Name: in.Name, Kind: in.Kind, Target: in.Target, IntervalSeconds: in.IntervalSeconds,
			TimeoutMs: in.TimeoutMs, Config: cfg, Labels: in.Labels, Enabled: *in.Enabled, CreatedBy: &mem.UserID,
		})
		if database.IsUniqueViolation(err, "monitors_organization_id_name_key") {
			return errNameTaken()
		}
		if err != nil {
			return err
		}
		out = toMonitor(m)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "monitor.create", ResourceType: "monitor", ResourceID: m.ID.String(), After: monitorAudit(m),
		})
	})
	return out, err
}

// UpdateMonitor replaces a monitor's settings; enabled=false pauses it (monitor.manage,
// If-Match).
func (s *Service) UpdateMonitor(ctx context.Context, id uuid.UUID, version int32, in MonitorInput) (Monitor, error) {
	var out Monitor
	err := s.inTx(ctx, func(q *store.Queries) error {
		before, _, err := s.load(ctx, q, id, authz.MonitorManage)
		if err != nil {
			return err
		}
		if fields := in.normalize(before.Kind); len(fields) > 0 {
			return apperr.Validation(fields)
		}
		cfg, _ := json.Marshal(in.Settings)
		m, err := q.UpdateMonitor(ctx, store.UpdateMonitorParams{
			ID: id, Name: in.Name, Target: in.Target, IntervalSeconds: in.IntervalSeconds, TimeoutMs: in.TimeoutMs,
			Config: cfg, Labels: in.Labels, Enabled: *in.Enabled, ExpectedVersion: version,
		})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if database.IsUniqueViolation(err, "monitors_organization_id_name_key") {
			return errNameTaken()
		}
		if err != nil {
			return err
		}
		out = toMonitor(m)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &m.OrganizationID, Action: "monitor.update", ResourceType: "monitor", ResourceID: id.String(),
			Before: monitorAudit(before), After: monitorAudit(m),
		})
	})
	return out, err
}

// DeleteMonitor removes a monitor with its results (monitor.manage).
func (s *Service) DeleteMonitor(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		m, _, err := s.load(ctx, q, id, authz.MonitorManage)
		if err != nil {
			return err
		}
		if _, err := q.DeleteMonitor(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &m.OrganizationID, Action: "monitor.delete", ResourceType: "monitor", ResourceID: id.String(), Before: monitorAudit(m),
		})
	})
}
