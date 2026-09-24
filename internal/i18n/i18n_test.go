package i18n

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNegotiate(t *testing.T) {
	tests := []struct {
		name, pref, accept, want string
	}{
		{"default", "", "", "en"},
		{"user preference wins", "km", "en-US,en;q=0.9", "km"},
		{"regional user preference", "km-KH", "", "km"},
		{"accept-language khmer", "", "km-KH,km;q=0.9,en;q=0.8", "km"},
		{"accept-language weighted", "", "fr;q=0.9,km;q=0.5", "km"},
		{"unsupported falls back", "", "fr-FR,de;q=0.8", "en"},
		{"invalid preference ignored", "xx-invalid!", "km", "km"},
		{"garbage header", "", ";;;", "en"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Negotiate(tt.pref, tt.accept))
		})
	}
}

func TestBundleTranslatesWithFallback(t *testing.T) {
	b, err := NewBundle()
	require.NoError(t, err)

	data := map[string]any{"Number": 42, "Branch": "main"}
	assert.Equal(t, "Pipeline #42 failed on main", b.T("en", "notify.pipeline.failed.title", data))
	assert.Equal(t, "Pipeline #42 បានបរាជ័យនៅលើ main", b.T("km", "notify.pipeline.failed.title", data))
	assert.Equal(t, "missing.key", b.T("km", "missing.key", nil))
}

// km must define every key en defines (mirrors the frontend CI check).
func TestLocaleParity(t *testing.T) {
	keys := func(locale string) []string {
		raw, err := localeFS.ReadFile("locales/" + locale + ".json")
		require.NoError(t, err)
		m := map[string]string{}
		require.NoError(t, json.Unmarshal(raw, &m))
		for k, v := range m {
			assert.NotEmpty(t, v, "%s: empty value for %s", locale, k)
		}
		return slices.Sorted(maps.Keys(m))
	}
	assert.Equal(t, keys("en"), keys("km"))
}

func TestMiddlewareSetsLocale(t *testing.T) {
	var got string
	h := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = LocaleFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "km")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, "km", got)
	assert.Equal(t, "km", rec.Header().Get("Content-Language"))
}
