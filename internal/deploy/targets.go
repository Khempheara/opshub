// Package deploy implements deploy targets and deployments (Module 6): SSH hosts, Docker
// hosts and Kubernetes clusters that OpsHub deploys container images to from a River
// worker, with health checks, automatic revert, release history and one-click rollback.
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/store"
)

// Config holds operator settings.
type Config struct {
	// OutboundAllowedCIDRs lets targets and health checks on private networks through the
	// SSRF guard (OPSHUB_OUTBOUND_ALLOWED_CIDRS).
	OutboundAllowedCIDRs []netip.Prefix
	// AllowLocalDocker permits Docker targets that use the API host's own socket.
	AllowLocalDocker bool
}

// Service implements deploy targets and deployments.
type Service struct {
	pool      *pgxpool.Pool
	keys      *crypto.KeyRing
	pipelines *pipeline.Service
	jobs      jobs.Inserter
	cfg       Config
	logger    *slog.Logger
	dialer    *net.Dialer
	health    healthChecker
	now       func() time.Time
}

func NewService(pool *pgxpool.Pool, keys *crypto.KeyRing, pipelines *pipeline.Service, inserter jobs.Inserter, cfg Config, logger *slog.Logger) *Service {
	opts := safehttp.Options{AllowedCIDRs: cfg.OutboundAllowedCIDRs, Timeout: 10 * time.Second}
	s := &Service{
		pool: pool, keys: keys, pipelines: pipelines, jobs: inserter, cfg: cfg, logger: logger,
		dialer: opts.Dialer(), health: healthChecker{client: safehttp.NewClient(opts), interval: 2 * time.Second},
		now: time.Now,
	}
	pipelines.SetDeployStarter(s.startFromJob)
	return s
}

func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx, q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, store.New(tx)) })
}

// Target is the API representation of a deploy target. Credentials are write-only: only the
// names of the fields that are set are returned.
type Target struct {
	ID          uuid.UUID              `json:"id"`
	Name        string                 `json:"name"`
	Kind        store.DeployTargetKind `json:"kind"`
	Description string                 `json:"description"`
	Config      json.RawMessage        `json:"config"`
	Credentials []string               `json:"credentials"`
	LastTestAt  *time.Time             `json:"last_test_at"`
	LastTestOK  *bool                  `json:"last_test_ok"`
	Version     int32                  `json:"version"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

func errTargetNotFound() *apperr.Error {
	return apperr.New(apperr.CodeTargetNotFound, http.StatusNotFound, "deploy target not found")
}

func (s *Service) toTarget(t store.DeployTarget) Target {
	out := Target{
		ID: t.ID, Name: t.Name, Kind: t.Kind, Description: t.Description, Config: t.Config, Credentials: []string{},
		LastTestAt: t.LastTestAt, LastTestOK: t.LastTestOk, Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	if raw, err := s.keys.Decrypt(t.CredentialsEnc, t.ID[:]); err == nil {
		var m map[string]string
		if json.Unmarshal(raw, &m) == nil {
			for k, v := range m {
				if v != "" {
					out.Credentials = append(out.Credentials, k)
				}
			}
			sort.Strings(out.Credentials)
		}
	}
	return out
}

// TargetInput is POST /orgs/{id}/deploy-targets.
type TargetInput struct {
	Name        string                 `json:"name" validate:"required"`
	Kind        store.DeployTargetKind `json:"kind" validate:"required,oneof=ssh docker kubernetes"`
	Description string                 `json:"description" validate:"max=500"`
	Config      json.RawMessage        `json:"config"`
	Credentials json.RawMessage        `json:"credentials"`
}

// UpdateTargetInput is PATCH /deploy-targets/{id}. The name and kind can't change (pipelines
// refer to targets by name); credentials are kept when omitted.
type UpdateTargetInput struct {
	Description string          `json:"description" validate:"max=500"`
	Config      json.RawMessage `json:"config"`
	Credentials json.RawMessage `json:"credentials"`
}

// normalize validates a kind's config and credentials. creds may be nil (keep the stored
// ones), in which case storedCreds is validated against the new config instead.
func (s *Service) normalize(kind store.DeployTargetKind, rawConfig, rawCreds, storedCreds json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	required := len(rawCreds) > 0 && string(rawCreds) != "null"
	creds := rawCreds
	if !required {
		creds = storedCreds
	}
	var config json.RawMessage
	var err error
	switch kind {
	case KindSSH:
		var c SSHConfig
		var cr SSHCredentials
		if err = strictDecode(rawConfig, &c, "config"); err != nil {
			return nil, nil, err
		}
		if err = strictDecode(creds, &cr, "credentials"); err != nil {
			return nil, nil, err
		}
		config, err = c.normalize()
		err = mergeValidation(err, cr.validate(true))
		creds, _ = json.Marshal(cr) // #nosec G117 -- serialized only to be encrypted (credentials_enc)
	case KindDocker:
		var c DockerConfig
		var cr DockerCredentials
		if err = strictDecode(rawConfig, &c, "config"); err != nil {
			return nil, nil, err
		}
		if err = strictDecode(creds, &cr, "credentials"); err != nil {
			return nil, nil, err
		}
		config, err = c.normalize(s.cfg.AllowLocalDocker)
		err = mergeValidation(err, cr.validate(c.Connection, true))
		creds, _ = json.Marshal(cr) // #nosec G117 -- serialized only to be encrypted (credentials_enc)
	case KindKubernetes:
		var c KubernetesConfig
		var cr KubernetesCredentials
		if err = strictDecode(rawConfig, &c, "config"); err != nil {
			return nil, nil, err
		}
		if err = strictDecode(creds, &cr, "credentials"); err != nil {
			return nil, nil, err
		}
		config, err = c.normalize()
		err = mergeValidation(err, cr.validate(true))
		creds, _ = json.Marshal(cr) // #nosec G117 -- serialized only to be encrypted (credentials_enc)
	default:
		return nil, nil, apperr.Validation([]apperr.FieldError{{Field: "kind", Rule: "oneof", Param: "ssh docker kubernetes"}})
	}
	if err != nil {
		return nil, nil, err
	}
	return config, creds, nil
}

// mergeValidation reports config and credential problems together, so a form shows every
// invalid field at once.
func mergeValidation(errs ...error) error {
	var fields []apperr.FieldError
	for _, err := range errs {
		if err == nil {
			continue
		}
		ae, ok := apperr.From(err)
		if !ok || ae.Code != apperr.CodeValidation {
			return err
		}
		if f, ok := ae.Details["fields"].([]apperr.FieldError); ok {
			fields = append(fields, f...)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return apperr.Validation(fields)
}

// ListTargets lists the organization's targets (target.view).
func (s *Service) ListTargets(ctx context.Context, orgID uuid.UUID) ([]Target, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.TargetView); err != nil {
		return nil, err
	}
	rows, err := q.ListDeployTargets(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(rows))
	for _, t := range rows {
		out = append(out, s.toTarget(t))
	}
	return out, nil
}

// CreateTarget adds a target (target.manage).
func (s *Service) CreateTarget(ctx context.Context, orgID uuid.UUID, in TargetInput) (Target, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !namePattern.MatchString(in.Name) {
		return Target{}, apperr.Validation([]apperr.FieldError{{Field: "name", Rule: "slug"}})
	}
	var out Target
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		m, err := authz.Require(ctx, q, orgID, authz.TargetManage)
		if err != nil {
			return err
		}
		config, creds, err := s.normalize(in.Kind, in.Config, in.Credentials, nil)
		if err != nil {
			return err
		}
		// Credentials are encrypted with the row's id as associated data, so the id is chosen here.
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		enc, err := s.keys.Encrypt(creds, id[:])
		if err != nil {
			return err
		}
		t, err := q.CreateDeployTargetWithID(ctx, store.CreateDeployTargetWithIDParams{
			ID: id, OrganizationID: orgID, Name: in.Name, Kind: in.Kind, Description: strings.TrimSpace(in.Description),
			Config: config, CredentialsEnc: enc, CreatedBy: &m.UserID,
		})
		if database.IsUniqueViolation(err, "deploy_targets_organization_id_name_key") {
			return apperr.New(apperr.CodeTargetNameTaken, http.StatusConflict, "a deploy target with this name exists")
		}
		if err != nil {
			return err
		}
		out = s.toTarget(t)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "deploy_target.create", ResourceType: "deploy_target", ResourceID: t.ID.String(),
			After: map[string]any{"name": t.Name, "kind": t.Kind, "config": json.RawMessage(t.Config)},
		})
	})
	return out, err
}

// loadTarget finds a target and checks the action in its organization; other tenants get
// DEPLOY_TARGET_NOT_FOUND.
func (s *Service) loadTarget(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.DeployTarget, error) {
	t, err := q.GetDeployTarget(ctx, id)
	if database.IsNoRows(err) {
		return t, errTargetNotFound()
	}
	if err != nil {
		return t, err
	}
	if _, err := authz.Require(ctx, q, t.OrganizationID, a); err != nil {
		if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
			return t, errTargetNotFound()
		}
		return t, err
	}
	return t, nil
}

// GetTarget returns one target (target.view).
func (s *Service) GetTarget(ctx context.Context, id uuid.UUID) (Target, error) {
	t, err := s.loadTarget(ctx, store.New(s.pool), id, authz.TargetView)
	if err != nil {
		return Target{}, err
	}
	return s.toTarget(t), nil
}

// UpdateTarget replaces the description and config, and the credentials when given
// (target.manage, If-Match).
func (s *Service) UpdateTarget(ctx context.Context, id uuid.UUID, version int32, in UpdateTargetInput) (Target, error) {
	var out Target
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		t, err := s.loadTarget(ctx, q, id, authz.TargetManage)
		if err != nil {
			return err
		}
		stored, err := s.keys.Decrypt(t.CredentialsEnc, t.ID[:])
		if err != nil {
			return err
		}
		config, creds, err := s.normalize(t.Kind, in.Config, in.Credentials, stored)
		if err != nil {
			return err
		}
		enc, err := s.keys.Encrypt(creds, t.ID[:])
		if err != nil {
			return err
		}
		u, err := q.UpdateDeployTarget(ctx, store.UpdateDeployTargetParams{
			ID: id, Version: version, Description: strings.TrimSpace(in.Description), Config: config, CredentialsEnc: enc,
		})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if err != nil {
			return err
		}
		out = s.toTarget(u)
		credsChanged := len(in.Credentials) > 0 && string(in.Credentials) != "null"
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &t.OrganizationID, Action: "deploy_target.update", ResourceType: "deploy_target", ResourceID: id.String(),
			Before:   map[string]any{"description": t.Description, "config": json.RawMessage(t.Config)},
			After:    map[string]any{"description": u.Description, "config": json.RawMessage(u.Config)},
			Metadata: map[string]any{"credentials_changed": credsChanged},
		})
	})
	return out, err
}

// DeleteTarget removes a target (target.manage). History keeps the target's name.
func (s *Service) DeleteTarget(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		t, err := s.loadTarget(ctx, q, id, authz.TargetManage)
		if err != nil {
			return err
		}
		active, err := q.CountActiveTargetDeployments(ctx, &id)
		if err != nil {
			return err
		}
		if active > 0 {
			return apperr.New(apperr.CodeTargetInUse, http.StatusConflict, "a deployment to this target is in progress")
		}
		if _, err := q.DeleteDeployTarget(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &t.OrganizationID, Action: "deploy_target.delete", ResourceType: "deploy_target", ResourceID: id.String(),
			Before: map[string]any{"name": t.Name, "kind": t.Kind},
		})
	})
}

// open builds the executor for a target.
func (s *Service) open(ctx context.Context, t store.DeployTarget) (executor, error) {
	raw, err := s.keys.Decrypt(t.CredentialsEnc, t.ID[:])
	if err != nil {
		return nil, err
	}
	switch t.Kind {
	case KindSSH:
		var c SSHConfig
		var cr SSHCredentials
		if err := json.Unmarshal(t.Config, &c); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &cr); err != nil {
			return nil, err
		}
		d, err := newSSHDialer(s.dialer, c.User, cr)
		if err != nil {
			return nil, err
		}
		return &sshExecutor{cfg: c, dialer: d, health: s.health}, nil
	case KindDocker:
		var c DockerConfig
		var cr DockerCredentials
		if err := json.Unmarshal(t.Config, &c); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &cr); err != nil {
			return nil, err
		}
		if c.Connection == DockerViaLocal && !s.cfg.AllowLocalDocker {
			return nil, fail(ReasonUnreachable, "local Docker targets are disabled on this server")
		}
		dc, sc, err := connectDocker(ctx, c, cr, s.dialer)
		if err != nil {
			return nil, err
		}
		return &dockerExecutor{cfg: c, docker: dc, ssh: sc, health: s.health}, nil
	case KindKubernetes:
		var c KubernetesConfig
		var cr KubernetesCredentials
		if err := json.Unmarshal(t.Config, &c); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &cr); err != nil {
			return nil, err
		}
		kc, err := parseKubeconfig(cr.Kubeconfig)
		if err != nil {
			return nil, err
		}
		k, err := newKubeClient(kc, s.dialer)
		if err != nil {
			return nil, err
		}
		return &kubeExecutor{cfg: c, kube: k, health: s.health}, nil
	}
	return nil, errors.New("unknown target kind")
}

// TestTarget checks connectivity and reports SSH host key fingerprints to trust
// (target.manage).
func (s *Service) TestTarget(ctx context.Context, id uuid.UUID) (TestResult, error) {
	q := store.New(s.pool)
	t, err := s.loadTarget(ctx, q, id, authz.TargetManage)
	if err != nil {
		return TestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res := s.test(ctx, t)
	if err := q.RecordTargetTest(ctx, store.RecordTargetTestParams{ID: id, Ok: &res.OK}); err != nil {
		return res, err
	}
	return res, nil
}

func (s *Service) test(ctx context.Context, t store.DeployTarget) TestResult {
	if t.Kind == KindDocker {
		// Docker over SSH: show the host key before connecting when it isn't trusted.
		var c DockerConfig
		_ = json.Unmarshal(t.Config, &c)
		if c.Connection == DockerViaSSH {
			if r, done := s.probeDockerHost(ctx, t, c); done {
				return r
			}
		}
	}
	x, err := s.open(ctx, t)
	if err != nil {
		return TestResult{Checks: []Check{{Name: t.Name, Detail: err.Error()}}}
	}
	defer func() { _ = x.Close() }()
	return x.Test(ctx)
}

// probeDockerHost reports the host key when it isn't pinned or changed (done = true).
func (s *Service) probeDockerHost(ctx context.Context, t store.DeployTarget, c DockerConfig) (TestResult, bool) {
	raw, err := s.keys.Decrypt(t.CredentialsEnc, t.ID[:])
	if err != nil {
		return TestResult{Checks: []Check{{Name: c.Host, Detail: err.Error()}}}, true
	}
	var cr DockerCredentials
	_ = json.Unmarshal(raw, &cr)
	d, err := newSSHDialer(s.dialer, c.User, SSHCredentials{PrivateKey: cr.PrivateKey, Passphrase: cr.Passphrase})
	if err != nil {
		return TestResult{Checks: []Check{{Name: c.Host, Detail: err.Error()}}}, true
	}
	fp, err := d.probe(ctx, c.Host)
	switch {
	case err != nil:
		return TestResult{Checks: []Check{{Name: c.Host, Detail: err.Error()}}}, true
	case c.HostKey == "":
		return TestResult{Checks: []Check{{Name: c.Host, Fingerprint: fp, Detail: "host key not trusted yet"}}}, true
	case c.HostKey != fp:
		return TestResult{Checks: []Check{{Name: c.Host, Fingerprint: fp, Detail: "host key changed since it was trusted"}}}, true
	}
	return TestResult{}, false
}
