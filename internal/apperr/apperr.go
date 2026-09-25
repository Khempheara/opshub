// Package apperr defines the machine-readable error model shared by every layer.
//
// The API never returns localized sentences: clients translate by Code. Message is an
// English fallback for logs, curl users and API consumers without a translation table.
package apperr

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
)

// Code is a stable, SCREAMING_SNAKE_CASE identifier. Codes are part of the public API:
// never rename one; add a new code instead. Every code must have an entry in
// web/src/locales/{en,km}/errors.json (enforced by TestAllCodesTranslated).
type Code string

const (
	CodeInternal         Code = "INTERNAL"
	CodeBadRequest       Code = "BAD_REQUEST"
	CodeValidation       Code = "VALIDATION_FAILED"
	CodeUnauthenticated  Code = "UNAUTHENTICATED"
	CodeForbidden        Code = "FORBIDDEN"
	CodeNotFound         Code = "NOT_FOUND"
	CodeRouteNotFound    Code = "ROUTE_NOT_FOUND"
	CodeMethodNotAllowed Code = "METHOD_NOT_ALLOWED"
	CodeConflict         Code = "CONFLICT"
	CodePayloadTooLarge  Code = "PAYLOAD_TOO_LARGE"
	CodeRateLimited      Code = "RATE_LIMITED"
	CodeUnavailable      Code = "SERVICE_UNAVAILABLE"
	CodeVersionConflict  Code = "VERSION_CONFLICT"
	CodePrecondition     Code = "PRECONDITION_REQUIRED"
	CodeCSRF             Code = "CSRF_FAILED"
	CodeSessionRequired  Code = "SESSION_REQUIRED"
	CodeScope            Code = "INSUFFICIENT_SCOPE"
)

// Module 1: auth & users.
const (
	CodeInvalidCredentials  Code = "INVALID_CREDENTIALS" // #nosec G101 -- an error code, not a credential
	CodeAccountLocked       Code = "ACCOUNT_LOCKED"
	CodeAccountDisabled     Code = "ACCOUNT_DISABLED"
	CodeEmailNotVerified    Code = "EMAIL_NOT_VERIFIED"
	CodeSignupDisabled      Code = "SIGNUP_DISABLED"
	CodeInvalidToken        Code = "INVALID_OR_EXPIRED_TOKEN" // #nosec G101 -- an error code, not a credential
	CodePasswordTooShort    Code = "PASSWORD_TOO_SHORT"
	CodePasswordTooLong     Code = "PASSWORD_TOO_LONG"
	CodePasswordBreached    Code = "PASSWORD_BREACHED"
	CodePasswordWeak        Code = "PASSWORD_TOO_WEAK"
	CodePasswordIncorrect   Code = "PASSWORD_INCORRECT"
	CodeMFAInvalidCode      Code = "MFA_INVALID_CODE"
	CodeMFAChallengeExpired Code = "MFA_CHALLENGE_EXPIRED"
	CodeMFAAlreadyEnabled   Code = "MFA_ALREADY_ENABLED"
	CodeMFANotEnabled       Code = "MFA_NOT_ENABLED"
	CodeMFASetupRequired    Code = "MFA_SETUP_REQUIRED"
	CodeRefreshInvalid      Code = "REFRESH_TOKEN_INVALID"
	CodeRefreshReused       Code = "REFRESH_TOKEN_REUSED"
	CodeSSOUnknownProvider  Code = "SSO_PROVIDER_UNKNOWN"
	CodeSSOFailed           Code = "SSO_FAILED"
	CodeSSOEmailUnverified  Code = "SSO_EMAIL_UNVERIFIED"
	CodeLastLoginMethod     Code = "LAST_LOGIN_METHOD"
	CodeSessionNotFound     Code = "SESSION_NOT_FOUND"
	CodeTokenNotFound       Code = "TOKEN_NOT_FOUND"
	CodeIdentityNotFound    Code = "IDENTITY_NOT_FOUND"
	CodeOrgNotFound         Code = "ORG_NOT_FOUND"
	CodeSlugTaken           Code = "SLUG_TAKEN"
)

// Module 2: RBAC, members, invitations, teams.
const (
	CodeLastOwner               Code = "LAST_OWNER"
	CodeAlreadyMember           Code = "ALREADY_MEMBER"
	CodeMemberNotFound          Code = "MEMBER_NOT_FOUND"
	CodeRoleNotAllowed          Code = "ROLE_NOT_ALLOWED"
	CodeInvitationNotFound      Code = "INVITATION_NOT_FOUND"
	CodeInvitationEmailMismatch Code = "INVITATION_EMAIL_MISMATCH"
	CodeTeamNotFound            Code = "TEAM_NOT_FOUND"
	CodeConfirmationMismatch    Code = "CONFIRMATION_MISMATCH"
)

// Module 3: projects, repositories, environments, idempotency.
const (
	CodeProjectNotFound          Code = "PROJECT_NOT_FOUND"
	CodeEnvironmentNotFound      Code = "ENVIRONMENT_NOT_FOUND"
	CodeEnvironmentNameTaken     Code = "ENVIRONMENT_NAME_TAKEN"
	CodeEnvironmentLimit         Code = "ENVIRONMENT_LIMIT_REACHED"
	CodeRepositoryNotFound       Code = "REPOSITORY_NOT_FOUND"
	CodeGitRepoNotFound          Code = "GIT_REPO_NOT_FOUND"
	CodeGitAccessDenied          Code = "GIT_ACCESS_DENIED"
	CodeGitProviderUnreachable   Code = "GIT_PROVIDER_UNREACHABLE"
	CodeSSRFBlocked              Code = "SSRF_BLOCKED"
	CodeWebhookSignatureInvalid  Code = "WEBHOOK_SIGNATURE_INVALID"
	CodeIdempotencyKeyReused     Code = "IDEMPOTENCY_KEY_REUSED"
	CodeIdempotencyKeyInProgress Code = "IDEMPOTENCY_KEY_IN_PROGRESS"
)

// Module 4: pipelines.
const (
	CodeRunNotFound          Code = "RUN_NOT_FOUND"
	CodeJobNotFound          Code = "JOB_NOT_FOUND"
	CodePipelineInvalid      Code = "PIPELINE_INVALID"
	CodePipelineFileNotFound Code = "PIPELINE_FILE_NOT_FOUND"
	CodeRefNotFound          Code = "REF_NOT_FOUND"
	CodeRunNotCancelable     Code = "RUN_NOT_CANCELABLE"
	CodeJobNotRetryable      Code = "JOB_NOT_RETRYABLE"
	CodeApprovalNotAllowed   Code = "APPROVAL_NOT_ALLOWED"
	CodeJobNotRunning        Code = "JOB_NOT_RUNNING"
)

// AllCodes lists every code; append new codes here as modules add them.
var AllCodes = []Code{
	CodeInternal, CodeBadRequest, CodeValidation, CodeUnauthenticated, CodeForbidden,
	CodeNotFound, CodeRouteNotFound, CodeMethodNotAllowed, CodeConflict,
	CodePayloadTooLarge, CodeRateLimited, CodeUnavailable, CodeVersionConflict,
	CodePrecondition, CodeCSRF, CodeSessionRequired, CodeScope,

	CodeInvalidCredentials, CodeAccountLocked, CodeAccountDisabled, CodeEmailNotVerified,
	CodeSignupDisabled, CodeInvalidToken, CodePasswordTooShort, CodePasswordTooLong,
	CodePasswordBreached, CodePasswordWeak, CodePasswordIncorrect, CodeMFAInvalidCode,
	CodeMFAChallengeExpired, CodeMFAAlreadyEnabled, CodeMFANotEnabled, CodeMFASetupRequired,
	CodeRefreshInvalid, CodeRefreshReused, CodeSSOUnknownProvider, CodeSSOFailed,
	CodeSSOEmailUnverified, CodeLastLoginMethod, CodeSessionNotFound, CodeTokenNotFound,
	CodeIdentityNotFound, CodeOrgNotFound, CodeSlugTaken,

	CodeLastOwner, CodeAlreadyMember, CodeMemberNotFound, CodeRoleNotAllowed, CodeInvitationNotFound,
	CodeInvitationEmailMismatch, CodeTeamNotFound, CodeConfirmationMismatch,

	CodeProjectNotFound, CodeEnvironmentNotFound, CodeEnvironmentNameTaken, CodeEnvironmentLimit,
	CodeRepositoryNotFound, CodeGitRepoNotFound, CodeGitAccessDenied, CodeGitProviderUnreachable,
	CodeSSRFBlocked, CodeWebhookSignatureInvalid, CodeIdempotencyKeyReused, CodeIdempotencyKeyInProgress,

	CodeRunNotFound, CodeJobNotFound, CodePipelineInvalid, CodePipelineFileNotFound, CodeRefNotFound,
	CodeRunNotCancelable, CodeJobNotRetryable, CodeApprovalNotAllowed, CodeJobNotRunning,
}

// Error is an application error carrying its HTTP status and client-facing code.
type Error struct {
	Code    Code
	Status  int
	Message string
	Details map[string]any
	Err     error // internal cause; never serialized
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches on Code so errors.Is(err, apperr.NotFound(...)) style checks work.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// WithDetails returns a copy with the given details merged in.
func (e *Error) WithDetails(details map[string]any) *Error {
	cp := *e
	cp.Details = maps.Clone(e.Details)
	if cp.Details == nil {
		cp.Details = make(map[string]any, len(details))
	}
	maps.Copy(cp.Details, details)
	return &cp
}

// Wrap returns a copy that records cause as the internal error.
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.Err = cause
	return &cp
}

func New(code Code, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

// From extracts an *Error from err, or returns (nil, false).
func From(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Constructors for the generic codes. Modules define their own specific codes
// (e.g. PIPELINE_NOT_FOUND) with New and register them in AllCodes.

func Internal(cause error) *Error {
	return &Error{Code: CodeInternal, Status: http.StatusInternalServerError, Message: "internal server error", Err: cause}
}

func BadRequest(message string) *Error {
	return New(CodeBadRequest, http.StatusBadRequest, message)
}

func Unauthenticated() *Error {
	return New(CodeUnauthenticated, http.StatusUnauthorized, "authentication required")
}

func Forbidden() *Error {
	return New(CodeForbidden, http.StatusForbidden, "you do not have permission to perform this action")
}

func NotFound(message string) *Error {
	return New(CodeNotFound, http.StatusNotFound, message)
}

func Conflict(message string) *Error {
	return New(CodeConflict, http.StatusConflict, message)
}

// FieldError describes one failed validation rule; clients translate Rule.
type FieldError struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
	Param string `json:"param,omitempty"`
}

// Code-specific constructors shared across modules.

func VersionConflict() *Error {
	return New(CodeVersionConflict, http.StatusConflict, "the resource was modified by someone else; reload and retry")
}

func PreconditionRequired() *Error {
	return New(CodePrecondition, http.StatusPreconditionRequired, "If-Match header with the resource version is required")
}

func Validation(fields []FieldError) *Error {
	return New(CodeValidation, http.StatusUnprocessableEntity, "request validation failed").
		WithDetails(map[string]any{"fields": fields})
}
