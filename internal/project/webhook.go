package project

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/store"
)

// MaxWebhookBody bounds webhook request bodies (GitHub caps payloads at 25 MB; pushes with
// huge commit lists are truncated by the providers long before this).
const MaxWebhookBody = 5 << 20

// maxStoredPayload: larger payloads are recorded without their body.
const maxStoredPayload = 1 << 20

// WebhookOutcome is the result of handling an incoming webhook.
type WebhookOutcome string

const (
	WebhookAccepted  WebhookOutcome = "accepted"
	WebhookDuplicate WebhookOutcome = "duplicate"
)

func errWebhookNotFound() *apperr.Error {
	// Deliberately generic: don't confirm which repository ids exist.
	return apperr.New(apperr.CodeNotFound, http.StatusNotFound, "not found")
}

// ReceiveWebhook verifies and records a webhook request. Deliveries with a bad signature are
// recorded without their payload (to help debug a wrong secret) and rejected with
// WEBHOOK_SIGNATURE_INVALID. A valid delivery already recorded (a redelivery) is reported
// as a duplicate. Module 4 turns accepted push events into pipeline runs.
func (s *Service) ReceiveWebhook(ctx context.Context, provider gitprovider.Provider, repoID uuid.UUID, h http.Header, body []byte) (WebhookOutcome, error) {
	q := store.New(s.pool)
	repo, err := q.GetRepositoryForWebhook(ctx, store.GetRepositoryForWebhookParams{ID: repoID, Provider: store.GitProvider(provider)})
	if database.IsNoRows(err) {
		return "", errWebhookNotFound()
	}
	if err != nil {
		return "", err
	}
	secret, err := s.keys.Decrypt(repo.WebhookSecretEnc, repo.ID[:])
	if err != nil {
		return "", err
	}

	var valid bool
	switch provider {
	case gitprovider.GitHub:
		valid = gitprovider.VerifyGitHubSignature(secret, body, h.Get("X-Hub-Signature-256"))
	case gitprovider.GitLab:
		valid = gitprovider.VerifyGitLabToken(secret, h.Get("X-Gitlab-Token"))
	}

	var ev gitprovider.Event
	var perr error
	if valid {
		if provider == gitprovider.GitHub {
			ev, perr = gitprovider.ParseGitHub(h, body)
		} else {
			ev, perr = gitprovider.ParseGitLab(h, body)
		}
		if perr != nil {
			return "", apperr.New(apperr.CodeBadRequest, http.StatusBadRequest, "webhook body is not valid JSON")
		}
	} else {
		// Record who knocked, but trust nothing in the body.
		ev = gitprovider.Event{DeliveryID: h.Get("X-GitHub-Delivery"), Kind: h.Get("X-GitHub-Event")}
		if provider == gitprovider.GitLab {
			ev = gitprovider.Event{DeliveryID: h.Get("X-Gitlab-Event-UUID"), Kind: h.Get("X-Gitlab-Event")}
		}
		ev = sanitizeUnverified(ev)
	}

	var payload []byte
	if valid && len(body) <= maxStoredPayload && json.Valid(body) {
		payload = body
	}
	_, err = q.InsertWebhookDelivery(ctx, store.InsertWebhookDeliveryParams{
		RepositoryID: repo.ID, DeliveryID: ev.DeliveryID, Event: ev.Kind, Ref: ev.Ref, CommitSha: ev.CommitSHA,
		SignatureValid: valid, Payload: payload,
	})
	if !valid {
		if err != nil {
			return "", err
		}
		return "", apperr.New(apperr.CodeWebhookSignatureInvalid, http.StatusUnauthorized, "webhook signature is invalid")
	}
	if database.IsNoRows(err) {
		return WebhookDuplicate, nil
	}
	if err != nil {
		return "", err
	}
	if err := q.TouchRepositoryDelivery(ctx, repo.ID); err != nil {
		return "", err
	}
	return WebhookAccepted, nil
}

// sanitizeUnverified bounds header-derived fields of an unauthenticated request.
func sanitizeUnverified(ev gitprovider.Event) gitprovider.Event {
	clip := func(s string, n int) string {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	ev.DeliveryID = clip(ev.DeliveryID, 200)
	if ev.DeliveryID == "" {
		id, _ := uuid.NewV7()
		ev.DeliveryID = "unverified:" + id.String()
	}
	ev.Kind = clip(ev.Kind, 100)
	if ev.Kind == "" {
		ev.Kind = "unknown"
	}
	return ev
}
