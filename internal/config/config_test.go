package config

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var key32 = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))

// setRequired sets the variables every configuration needs.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("OPSHUB_DATABASE_URL", "postgres://localhost/opshub")
	t.Setenv("OPSHUB_MASTER_KEYS", "m1:"+key32)
	t.Setenv("OPSHUB_JWT_KEYS", "k1:"+key32)
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.HTTPAddr)
	assert.Equal(t, "text", cfg.LogFormat)
	assert.Equal(t, "Asia/Phnom_Penh", cfg.DefaultTimezone)
	assert.Empty(t, cfg.TrustedProxies)
	assert.Equal(t, cfg.DatabaseURL, cfg.MigrateDatabaseURL, "migrations default to the app URL")
	assert.True(t, cfg.AllowSignup)
	assert.False(t, cfg.SecureCookies(), "http public URL")
}

func TestLoadProduction(t *testing.T) {
	setRequired(t)
	t.Setenv("OPSHUB_ENV", "production")
	t.Setenv("OPSHUB_PUBLIC_URL", "https://ops.example.com")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "json", cfg.LogFormat)
	assert.True(t, cfg.SecureCookies())

	t.Setenv("OPSHUB_PUBLIC_URL", "http://ops.example.com")
	_, err = Load()
	assert.ErrorContains(t, err, "https in production")
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	setRequired(t)
	t.Setenv("OPSHUB_ENV", "staging")
	t.Setenv("OPSHUB_DEFAULT_LOCALE", "fr")
	t.Setenv("OPSHUB_DEFAULT_TIMEZONE", "Mars/Olympus")
	t.Setenv("OPSHUB_TRUSTED_PROXIES", "10.0.0.0/8,not-a-cidr")
	t.Setenv("OPSHUB_CORS_ALLOWED_ORIGINS", "https://ok.example.com,https://bad.example.com/path")
	t.Setenv("OPSHUB_SMTP_TLS", "maybe")
	t.Setenv("OPSHUB_JWT_KEYS", "k1:short")
	_, err := Load()
	require.Error(t, err)
	for _, want := range []string{"OPSHUB_ENV", "OPSHUB_DEFAULT_LOCALE", "OPSHUB_DEFAULT_TIMEZONE", `"not-a-cidr"`,
		"https://bad.example.com/path", "OPSHUB_SMTP_TLS", "OPSHUB_JWT_KEYS"} {
		assert.ErrorContains(t, err, want)
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	t.Setenv("OPSHUB_DATABASE_URL", "")
	t.Setenv("OPSHUB_MASTER_KEYS", "")
	t.Setenv("OPSHUB_JWT_KEYS", "")
	_, err := Load()
	assert.ErrorContains(t, err, "OPSHUB_DATABASE_URL")
	assert.ErrorContains(t, err, "OPSHUB_MASTER_KEYS")
	assert.ErrorContains(t, err, "OPSHUB_JWT_KEYS")
}

func TestParseKeyRing(t *testing.T) {
	keys, err := ParseKeyRing(" m2:" + key32 + " , m1:" + key32)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.Equal(t, "m2", keys[0].ID, "first key is active")

	for _, bad := range []string{"", "nokey", "m1:" + key32 + ",m1:" + key32, "bad id!:" + key32, "m1:not-base64!!", "m1:c2hvcnQ="} {
		_, err := ParseKeyRing(bad)
		assert.Error(t, err, bad)
	}
}
