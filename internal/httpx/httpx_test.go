package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/httpx"
)

type createReq struct {
	Email string `json:"email" validate:"required,email"`
	Name  string `json:"name" validate:"required,min=3"`
}

func decode(body string) error {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	var dst createReq
	return httpx.Decode(httptest.NewRecorder(), r, &dst)
}

func codeOf(t *testing.T, err error) apperr.Code {
	t.Helper()
	ae, ok := apperr.From(err)
	require.True(t, ok, "expected apperr, got %v", err)
	return ae.Code
}

func TestDecode(t *testing.T) {
	assert.NoError(t, decode(`{"email":"a@b.co","name":"Dara"}`))
	assert.Equal(t, apperr.CodeBadRequest, codeOf(t, decode(``)))
	assert.Equal(t, apperr.CodeBadRequest, codeOf(t, decode(`{"email":`)))
	assert.Equal(t, apperr.CodeBadRequest, codeOf(t, decode(`{"email":"a@b.co","name":"Dara","admin":true}`)))
	assert.Equal(t, apperr.CodeBadRequest, codeOf(t, decode(`{"email":"a@b.co","name":"Dara"}{}`)))
	big := `{"email":"a@b.co","name":"` + strings.Repeat("x", httpx.MaxBodyBytes) + `"}`
	assert.Equal(t, apperr.CodePayloadTooLarge, codeOf(t, decode(big)))
}

func TestDecodeValidationReportsJSONFieldNames(t *testing.T) {
	err := decode(`{"email":"nope","name":"ab"}`)
	ae, ok := apperr.From(err)
	require.True(t, ok)
	assert.Equal(t, apperr.CodeValidation, ae.Code)
	assert.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	assert.ElementsMatch(t, []apperr.FieldError{
		{Field: "email", Rule: "email"},
		{Field: "name", Rule: "min", Param: "3"},
	}, ae.Details["fields"])
}

func TestErrorEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{"app error", apperr.NotFound("pipeline not found"), 404, "NOT_FOUND", "pipeline not found"},
		{"unknown error hides cause", errors.New("pq: password=secret"), 500, "INTERNAL", "internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			httpx.Error(rec, httptest.NewRequest(http.MethodGet, "/", nil), tt.err)

			assert.Equal(t, tt.wantStatus, rec.Code)
			var body httpx.ErrorBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tt.wantCode, string(body.Error.Code))
			assert.Equal(t, tt.wantMsg, body.Error.Message)
			assert.NotNil(t, body.Error.Details, "details must always be an object")
			assert.NotContains(t, rec.Body.String(), "secret")
		})
	}
}
