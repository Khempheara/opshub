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
)

// AllCodes lists every code; append new codes here as modules add them.
var AllCodes = []Code{
	CodeInternal, CodeBadRequest, CodeValidation, CodeUnauthenticated, CodeForbidden,
	CodeNotFound, CodeRouteNotFound, CodeMethodNotAllowed, CodeConflict,
	CodePayloadTooLarge, CodeRateLimited, CodeUnavailable,
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

func Validation(fields []FieldError) *Error {
	return New(CodeValidation, http.StatusUnprocessableEntity, "request validation failed").
		WithDetails(map[string]any{"fields": fields})
}
