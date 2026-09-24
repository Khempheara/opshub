package project

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// Limits on environments and their variables.
const (
	MaxEnvironments    = 20
	MaxVariables       = 100
	MaxVariableValue   = 4096
	MaxAllowedBranches = 20
	MaxApprovals       = 10
)

var (
	envNamePattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,38}[a-z0-9])?$`)
	varNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	branchGlob      = regexp.MustCompile(`^[A-Za-z0-9._/*-]{1,255}$`)
	environmentKind = []string{"development", "staging", "production"}
)

// Protection is an environment's protection rule. Deployments (Module 6) and approval gates
// (Module 4) enforce it.
type Protection struct {
	RequiredApprovals int32        `json:"required_approvals"`
	AllowedBranches   []string     `json:"allowed_branches"` // glob patterns; empty = any branch
	AllowedRoles      []authz.Role `json:"allowed_roles"`    // who may deploy / approve
}

// Environment is the API representation of an environment. Variables are non-secret
// configuration; secrets are managed separately (Module 8).
type Environment struct {
	ID         uuid.UUID         `json:"id"`
	ProjectID  uuid.UUID         `json:"project_id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Variables  map[string]string `json:"variables"`
	Protection *Protection       `json:"protection"`
	Version    int32             `json:"version"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// EnvironmentInput creates (name required) or updates (name ignored) an environment.
// A nil Protection means "not protected".
type EnvironmentInput struct {
	Name       string            `json:"name" validate:"omitempty,max=40"`
	Kind       string            `json:"kind" validate:"required"`
	Variables  map[string]string `json:"variables"`
	Protection *Protection       `json:"protection"`
}

func errEnvironmentNotFound() *apperr.Error {
	return apperr.New(apperr.CodeEnvironmentNotFound, http.StatusNotFound, "environment not found")
}

func toEnvironment(e store.Environment, protected bool, approvals int32, branches, roles []string) (Environment, error) {
	out := Environment{
		ID: e.ID, ProjectID: e.ProjectID, Name: e.Name, Kind: string(e.Kind), Variables: map[string]string{},
		Version: e.Version, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
	if err := json.Unmarshal(e.Variables, &out.Variables); err != nil {
		return out, err
	}
	if protected {
		p := &Protection{RequiredApprovals: approvals, AllowedBranches: branches, AllowedRoles: make([]authz.Role, 0, len(roles))}
		if p.AllowedBranches == nil {
			p.AllowedBranches = []string{}
		}
		for _, r := range roles {
			p.AllowedRoles = append(p.AllowedRoles, authz.Role(r))
		}
		out.Protection = p
	}
	return out, nil
}

// validate checks an environment input and returns normalized variables and roles.
func (in EnvironmentInput) validate(create bool) ([]byte, []string, error) {
	var fields []apperr.FieldError
	if create && !envNamePattern.MatchString(in.Name) {
		fields = append(fields, apperr.FieldError{Field: "name", Rule: "slug"})
	}
	if !slices.Contains(environmentKind, in.Kind) {
		fields = append(fields, apperr.FieldError{Field: "kind", Rule: "oneof", Param: "development staging production"})
	}
	if len(in.Variables) > MaxVariables {
		fields = append(fields, apperr.FieldError{Field: "variables", Rule: "max", Param: strconv.Itoa(MaxVariables)})
	}
	for k, v := range in.Variables {
		if !varNamePattern.MatchString(k) {
			fields = append(fields, apperr.FieldError{Field: "variables." + k, Rule: "variable_name"})
		}
		if len(v) > MaxVariableValue {
			fields = append(fields, apperr.FieldError{Field: "variables." + k, Rule: "max", Param: strconv.Itoa(MaxVariableValue)})
		}
	}
	var roles []string
	if p := in.Protection; p != nil {
		if p.RequiredApprovals < 0 || p.RequiredApprovals > MaxApprovals {
			fields = append(fields, apperr.FieldError{Field: "protection.required_approvals", Rule: "range", Param: "0-10"})
		}
		if len(p.AllowedBranches) > MaxAllowedBranches {
			fields = append(fields, apperr.FieldError{Field: "protection.allowed_branches", Rule: "max", Param: strconv.Itoa(MaxAllowedBranches)})
		}
		for _, b := range p.AllowedBranches {
			if !branchGlob.MatchString(b) {
				fields = append(fields, apperr.FieldError{Field: "protection.allowed_branches", Rule: "branch"})
				break
			}
		}
		if len(p.AllowedRoles) == 0 {
			fields = append(fields, apperr.FieldError{Field: "protection.allowed_roles", Rule: "required"})
		}
		for _, r := range p.AllowedRoles {
			if !authz.ValidRole(r) || r == authz.Viewer {
				fields = append(fields, apperr.FieldError{Field: "protection.allowed_roles", Rule: "oneof", Param: "owner admin developer"})
				break
			}
			if !slices.Contains(roles, string(r)) {
				roles = append(roles, string(r))
			}
		}
	}
	if len(fields) > 0 {
		return nil, nil, apperr.Validation(fields)
	}
	vars := in.Variables
	if vars == nil {
		vars = map[string]string{}
	}
	b, err := json.Marshal(vars)
	return b, roles, err
}

func (s *Service) saveProtection(ctx context.Context, q *store.Queries, envID uuid.UUID, p *Protection, roles []string) error {
	if p == nil {
		return q.DeleteProtectionRule(ctx, envID)
	}
	branches := p.AllowedBranches
	if branches == nil {
		branches = []string{}
	}
	return q.UpsertProtectionRule(ctx, store.UpsertProtectionRuleParams{
		EnvironmentID: envID, RequiredApprovals: p.RequiredApprovals, AllowedBranches: branches, AllowedRoles: roles,
	})
}

// ListEnvironments lists a project's environments (development, staging, production order).
func (s *Service) ListEnvironments(ctx context.Context, projectID uuid.UUID) ([]Environment, error) {
	q := store.New(s.pool)
	if _, err := load(ctx, q, projectID, authz.ProjectView); err != nil {
		return nil, err
	}
	rows, err := q.ListEnvironments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Environment, 0, len(rows))
	for _, r := range rows {
		e, err := toEnvironment(r.Environment, r.Protected, r.RequiredApprovals, r.AllowedBranches, r.AllowedRoles)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// CreateEnvironment adds an environment (project Admins).
func (s *Service) CreateEnvironment(ctx context.Context, projectID uuid.UUID, in EnvironmentInput) (Environment, error) {
	vars, roles, err := in.validate(true)
	if err != nil {
		return Environment{}, err
	}
	var out Environment
	err = s.inTx(ctx, func(q *store.Queries) error {
		a, err := load(ctx, q, projectID, authz.EnvironmentManage)
		if err != nil {
			return err
		}
		n, err := q.CountEnvironments(ctx, projectID)
		if err != nil {
			return err
		}
		if n >= MaxEnvironments {
			return apperr.New(apperr.CodeEnvironmentLimit, http.StatusConflict, "this project has the maximum number of environments").
				WithDetails(map[string]any{"max": MaxEnvironments})
		}
		e, err := q.CreateEnvironment(ctx, store.CreateEnvironmentParams{
			ProjectID: projectID, Name: in.Name, Kind: store.EnvironmentKind(in.Kind), Variables: vars,
		})
		if database.IsUniqueViolation(err, "environments_project_name_key") {
			return apperr.New(apperr.CodeEnvironmentNameTaken, http.StatusConflict, "an environment with this name already exists").
				WithDetails(map[string]any{"name": in.Name})
		}
		if err != nil {
			return err
		}
		if err := s.saveProtection(ctx, q, e.ID, in.Protection, roles); err != nil {
			return err
		}
		out, err = s.getEnvironment(ctx, q, e.ID)
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.project.OrganizationID, ProjectID: &projectID, Action: "environment.create",
			ResourceType: "environment", ResourceID: e.ID.String(), After: auditEnv(out),
		})
	})
	return out, err
}

func (s *Service) getEnvironment(ctx context.Context, q *store.Queries, id uuid.UUID) (Environment, error) {
	r, err := q.GetEnvironment(ctx, id)
	if database.IsNoRows(err) {
		return Environment{}, errEnvironmentNotFound()
	}
	if err != nil {
		return Environment{}, err
	}
	return toEnvironment(r.Environment, r.Protected, r.RequiredApprovals, r.AllowedBranches, r.AllowedRoles)
}

// loadEnvironment resolves an environment and checks the action on its project. Environments
// of projects the caller can't see are ENVIRONMENT_NOT_FOUND.
func (s *Service) loadEnvironment(ctx context.Context, q *store.Queries, id uuid.UUID, act authz.Action) (Environment, access, error) {
	e, err := s.getEnvironment(ctx, q, id)
	if err != nil {
		return Environment{}, access{}, err
	}
	a, err := load(ctx, q, e.ProjectID, act)
	if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeProjectNotFound {
		return Environment{}, access{}, errEnvironmentNotFound()
	}
	return e, a, err
}

// GetEnvironment returns one environment (any project member).
func (s *Service) GetEnvironment(ctx context.Context, id uuid.UUID) (Environment, error) {
	e, _, err := s.loadEnvironment(ctx, store.New(s.pool), id, authz.ProjectView)
	return e, err
}

// UpdateEnvironment replaces an environment's kind, variables and protection (If-Match).
// The name can't change: pipelines refer to environments by name.
func (s *Service) UpdateEnvironment(ctx context.Context, id uuid.UUID, version int32, in EnvironmentInput) (Environment, error) {
	vars, roles, err := in.validate(false)
	if err != nil {
		return Environment{}, err
	}
	var out Environment
	err = s.inTx(ctx, func(q *store.Queries) error {
		before, a, err := s.loadEnvironment(ctx, q, id, authz.EnvironmentManage)
		if err != nil {
			return err
		}
		if _, err := q.UpdateEnvironment(ctx, store.UpdateEnvironmentParams{
			ID: id, Version: version, Kind: store.EnvironmentKind(in.Kind), Variables: vars,
		}); err != nil {
			if database.IsNoRows(err) {
				return apperr.VersionConflict()
			}
			return err
		}
		if err := s.saveProtection(ctx, q, id, in.Protection, roles); err != nil {
			return err
		}
		out, err = s.getEnvironment(ctx, q, id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.project.OrganizationID, ProjectID: &a.project.ID, Action: "environment.update",
			ResourceType: "environment", ResourceID: id.String(), Before: auditEnv(before), After: auditEnv(out),
		})
	})
	return out, err
}

// DeleteEnvironment removes an environment (project Admins).
func (s *Service) DeleteEnvironment(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		e, a, err := s.loadEnvironment(ctx, q, id, authz.EnvironmentManage)
		if err != nil {
			return err
		}
		if err := q.SoftDeleteEnvironment(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.project.OrganizationID, ProjectID: &a.project.ID, Action: "environment.delete",
			ResourceType: "environment", ResourceID: id.String(), Before: auditEnv(e),
		})
	})
}

// auditEnv is what the audit log keeps about an environment: variable names, not values.
func auditEnv(e Environment) map[string]any {
	names := make([]string, 0, len(e.Variables))
	for k := range e.Variables {
		names = append(names, k)
	}
	slices.Sort(names)
	return map[string]any{"name": e.Name, "kind": e.Kind, "variables": names, "protection": e.Protection}
}
