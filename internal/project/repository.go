package project

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/store"
)

// Repository is a project's connected Git repository. The access token and webhook secret
// are never returned (the secret is shown once, when manual webhook setup is needed).
type Repository struct {
	ID             uuid.UUID  `json:"id"`
	Provider       string     `json:"provider"`
	BaseURL        *string    `json:"base_url"`
	FullName       string     `json:"full_name"`
	WebURL         string     `json:"web_url"`
	CloneURL       string     `json:"clone_url"`
	DefaultBranch  string     `json:"default_branch"`
	WebhookMode    string     `json:"webhook_mode"` // automatic | manual
	WebhookURL     string     `json:"webhook_url"`
	LastDeliveryAt *time.Time `json:"last_delivery_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

// Webhook setup reasons when OpsHub couldn't create the webhook itself.
const (
	ManualNoPermission = "token_cannot_manage_webhooks"
	ManualRejected     = "provider_rejected_webhook"
	ManualUnreachable  = "provider_unreachable"
)

// ConnectResult is PUT /projects/{id}/repository. In manual mode it carries the secret to
// paste into the Git host; it's shown only here.
type ConnectResult struct {
	Repository Repository `json:"repository"`
	Webhook    struct {
		Mode   string `json:"mode"`
		URL    string `json:"url"`
		Secret string `json:"secret,omitempty"`
		Reason string `json:"reason,omitempty"`
	} `json:"webhook"`
}

// ConnectInput connects (or replaces) a project's repository.
type ConnectInput struct {
	Provider    string `json:"provider" validate:"required,oneof=github gitlab"`
	BaseURL     string `json:"base_url" validate:"max=255"`
	FullName    string `json:"full_name" validate:"required,max=255"`
	AccessToken string `json:"access_token" validate:"required,max=512"` // #nosec G117 -- request field, never logged
}

// TestResult is POST /projects/{id}/repository/test.
type TestResult struct {
	DefaultBranch      string `json:"default_branch"`
	CanManageWebhooks  bool   `json:"can_manage_webhooks"`
	WebhookInstalled   bool   `json:"webhook_installed"` // OpsHub created the hook (automatic mode)
	RepositoryFullName string `json:"full_name"`
}

// Delivery is a recorded webhook request (without its payload).
type Delivery struct {
	ID             uuid.UUID `json:"id"`
	DeliveryID     string    `json:"delivery_id"`
	Event          string    `json:"event"`
	Ref            string    `json:"ref"`
	CommitSHA      string    `json:"commit_sha"`
	SignatureValid bool      `json:"signature_valid"`
	ReceivedAt     time.Time `json:"received_at"`
}

func (s *Service) webhookURL(r store.Repository) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/api/v1/webhooks/" + string(r.Provider) + "/" + r.ID.String()
}

func (s *Service) toRepository(r store.Repository) Repository {
	return Repository{
		ID: r.ID, Provider: string(r.Provider), BaseURL: r.BaseUrl, FullName: r.FullName, WebURL: r.WebUrl,
		CloneURL: r.CloneUrl, DefaultBranch: r.DefaultBranch, WebhookMode: string(r.WebhookMode),
		WebhookURL: s.webhookURL(r), LastDeliveryAt: r.LastDeliveryAt, CreatedAt: r.CreatedAt,
	}
}

func errRepositoryNotFound() *apperr.Error {
	return apperr.New(apperr.CodeRepositoryNotFound, http.StatusNotFound, "no repository is connected to this project")
}

// gitError maps provider errors to API errors.
func gitError(err error) error {
	switch {
	case safehttp.IsBlocked(err):
		return apperr.New(apperr.CodeSSRFBlocked, http.StatusUnprocessableEntity,
			"this Git host resolves to an internal address; an operator must allow it (OPSHUB_OUTBOUND_ALLOWED_CIDRS)")
	case errors.Is(err, gitprovider.ErrInvalidInput):
		return apperr.Validation([]apperr.FieldError{{Field: "full_name", Rule: "repository"}})
	case errors.Is(err, gitprovider.ErrNotFound):
		return apperr.New(apperr.CodeGitRepoNotFound, http.StatusUnprocessableEntity, "repository not found, or the token can't see it")
	case errors.Is(err, gitprovider.ErrUnauthorized), errors.Is(err, gitprovider.ErrForbidden):
		return apperr.New(apperr.CodeGitAccessDenied, http.StatusUnprocessableEntity, "the Git host rejected the access token")
	case errors.Is(err, gitprovider.ErrUnavailable):
		return apperr.New(apperr.CodeGitProviderUnreachable, http.StatusBadGateway, "the Git host could not be reached")
	}
	return err
}

// GetRepository returns the connected repository (any project member).
func (s *Service) GetRepository(ctx context.Context, projectID uuid.UUID) (Repository, error) {
	q := store.New(s.pool)
	if _, err := load(ctx, q, projectID, authz.ProjectView); err != nil {
		return Repository{}, err
	}
	r, err := q.GetRepositoryByProject(ctx, projectID)
	if database.IsNoRows(err) {
		return Repository{}, errRepositoryNotFound()
	}
	if err != nil {
		return Repository{}, err
	}
	return s.toRepository(r), nil
}

// ConnectRepository validates the repository and token against the Git host, then tries to
// create a webhook with a fresh random secret. If OpsHub can't (the token lacks permission,
// the host rejects the URL, or OpsHub isn't reachable), the repository is still connected in
// manual mode and the result carries the URL and secret to configure by hand. Connecting
// again replaces the previous repository and removes OpsHub's old webhook.
func (s *Service) ConnectRepository(ctx context.Context, projectID uuid.UUID, in ConnectInput) (ConnectResult, error) {
	var res ConnectResult
	// Authorize before contacting the Git host with the caller's token.
	if _, err := load(ctx, store.New(s.pool), projectID, authz.RepoConnect); err != nil {
		return res, err
	}
	provider := gitprovider.Provider(in.Provider)
	fullName := strings.Trim(strings.TrimSpace(in.FullName), "/")
	baseURL, err := gitprovider.NormalizeBaseURL(in.BaseURL)
	if err != nil {
		return res, apperr.Validation([]apperr.FieldError{{Field: "base_url", Rule: "https_url"}})
	}
	if !gitprovider.ValidFullName(provider, fullName) {
		return res, apperr.Validation([]apperr.FieldError{{Field: "full_name", Rule: "repository"}})
	}
	client, err := s.git.Client(provider, baseURL, fullName, strings.TrimSpace(in.AccessToken))
	if err != nil {
		return res, gitError(err)
	}
	meta, err := client.Repo(ctx)
	if err != nil {
		return res, gitError(err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return res, err
	}
	secret := crypto.RandomToken(32)
	row := store.Repository{ID: id, Provider: store.GitProvider(provider)}
	hookURL := s.webhookURL(row)

	mode, reason, hookID := store.WebhookModeManual, "", ""
	if !meta.CanManageHooks {
		reason = ManualNoPermission
	} else if hookID, err = client.CreateHook(ctx, hookURL, secret); err != nil {
		switch {
		case errors.Is(err, gitprovider.ErrForbidden), errors.Is(err, gitprovider.ErrUnauthorized):
			reason = ManualRejected
		default:
			reason = ManualUnreachable
		}
		s.logger.WarnContext(ctx, "webhook creation failed; manual setup required", "provider", provider, "error", err)
		hookID = ""
	} else {
		mode = store.WebhookModeAutomatic
	}

	aad := id[:]
	tokenEnc, err := s.keys.Encrypt([]byte(strings.TrimSpace(in.AccessToken)), aad)
	if err != nil {
		return res, err
	}
	secretEnc, err := s.keys.Encrypt([]byte(secret), aad)
	if err != nil {
		return res, err
	}

	var replaced *store.Repository
	err = s.inTx(ctx, func(q *store.Queries) error {
		a, err := load(ctx, q, projectID, authz.RepoConnect)
		if err != nil {
			return err
		}
		old, err := q.DeleteRepositoryByProject(ctx, projectID)
		switch {
		case err == nil:
			replaced = &old
		case !database.IsNoRows(err):
			return err
		}
		var base, hook *string
		if baseURL != "" {
			base = &baseURL
		}
		if hookID != "" {
			hook = &hookID
		}
		saved, err := q.InsertRepository(ctx, store.InsertRepositoryParams{
			ID: id, ProjectID: projectID, Provider: store.GitProvider(provider), BaseUrl: base,
			FullName: meta.FullName, ExternalID: meta.ExternalID, WebUrl: meta.WebURL, CloneUrl: meta.CloneURL,
			DefaultBranch: meta.DefaultBranch, AccessTokenEnc: tokenEnc, WebhookSecretEnc: secretEnc,
			WebhookMode: mode, WebhookID: hook, ConnectedBy: &a.userID,
		})
		if err != nil {
			return err
		}
		res.Repository = s.toRepository(saved)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.project.OrganizationID, ProjectID: &projectID, Action: string(authz.RepoConnect),
			ResourceType: "repository", ResourceID: id.String(),
			After: map[string]any{"provider": provider, "full_name": meta.FullName, "base_url": baseURL, "webhook_mode": mode},
		})
	})
	if err != nil {
		if hookID != "" {
			// Don't leave an orphaned webhook on the Git host.
			if derr := client.DeleteHook(context.WithoutCancel(ctx), hookID); derr != nil {
				s.logger.WarnContext(ctx, "could not remove webhook after failed connect", "error", derr)
			}
		}
		return res, err
	}
	if replaced != nil {
		s.removeHook(ctx, *replaced)
	}
	res.Webhook.Mode, res.Webhook.URL, res.Webhook.Reason = string(mode), hookURL, reason
	if mode == store.WebhookModeManual {
		res.Webhook.Secret = secret
	}
	return res, nil
}

// DisconnectRepository removes the repository (and its encrypted token), then deletes
// OpsHub's webhook from the Git host on a best-effort basis.
func (s *Service) DisconnectRepository(ctx context.Context, projectID uuid.UUID) error {
	var removed store.Repository
	err := s.inTx(ctx, func(q *store.Queries) error {
		a, err := load(ctx, q, projectID, authz.RepoConnect)
		if err != nil {
			return err
		}
		removed, err = q.DeleteRepositoryByProject(ctx, projectID)
		if database.IsNoRows(err) {
			return errRepositoryNotFound()
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.project.OrganizationID, ProjectID: &projectID, Action: "repo.disconnect",
			ResourceType: "repository", ResourceID: removed.ID.String(),
			Before: map[string]any{"provider": removed.Provider, "full_name": removed.FullName},
		})
	})
	if err == nil {
		s.removeHook(ctx, removed)
	}
	return err
}

// clientFor builds a provider client from a stored repository (decrypting its token).
func (s *Service) clientFor(r store.Repository) (gitprovider.Client, error) {
	token, err := s.keys.Decrypt(r.AccessTokenEnc, r.ID[:])
	if err != nil {
		return nil, err
	}
	base := ""
	if r.BaseUrl != nil {
		base = *r.BaseUrl
	}
	return s.git.Client(gitprovider.Provider(r.Provider), base, r.FullName, string(token))
}

// removeHook deletes OpsHub's webhook from the Git host; failures are only logged (the
// repository is already gone from OpsHub, and deliveries to it will be rejected).
func (s *Service) removeHook(ctx context.Context, r store.Repository) {
	if r.WebhookMode != store.WebhookModeAutomatic || r.WebhookID == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	c, err := s.clientFor(r)
	if err == nil {
		err = c.DeleteHook(ctx, *r.WebhookID)
	}
	if err != nil {
		s.logger.WarnContext(ctx, "could not delete webhook from the Git host", "repository_id", r.ID, "error", err)
	}
}

// TestRepository re-checks the stored token against the Git host and refreshes the
// repository's default branch.
func (s *Service) TestRepository(ctx context.Context, projectID uuid.UUID) (TestResult, error) {
	q := store.New(s.pool)
	if _, err := load(ctx, q, projectID, authz.RepoConnect); err != nil {
		return TestResult{}, err
	}
	r, err := q.GetRepositoryByProject(ctx, projectID)
	if database.IsNoRows(err) {
		return TestResult{}, errRepositoryNotFound()
	}
	if err != nil {
		return TestResult{}, err
	}
	c, err := s.clientFor(r)
	if err != nil {
		return TestResult{}, err
	}
	meta, err := c.Repo(ctx)
	if err != nil {
		return TestResult{}, gitError(err)
	}
	if meta.DefaultBranch != "" && meta.DefaultBranch != r.DefaultBranch {
		if err := q.UpdateRepositoryDefaultBranch(ctx, store.UpdateRepositoryDefaultBranchParams{ID: r.ID, DefaultBranch: meta.DefaultBranch}); err != nil {
			return TestResult{}, err
		}
	}
	return TestResult{
		DefaultBranch: meta.DefaultBranch, CanManageWebhooks: meta.CanManageHooks,
		WebhookInstalled: r.WebhookMode == store.WebhookModeAutomatic, RepositoryFullName: meta.FullName,
	}, nil
}

type deliveryCursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

// ListDeliveries lists recent webhook deliveries, newest first (project Admins).
func (s *Service) ListDeliveries(ctx context.Context, projectID uuid.UUID, page pagination.Params) (pagination.Page[Delivery], error) {
	q := store.New(s.pool)
	if _, err := load(ctx, q, projectID, authz.RepoConnect); err != nil {
		return pagination.Page[Delivery]{}, err
	}
	r, err := q.GetRepositoryByProject(ctx, projectID)
	if database.IsNoRows(err) {
		return pagination.Page[Delivery]{}, errRepositoryNotFound()
	}
	if err != nil {
		return pagination.Page[Delivery]{}, err
	}
	params := store.ListWebhookDeliveriesParams{RepositoryID: r.ID, PageSize: page.FetchSize()}
	var cur deliveryCursor
	if has, err := page.Decode(&cur); err != nil {
		return pagination.Page[Delivery]{}, err
	} else if has {
		params.CursorAt, params.CursorID = &cur.At, &cur.ID
	}
	rows, err := q.ListWebhookDeliveries(ctx, params)
	if err != nil {
		return pagination.Page[Delivery]{}, err
	}
	return pagination.Build(rows, page.Limit, func(d store.ListWebhookDeliveriesRow) Delivery {
		return Delivery{ID: d.ID, DeliveryID: d.DeliveryID, Event: d.Event, Ref: d.Ref, CommitSHA: d.CommitSha,
			SignatureValid: d.SignatureValid, ReceivedAt: d.ReceivedAt}
	}, func(d store.ListWebhookDeliveriesRow) any { return deliveryCursor{At: d.ReceivedAt, ID: d.ID} }), nil
}
