// Package infra implements the infrastructure inventory (Module 7): assets (servers,
// clusters, databases, domains), metrics sent by infra agents running on servers, and TLS
// certificates that OpsHub probes on domains.
package infra

import (
	"context"
	"crypto/x509"
	"encoding/json"
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

// Agents report every HeartbeatInterval; missing three makes a server offline.
const (
	HeartbeatInterval = 30 * time.Second
	OfflineAfter      = 3 * HeartbeatInterval
	// ExpiringWithin marks certificates that need attention soon.
	ExpiringWithin = 30 * 24 * time.Hour
)

// Config holds operator settings.
type Config struct {
	// OutboundAllowedCIDRs lets certificate probes reach private networks.
	OutboundAllowedCIDRs []netip.Prefix
	// Roots verifies probed certificates (nil = the system roots).
	Roots *x509.CertPool
}

// Service implements the inventory.
type Service struct {
	pool   *pgxpool.Pool
	cfg    Config
	logger *slog.Logger
	dialer *net.Dialer
	now    func() time.Time
	// addr maps a probed host and port to the address dialed (tests point names at local
	// servers; the TLS server name stays the host).
	addr func(host string, port int32) string
}

func NewService(pool *pgxpool.Pool, cfg Config, logger *slog.Logger) *Service {
	return &Service{
		pool: pool, cfg: cfg, logger: logger,
		dialer: safehttp.Options{AllowedCIDRs: cfg.OutboundAllowedCIDRs}.Dialer(), now: time.Now,
		addr: func(host string, port int32) string { return net.JoinHostPort(host, strconv.Itoa(int(port))) },
	}
}

func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx, q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, store.New(tx)) })
}

// Metrics is one sample.
type Metrics struct {
	CPU  *float32 `json:"cpu_pct"`
	Mem  *float32 `json:"mem_pct"`
	Disk *float32 `json:"disk_pct"`
	Load *float32 `json:"load1"`
}

// Agent describes a server's infra agent.
type Agent struct {
	Installed       bool       `json:"installed"` // a token was issued
	TokenPrefix     string     `json:"token_prefix"`
	Version         string     `json:"version"`
	Hostname        string     `json:"hostname"`
	OS              string     `json:"os"`
	Arch            string     `json:"arch"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
}

// CertificateSummary is a domain's certificate state for lists.
type CertificateSummary struct {
	NotAfter  *time.Time `json:"not_after"`
	Error     string     `json:"error"`
	CheckedAt *time.Time `json:"checked_at"`
}

// Asset is the API representation of an asset.
type Asset struct {
	ID          uuid.UUID           `json:"id"`
	Kind        store.AssetKind     `json:"kind"`
	Name        string              `json:"name"`
	Address     string              `json:"address"`
	Description string              `json:"description"`
	Tags        []string            `json:"tags"`
	Metadata    map[string]string   `json:"metadata"`
	TLSPort     int32               `json:"tls_port"`
	Status      string              `json:"status"`
	Agent       *Agent              `json:"agent"`       // servers
	Metrics     *Metrics            `json:"metrics"`     // latest sample
	Certificate *CertificateSummary `json:"certificate"` // domains
	Version     int32               `json:"version"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// Statuses. Servers: online, offline, pending (token issued, no heartbeat yet), no_agent.
// Domains: valid, expiring, expired, error, unchecked. Other kinds: inventory.
const (
	StatusOnline    = "online"
	StatusOffline   = "offline"
	StatusPending   = "pending"
	StatusNoAgent   = "no_agent"
	StatusValid     = "valid"
	StatusExpiring  = "expiring"
	StatusExpired   = "expired"
	StatusError     = "error"
	StatusUnchecked = "unchecked"
	StatusInventory = "inventory"
)

func (s *Service) certStatus(notAfter *time.Time, errText string, checked bool) string {
	now := s.now()
	switch {
	case !checked:
		return StatusUnchecked
	case notAfter != nil && !notAfter.After(now):
		return StatusExpired
	case errText != "":
		return StatusError
	case notAfter != nil && notAfter.Sub(now) < ExpiringWithin:
		return StatusExpiring
	}
	return StatusValid
}

func (s *Service) toAsset(a store.InfraAsset, cert *CertificateSummary) Asset {
	out := Asset{
		ID: a.ID, Kind: a.Kind, Name: a.Name, Address: a.Address, Description: a.Description, Tags: a.Tags,
		Metadata: map[string]string{}, TLSPort: a.TlsPort, Version: a.Version, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	_ = json.Unmarshal(a.Metadata, &out.Metadata)
	switch a.Kind {
	case store.AssetKindServer:
		out.Agent = &Agent{
			Installed: a.AgentTokenHash != nil, TokenPrefix: a.AgentTokenPrefix, Version: a.AgentVersion,
			Hostname: a.AgentHostname, OS: a.AgentOs, Arch: a.AgentArch, LastHeartbeatAt: a.LastHeartbeatAt,
		}
		switch {
		case a.AgentTokenHash == nil:
			out.Status = StatusNoAgent
		case a.LastHeartbeatAt == nil:
			out.Status = StatusPending
		case s.now().Sub(*a.LastHeartbeatAt) < OfflineAfter:
			out.Status = StatusOnline
		default:
			out.Status = StatusOffline
		}
		if len(a.LastMetrics) > 0 {
			var m Metrics
			if json.Unmarshal(a.LastMetrics, &m) == nil {
				out.Metrics = &m
			}
		}
	case store.AssetKindDomain:
		if cert == nil {
			cert = &CertificateSummary{}
		}
		out.Certificate = cert
		out.Status = s.certStatus(cert.NotAfter, cert.Error, cert.CheckedAt != nil)
	default:
		out.Status = StatusInventory
	}
	return out
}

func errAssetNotFound() *apperr.Error {
	return apperr.New(apperr.CodeAssetNotFound, http.StatusNotFound, "asset not found")
}

// AssetInput is POST /orgs/{id}/assets (kind is fixed after creation).
type AssetInput struct {
	Kind        store.AssetKind   `json:"kind" validate:"omitempty,oneof=server cluster database domain"`
	Name        string            `json:"name"`
	Address     string            `json:"address"`
	Description string            `json:"description"`
	Tags        []string          `json:"tags"`
	Metadata    map[string]string `json:"metadata"`
	TLSPort     int32             `json:"tls_port"`
}

var (
	tagPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,62}$`)
	metaKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	hostPattern    = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	domainPattern  = regexp.MustCompile(`^(?i)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

// validHost accepts a DNS name or an IP address.
func validHost(h string) bool {
	if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return true
	}
	return len(h) <= 253 && hostPattern.MatchString(h)
}

// validAddress checks the address for a kind; empty is allowed except for domains.
func validAddress(kind store.AssetKind, a string) bool {
	if a == "" {
		return kind != store.AssetKindDomain
	}
	if len(a) > 255 || strings.ContainsAny(a, " \t\n") {
		return false
	}
	switch kind {
	case store.AssetKindDomain:
		return domainPattern.MatchString(a) && len(a) <= 253
	case store.AssetKindServer:
		return validHost(a)
	case store.AssetKindDatabase:
		h, p, err := net.SplitHostPort(a)
		if err != nil {
			return validHost(a)
		}
		n, err := strconv.Atoi(p)
		return err == nil && n > 0 && n < 65536 && validHost(h)
	case store.AssetKindCluster:
		if u, err := url.Parse(a); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			return true
		}
		return validHost(a)
	}
	return false
}

// normalize validates the input and returns cleaned tags and metadata JSON.
func (in *AssetInput) normalize(kind store.AssetKind) ([]string, []byte, error) {
	var fields []apperr.FieldError
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.TrimSpace(in.Address)
	in.Description = strings.TrimSpace(in.Description)
	if kind == store.AssetKindDomain {
		in.Address = strings.TrimSuffix(strings.ToLower(in.Address), ".")
	}
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		fields = append(fields, apperr.FieldError{Field: "name", Rule: "range", Param: "1-100"})
	}
	if !validAddress(kind, in.Address) {
		rule := "host"
		if kind == store.AssetKindDomain {
			rule = "domain"
		}
		if kind == store.AssetKindDomain && in.Address == "" {
			rule = "required"
		}
		fields = append(fields, apperr.FieldError{Field: "address", Rule: rule})
	}
	if len([]rune(in.Description)) > 500 {
		fields = append(fields, apperr.FieldError{Field: "description", Rule: "max", Param: "500"})
	}
	tags := []string{}
	for _, t := range in.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || slices.Contains(tags, t) {
			continue
		}
		if !tagPattern.MatchString(t) {
			fields = append(fields, apperr.FieldError{Field: "tags", Rule: "pattern"})
			break
		}
		tags = append(tags, t)
	}
	if len(tags) > 20 {
		fields = append(fields, apperr.FieldError{Field: "tags", Rule: "max", Param: "20"})
	}
	slices.Sort(tags)
	if in.Metadata == nil {
		in.Metadata = map[string]string{}
	}
	if len(in.Metadata) > 50 {
		fields = append(fields, apperr.FieldError{Field: "metadata", Rule: "max", Param: "50"})
	}
	for k, v := range in.Metadata {
		if !metaKeyPattern.MatchString(k) || len(v) > 500 {
			fields = append(fields, apperr.FieldError{Field: "metadata." + k, Rule: "pattern"})
		}
	}
	if in.TLSPort == 0 {
		in.TLSPort = 443
	}
	if in.TLSPort < 1 || in.TLSPort > 65535 {
		fields = append(fields, apperr.FieldError{Field: "tls_port", Rule: "range", Param: "1-65535"})
	}
	if len(fields) > 0 {
		return nil, nil, apperr.Validation(fields)
	}
	meta, _ := json.Marshal(in.Metadata)
	return tags, meta, nil
}

// AssetFilter narrows the list.
type AssetFilter struct {
	Kind, Tag, Search, Status string
}

// ListAssets lists the organization's assets (infra.view), at most 1000.
func (s *Service) ListAssets(ctx context.Context, orgID uuid.UUID, f AssetFilter) ([]Asset, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.InfraView); err != nil {
		return nil, err
	}
	params := store.ListAssetsParams{OrganizationID: orgID}
	if f.Kind != "" {
		if !slices.Contains([]string{"server", "cluster", "database", "domain"}, f.Kind) {
			return nil, apperr.Validation([]apperr.FieldError{{Field: "kind", Rule: "oneof", Param: "server cluster database domain"}})
		}
		params.Kind = &f.Kind
	}
	if f.Tag != "" {
		tag := strings.ToLower(f.Tag)
		params.Tag = &tag
	}
	if f.Search != "" {
		search := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Search)
		params.Search = &search
	}
	rows, err := q.ListAssets(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]Asset, 0, len(rows))
	for _, r := range rows {
		a := s.toAsset(store.InfraAsset{
			ID: r.ID, OrganizationID: r.OrganizationID, Kind: r.Kind, Name: r.Name, Address: r.Address, Description: r.Description,
			Tags: r.Tags, Metadata: r.Metadata, TlsPort: r.TlsPort, AgentTokenHash: r.AgentTokenHash, AgentTokenPrefix: r.AgentTokenPrefix,
			AgentVersion: r.AgentVersion, AgentHostname: r.AgentHostname, AgentOs: r.AgentOs, AgentArch: r.AgentArch,
			LastHeartbeatAt: r.LastHeartbeatAt, LastMetrics: r.LastMetrics, CreatedBy: r.CreatedBy, Version: r.Version,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}, &CertificateSummary{NotAfter: r.CertNotAfter, Error: r.CertError, CheckedAt: r.CertCheckedAt})
		if f.Status == "" || a.Status == f.Status {
			out = append(out, a)
		}
	}
	return out, nil
}

// loadAsset finds an asset and checks the action in its organization; other tenants get
// ASSET_NOT_FOUND.
func (s *Service) loadAsset(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.InfraAsset, error) {
	asset, err := q.GetAsset(ctx, id)
	if database.IsNoRows(err) {
		return asset, errAssetNotFound()
	}
	if err != nil {
		return asset, err
	}
	if _, err := authz.Require(ctx, q, asset.OrganizationID, a); err != nil {
		if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
			return asset, errAssetNotFound()
		}
		return asset, err
	}
	return asset, nil
}

func (s *Service) withCert(ctx context.Context, q *store.Queries, a store.InfraAsset) (Asset, error) {
	if a.Kind != store.AssetKindDomain {
		return s.toAsset(a, nil), nil
	}
	c, err := q.GetCertificate(ctx, a.ID)
	if database.IsNoRows(err) {
		return s.toAsset(a, nil), nil
	}
	if err != nil {
		return Asset{}, err
	}
	return s.toAsset(a, &CertificateSummary{NotAfter: c.NotAfter, Error: c.Error, CheckedAt: &c.LastCheckedAt}), nil
}

// GetAsset returns one asset (infra.view).
func (s *Service) GetAsset(ctx context.Context, id uuid.UUID) (Asset, error) {
	q := store.New(s.pool)
	a, err := s.loadAsset(ctx, q, id, authz.InfraView)
	if err != nil {
		return Asset{}, err
	}
	return s.withCert(ctx, q, a)
}

func assetAudit(a store.InfraAsset) map[string]any {
	return map[string]any{"kind": a.Kind, "name": a.Name, "address": a.Address, "tags": a.Tags, "metadata": json.RawMessage(a.Metadata)}
}

// CreateAsset adds an asset (infra.manage).
func (s *Service) CreateAsset(ctx context.Context, orgID uuid.UUID, in AssetInput) (Asset, error) {
	if in.Kind == "" {
		return Asset{}, apperr.Validation([]apperr.FieldError{{Field: "kind", Rule: "required"}})
	}
	tags, meta, err := in.normalize(in.Kind)
	if err != nil {
		return Asset{}, err
	}
	var out Asset
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		m, err := authz.Require(ctx, q, orgID, authz.InfraManage)
		if err != nil {
			return err
		}
		a, err := q.CreateAsset(ctx, store.CreateAssetParams{
			OrganizationID: orgID, Kind: in.Kind, Name: in.Name, Address: in.Address, Description: in.Description,
			Tags: tags, Metadata: meta, TlsPort: in.TLSPort, CreatedBy: &m.UserID,
		})
		if database.IsUniqueViolation(err, "infra_assets_organization_id_name_key") {
			return apperr.New(apperr.CodeAssetNameTaken, http.StatusConflict, "an asset with this name exists")
		}
		if err != nil {
			return err
		}
		out = s.toAsset(a, nil)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "asset.create", ResourceType: "asset", ResourceID: a.ID.String(), After: assetAudit(a),
		})
	})
	return out, err
}

// UpdateAsset replaces an asset's editable fields (infra.manage, If-Match). A domain whose
// address or port changes is checked again.
func (s *Service) UpdateAsset(ctx context.Context, id uuid.UUID, version int32, in AssetInput) (Asset, error) {
	var out Asset
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		before, err := s.loadAsset(ctx, q, id, authz.InfraManage)
		if err != nil {
			return err
		}
		tags, meta, err := in.normalize(before.Kind)
		if err != nil {
			return err
		}
		a, err := q.UpdateAsset(ctx, store.UpdateAssetParams{
			ID: id, Version: version, Name: in.Name, Address: in.Address, Description: in.Description, Tags: tags,
			Metadata: meta, TlsPort: in.TLSPort,
		})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if database.IsUniqueViolation(err, "infra_assets_organization_id_name_key") {
			return apperr.New(apperr.CodeAssetNameTaken, http.StatusConflict, "an asset with this name exists")
		}
		if err != nil {
			return err
		}
		if a.Kind == store.AssetKindDomain && (a.Address != before.Address || a.TlsPort != before.TlsPort) {
			if err := q.DeleteCertificate(ctx, a.ID); err != nil {
				return err
			}
		}
		if out, err = s.withCert(ctx, q, a); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.OrganizationID, Action: "asset.update", ResourceType: "asset", ResourceID: id.String(),
			Before: assetAudit(before), After: assetAudit(a),
		})
	})
	return out, err
}

// DeleteAsset removes an asset with its metrics and certificate (infra.manage).
func (s *Service) DeleteAsset(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		a, err := s.loadAsset(ctx, q, id, authz.InfraManage)
		if err != nil {
			return err
		}
		if _, err := q.DeleteAsset(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.OrganizationID, Action: "asset.delete", ResourceType: "asset", ResourceID: id.String(), Before: assetAudit(a),
		})
	})
}
