// Package config loads OpsHub runtime configuration from environment variables (12-factor).
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/opshub/opshub/internal/safehttp"
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
	// Extra browser origins allowed to call the API (CORS). Empty: same-origin only.
	CORSAllowedOrigins []string `env:"OPSHUB_CORS_ALLOWED_ORIGINS" envSeparator:","`

	LogLevel  slog.Level `env:"OPSHUB_LOG_LEVEL" envDefault:"info"`
	LogFormat string     `env:"OPSHUB_LOG_FORMAT"` // "json" | "text"; defaults to json in production

	DatabaseURL string `env:"OPSHUB_DATABASE_URL,required,notEmpty"`
	// Schema-owner connection used for migrations; defaults to DatabaseURL. In production the
	// API runs as a DML-only role and only the migration job holds this URL.
	MigrateDatabaseURL string `env:"OPSHUB_MIGRATE_DATABASE_URL"`
	DBMaxConns         int32  `env:"OPSHUB_DB_MAX_CONNS" envDefault:"20"`
	MigrateOnStart     bool   `env:"OPSHUB_MIGRATE_ON_START" envDefault:"true"`

	ShutdownTimeout time.Duration `env:"OPSHUB_SHUTDOWN_TIMEOUT" envDefault:"20s"`

	// Tracing is enabled when the standard OTLP endpoint variable is set.
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	DefaultLocale   string `env:"OPSHUB_DEFAULT_LOCALE" envDefault:"en"`
	DefaultTimezone string `env:"OPSHUB_DEFAULT_TIMEZONE" envDefault:"Asia/Phnom_Penh"`

	// Key rings: "id:base64key,id:base64key". The first key is active (encrypt/sign); the
	// others are kept only to decrypt/verify during rotation. Generate with `opshub-api keys generate`.
	MasterKeys string `env:"OPSHUB_MASTER_KEYS,required,notEmpty"` // AES-256 KEKs (32 bytes)
	JWTKeys    string `env:"OPSHUB_JWT_KEYS,required,notEmpty"`    // Ed25519 seeds (32 bytes)

	// Sign-up policy. The bootstrap admin can always register and becomes platform admin.
	AllowSignup         bool   `env:"OPSHUB_ALLOW_SIGNUP" envDefault:"true"`
	BootstrapAdminEmail string `env:"OPSHUB_BOOTSTRAP_ADMIN_EMAIL"`

	// Rate limits (per client IP when anonymous, per user/token when authenticated).
	RateLimitRPS   float64 `env:"OPSHUB_RATE_LIMIT_RPS" envDefault:"20"`
	RateLimitBurst int     `env:"OPSHUB_RATE_LIMIT_BURST" envDefault:"40"`
	// Stricter per-IP limits on sign-in and on endpoints that send email / consume tokens.
	AuthLoginPerMinute int `env:"OPSHUB_AUTH_LOGIN_PER_MINUTE" envDefault:"10"`
	AuthEmailPerMinute int `env:"OPSHUB_AUTH_EMAIL_PER_MINUTE" envDefault:"5"`

	// Outbound requests to user-supplied hosts (self-hosted Git) may not reach internal
	// addresses (SSRF protection) except these CIDRs or single addresses.
	OutboundAllowedCIDRs []string `env:"OPSHUB_OUTBOUND_ALLOWED_CIDRS" envSeparator:","`

	// Pipeline artifacts and caches are stored as files here (a shared volume when several
	// API replicas run). Sizes are in bytes.
	BlobDir          string `env:"OPSHUB_BLOB_DIR" envDefault:"data/blobs"`
	ArtifactMaxBytes int64  `env:"OPSHUB_ARTIFACT_MAX_BYTES" envDefault:"104857600"`         // 100 MiB per job
	CacheMaxBytes    int64  `env:"OPSHUB_CACHE_MAX_BYTES" envDefault:"524288000"`            // 500 MiB per entry
	CacheQuotaBytes  int64  `env:"OPSHUB_CACHE_PROJECT_QUOTA_BYTES" envDefault:"2147483648"` // 2 GiB per project
	SourceMaxBytes   int64  `env:"OPSHUB_SOURCE_MAX_BYTES" envDefault:"524288000"`           // 500 MiB per checkout

	// DeployLocalDocker allows Docker deploy targets that use the API host's own socket
	// (root-equivalent on that host; for single-machine setups and development).
	DeployLocalDocker bool `env:"OPSHUB_DEPLOY_LOCAL_DOCKER" envDefault:"false"`

	SMTP SMTPConfig
	SSO  SSOConfig
}

// SMTPConfig configures outgoing email (verification, password reset, notifications).
type SMTPConfig struct {
	Host     string `env:"OPSHUB_SMTP_HOST" envDefault:"localhost"`
	Port     int    `env:"OPSHUB_SMTP_PORT" envDefault:"1025"`
	Username string `env:"OPSHUB_SMTP_USERNAME"`
	Password string `env:"OPSHUB_SMTP_PASSWORD"`
	From     string `env:"OPSHUB_SMTP_FROM" envDefault:"OpsHub <noreply@opshub.local>"`
	// "none" (plain, dev only), "starttls" (port 587) or "tls" (implicit TLS, port 465).
	TLSMode string `env:"OPSHUB_SMTP_TLS" envDefault:"starttls"`
}

// SSOConfig enables login providers; a provider is enabled when its client ID is set.
type SSOConfig struct {
	GitHubClientID     string `env:"OPSHUB_SSO_GITHUB_CLIENT_ID"`
	GitHubClientSecret string `env:"OPSHUB_SSO_GITHUB_CLIENT_SECRET"`
	// Override for GitHub Enterprise Server, e.g. https://github.example.com
	GitHubURL    string `env:"OPSHUB_SSO_GITHUB_URL" envDefault:"https://github.com"`
	GitHubAPIURL string `env:"OPSHUB_SSO_GITHUB_API_URL" envDefault:"https://api.github.com"`

	GoogleClientID     string `env:"OPSHUB_SSO_GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `env:"OPSHUB_SSO_GOOGLE_CLIENT_SECRET"`

	KeycloakIssuer       string `env:"OPSHUB_SSO_KEYCLOAK_ISSUER"` // e.g. https://sso.example.com/realms/acme
	KeycloakClientID     string `env:"OPSHUB_SSO_KEYCLOAK_CLIENT_ID"`
	KeycloakClientSecret string `env:"OPSHUB_SSO_KEYCLOAK_CLIENT_SECRET"`
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
	if cfg.MigrateDatabaseURL == "" {
		cfg.MigrateDatabaseURL = cfg.DatabaseURL
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
	if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("OPSHUB_PUBLIC_URL must be an absolute http(s) URL, got %q", c.PublicURL))
	} else if c.IsProduction() && u.Scheme != "https" {
		errs = append(errs, errors.New("OPSHUB_PUBLIC_URL must use https in production"))
	}
	for _, o := range c.CORSAllowedOrigins {
		if u, err := url.Parse(o); err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			errs = append(errs, fmt.Errorf("OPSHUB_CORS_ALLOWED_ORIGINS: %q is not an origin (scheme://host[:port])", o))
		}
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
	if _, err := safehttp.ParseCIDRs(c.OutboundAllowedCIDRs); err != nil {
		errs = append(errs, fmt.Errorf("OPSHUB_OUTBOUND_ALLOWED_CIDRS: %w", err))
	}
	for name, v := range map[string]int64{
		"OPSHUB_ARTIFACT_MAX_BYTES": c.ArtifactMaxBytes, "OPSHUB_CACHE_MAX_BYTES": c.CacheMaxBytes,
		"OPSHUB_CACHE_PROJECT_QUOTA_BYTES": c.CacheQuotaBytes, "OPSHUB_SOURCE_MAX_BYTES": c.SourceMaxBytes,
	} {
		if v < 1 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if c.DBMaxConns < 1 {
		errs = append(errs, errors.New("OPSHUB_DB_MAX_CONNS must be >= 1"))
	}
	if _, err := ParseKeyRing(c.MasterKeys); err != nil {
		errs = append(errs, fmt.Errorf("OPSHUB_MASTER_KEYS: %w", err))
	}
	if _, err := ParseKeyRing(c.JWTKeys); err != nil {
		errs = append(errs, fmt.Errorf("OPSHUB_JWT_KEYS: %w", err))
	}
	if c.BootstrapAdminEmail != "" {
		if _, err := mail.ParseAddress(c.BootstrapAdminEmail); err != nil {
			errs = append(errs, fmt.Errorf("OPSHUB_BOOTSTRAP_ADMIN_EMAIL: %w", err))
		}
	}
	if c.RateLimitRPS <= 0 || c.RateLimitBurst < 1 {
		errs = append(errs, errors.New("OPSHUB_RATE_LIMIT_RPS must be > 0 and OPSHUB_RATE_LIMIT_BURST >= 1"))
	}
	if c.AuthLoginPerMinute < 1 || c.AuthEmailPerMinute < 1 {
		errs = append(errs, errors.New("OPSHUB_AUTH_LOGIN_PER_MINUTE and OPSHUB_AUTH_EMAIL_PER_MINUTE must be >= 1"))
	}
	if !slices.Contains([]string{"none", "starttls", "tls"}, c.SMTP.TLSMode) {
		errs = append(errs, fmt.Errorf("OPSHUB_SMTP_TLS must be none, starttls or tls, got %q", c.SMTP.TLSMode))
	} else if c.IsProduction() && c.SMTP.TLSMode == "none" && c.SMTP.Username != "" {
		errs = append(errs, errors.New("OPSHUB_SMTP_TLS=none would send SMTP credentials in cleartext"))
	}
	if _, err := mail.ParseAddress(c.SMTP.From); err != nil {
		errs = append(errs, fmt.Errorf("OPSHUB_SMTP_FROM: %w", err))
	}
	if (c.SSO.KeycloakClientID == "") != (c.SSO.KeycloakIssuer == "") {
		errs = append(errs, errors.New("OPSHUB_SSO_KEYCLOAK_ISSUER and OPSHUB_SSO_KEYCLOAK_CLIENT_ID must be set together"))
	}
	return errors.Join(errs...)
}

func (c Config) IsProduction() bool { return c.Env == EnvProduction }

// SecureCookies reports whether cookies must carry the Secure flag (public URL is https).
func (c Config) SecureCookies() bool { return strings.HasPrefix(c.PublicURL, "https://") }

// NamedKey is one entry of a key ring.
type NamedKey struct {
	ID  string
	Key []byte
}

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ParseKeyRing parses "id:base64,id:base64" into 32-byte keys; the first is active.
func ParseKeyRing(s string) ([]NamedKey, error) {
	var keys []NamedKey
	seen := map[string]bool{}
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, b64, ok := strings.Cut(entry, ":")
		if !ok || !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("entry must be <id>:<base64 key> with id matching %s", keyIDPattern)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate key id %q", id)
		}
		seen[id] = true
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("key %q: invalid base64", id)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("key %q: must be 32 bytes, got %d", id, len(key))
		}
		keys = append(keys, NamedKey{ID: id, Key: key})
	}
	if len(keys) == 0 {
		return nil, errors.New("at least one key is required")
	}
	return keys, nil
}
