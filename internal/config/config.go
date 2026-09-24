// Package config loads OpsHub runtime configuration from environment variables (12-factor).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"github.com/caarlos0/env/v11"
)

const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// SupportedLocales are the UI/notification locales. "en" is the fallback.
var SupportedLocales = []string{"en", "km"}

// Config is the full API server configuration. See .env.example for documentation.
type Config struct {
	Env       string `env:"OPSHUB_ENV" envDefault:"development"`
	HTTPAddr  string `env:"OPSHUB_HTTP_ADDR" envDefault:":8080"`
	PublicURL string `env:"OPSHUB_PUBLIC_URL" envDefault:"http://localhost:5173"`
	// CIDRs of reverse proxies / ingress controllers allowed to set X-Forwarded-For.
	// Empty means the TCP peer address is the client IP (no proxy headers are trusted).
	TrustedProxies []string `env:"OPSHUB_TRUSTED_PROXIES" envSeparator:","`

	LogLevel  slog.Level `env:"OPSHUB_LOG_LEVEL" envDefault:"info"`
	LogFormat string     `env:"OPSHUB_LOG_FORMAT"` // "json" | "text"; defaults to json in production

	DatabaseURL    string `env:"OPSHUB_DATABASE_URL,required,notEmpty"`
	DBMaxConns     int32  `env:"OPSHUB_DB_MAX_CONNS" envDefault:"20"`
	MigrateOnStart bool   `env:"OPSHUB_MIGRATE_ON_START" envDefault:"true"`

	ShutdownTimeout time.Duration `env:"OPSHUB_SHUTDOWN_TIMEOUT" envDefault:"20s"`

	// Tracing is enabled when the standard OTLP endpoint variable is set.
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	DefaultLocale   string `env:"OPSHUB_DEFAULT_LOCALE" envDefault:"en"`
	DefaultTimezone string `env:"OPSHUB_DEFAULT_TIMEZONE" envDefault:"Asia/Phnom_Penh"`
}

// Load parses the environment and validates the result.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("parse env: %w", err)
	}
	if cfg.LogFormat == "" {
		cfg.LogFormat = "text"
		if cfg.IsProduction() {
			cfg.LogFormat = "json"
		}
	}
	return cfg, cfg.Validate()
}

// Validate checks cross-field and enumerated values.
func (c Config) Validate() error {
	var errs []error
	if !slices.Contains([]string{EnvDevelopment, EnvTest, EnvProduction}, c.Env) {
		errs = append(errs, fmt.Errorf("OPSHUB_ENV must be development, test or production, got %q", c.Env))
	}
	if !slices.Contains([]string{"json", "text"}, c.LogFormat) {
		errs = append(errs, fmt.Errorf("OPSHUB_LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}
	if !slices.Contains(SupportedLocales, c.DefaultLocale) {
		errs = append(errs, fmt.Errorf("OPSHUB_DEFAULT_LOCALE must be one of %v, got %q", SupportedLocales, c.DefaultLocale))
	}
	if _, err := time.LoadLocation(c.DefaultTimezone); err != nil {
		errs = append(errs, fmt.Errorf("OPSHUB_DEFAULT_TIMEZONE: %w", err))
	}
	for _, p := range c.TrustedProxies {
		if _, err := netip.ParsePrefix(p); err != nil {
			errs = append(errs, fmt.Errorf("OPSHUB_TRUSTED_PROXIES: %q is not a CIDR prefix", p))
		}
	}
	if c.DBMaxConns < 1 {
		errs = append(errs, errors.New("OPSHUB_DB_MAX_CONNS must be >= 1"))
	}
	return errors.Join(errs...)
}

func (c Config) IsProduction() bool { return c.Env == EnvProduction }
