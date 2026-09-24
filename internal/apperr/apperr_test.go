package apperr_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
)

func TestErrorIsMatchesByCode(t *testing.T) {
	err := fmt.Errorf("loading: %w", apperr.NotFound("project not found"))
	assert.ErrorIs(t, err, apperr.NotFound("anything"))
	assert.NotErrorIs(t, err, apperr.Forbidden())

	ae, ok := apperr.From(err)
	require.True(t, ok)
	assert.Equal(t, apperr.CodeNotFound, ae.Code)
	assert.Equal(t, 404, ae.Status)
}

func TestWithDetailsDoesNotMutateOriginal(t *testing.T) {
	base := apperr.Conflict("slug taken")
	withSlug := base.WithDetails(map[string]any{"slug": "web"})
	assert.Nil(t, base.Details)
	assert.Equal(t, "web", withSlug.Details["slug"])
}

func TestWrapKeepsCauseForLogsOnly(t *testing.T) {
	cause := errors.New("pg: connection refused")
	err := apperr.Internal(cause)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, "internal server error", err.Message)
}

// Every error code must be translated in both UI locales; otherwise users would see a raw code.
func TestAllCodesTranslated(t *testing.T) {
	for _, locale := range []string{"en", "km"} {
		t.Run(locale, func(t *testing.T) {
			path := filepath.Join("..", "..", "web", "src", "locales", locale, "errors.json")
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var file struct {
				Codes map[string]string `json:"codes"`
			}
			require.NoError(t, json.Unmarshal(raw, &file))
			for _, code := range apperr.AllCodes {
				assert.NotEmpty(t, file.Codes[string(code)], "%s: missing translation for error code %s", path, code)
			}
		})
	}
}
