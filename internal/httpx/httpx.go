// Package httpx contains HTTP helpers shared by all handlers: JSON responses,
// the error envelope, and validated request decoding.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-playground/validator/v10"

	"github.com/opshub/opshub/internal/apperr"
)

// MaxBodyBytes bounds JSON request bodies.
const MaxBodyBytes = 1 << 20

// ErrorBody is the wire format: { "error": { "code", "message", "details" } }.
type ErrorBody struct {
	Error ErrorPayload `json:"error"`
}

type ErrorPayload struct {
	Code    apperr.Code    `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// Error renders err as the standard envelope. Non-apperr errors become INTERNAL and are
// logged with their cause; client errors (4xx) are not logged at error level.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	ae, ok := apperr.From(err)
	if !ok {
		ae = apperr.Internal(err)
	}
	if ae.Status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed",
			"code", ae.Code, "error", err, "request_id", middleware.GetReqID(r.Context()))
	}
	details := ae.Details
	if details == nil {
		details = map[string]any{}
	}
	JSON(w, ae.Status, ErrorBody{Error: ErrorPayload{Code: ae.Code, Message: ae.Message, Details: details}})
}

var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Report JSON field names, not Go field names.
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
	return v
}

// Validate runs struct validation and converts failures to VALIDATION_FAILED.
func Validate(v any) error {
	err := validate.Struct(v)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return apperr.Internal(err)
	}
	fields := make([]apperr.FieldError, 0, len(verrs))
	for _, fe := range verrs {
		// Namespace is "Struct.field.sub"; drop the root struct name.
		_, field, _ := strings.Cut(fe.Namespace(), ".")
		fields = append(fields, apperr.FieldError{Field: field, Rule: fe.Tag(), Param: fe.Param()})
	}
	return apperr.Validation(fields)
}

// Decode reads a JSON body into dst (rejecting unknown fields and oversized bodies)
// and validates it.
func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			return apperr.New(apperr.CodePayloadTooLarge, http.StatusRequestEntityTooLarge, "request body too large")
		case errors.Is(err, io.EOF):
			return apperr.BadRequest("request body is empty")
		default:
			return apperr.BadRequest("malformed JSON body").WithDetails(map[string]any{"reason": err.Error()})
		}
	}
	if dec.More() {
		return apperr.BadRequest("request body must contain a single JSON object")
	}
	return Validate(dst)
}
