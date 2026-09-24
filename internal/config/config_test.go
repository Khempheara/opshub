package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("OPSHUB_DATABASE_URL", "postgres://localhost/opshub")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.HTTPAddr)
	assert.Equal(t, "text", cfg.LogFormat)
	assert.Equal(t, "Asia/Phnom_Penh", cfg.DefaultTimezone)
	assert.Empty(t, cfg.TrustedProxies)
}

func TestLoadProductionDefaultsToJSONLogs(t *testing.T) {
	t.Setenv("OPSHUB_DATABASE_URL", "postgres://localhost/opshub")
	t.Setenv("OPSHUB_ENV", "production")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "json", cfg.LogFormat)
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Setenv("OPSHUB_DATABASE_URL", "postgres://localhost/opshub")
	t.Setenv("OPSHUB_ENV", "staging")
	t.Setenv("OPSHUB_DEFAULT_LOCALE", "fr")
	t.Setenv("OPSHUB_DEFAULT_TIMEZONE", "Mars/Olympus")
	t.Setenv("OPSHUB_TRUSTED_PROXIES", "10.0.0.0/8,not-a-cidr")
	_, err := Load()
	require.Error(t, err)
	assert.ErrorContains(t, err, "OPSHUB_ENV")
	assert.ErrorContains(t, err, "OPSHUB_DEFAULT_LOCALE")
	assert.ErrorContains(t, err, "OPSHUB_DEFAULT_TIMEZONE")
	assert.ErrorContains(t, err, `"not-a-cidr"`)
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("OPSHUB_DATABASE_URL", "")
	_, err := Load()
	assert.ErrorContains(t, err, "OPSHUB_DATABASE_URL")
}
