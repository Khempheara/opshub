// Package notify implements notification channels (Module 9): Telegram, Slack, email and
// webhooks. Credentials are sealed with the master key ring and write-only. Alert messages
// are rendered in the channel's language (or each member recipient's) and delivered by a
// River worker with retries.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/i18n"
	mailpkg "github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/store"
)

// MaxAddresses is the most email recipients one channel has.
const MaxAddresses = 20

// Config holds operator settings.
type Config struct {
	// PublicURL builds links to alerts in messages.
	PublicURL string
	// DefaultLocale is used when neither the channel nor the recipient has a language.
	DefaultLocale string
	// OutboundAllowedCIDRs lets webhooks on private networks through the SSRF guard.
	OutboundAllowedCIDRs []netip.Prefix
	// TelegramAPI is the Bot API base URL (tests point it elsewhere).
	TelegramAPI string
}

// Service manages channels and sends notifications.
type Service struct {
	pool     *pgxpool.Pool
	keys     *crypto.KeyRing
	bundle   *i18n.Bundle
	renderer *mailpkg.Renderer
	mailer   mailpkg.Sender
	client   *http.Client
	cfg      Config
	logger   *slog.Logger
}

func NewService(pool *pgxpool.Pool, keys *crypto.KeyRing, bundle *i18n.Bundle, mailer mailpkg.Sender, cfg Config, logger *slog.Logger) *Service {
	if cfg.TelegramAPI == "" {
		cfg.TelegramAPI = "https://api.telegram.org"
	}
	if i18n.Normalize(cfg.DefaultLocale) == "" {
		cfg.DefaultLocale = i18n.Fallback
	}
	return &Service{
		pool: pool, keys: keys, bundle: bundle, renderer: &mailpkg.Renderer{Bundle: bundle}, mailer: mailer, cfg: cfg, logger: logger,
		client: safehttp.NewClient(safehttp.Options{AllowedCIDRs: cfg.OutboundAllowedCIDRs, Timeout: 15 * time.Second}),
	}
}

func (s *Service) inTx(ctx context.Context, fn func(q *store.Queries) error) error {
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(store.New(tx)) })
}

// Kinds.
const (
	KindTelegram = store.ChannelKindTelegram
	KindSlack    = store.ChannelKindSlack
	KindEmail    = store.ChannelKindEmail
	KindWebhook  = store.ChannelKindWebhook
)

// ChannelConfig is the non-secret part of a channel (by kind).
type ChannelConfig struct {
	ChatID    string   `json:"chat_id,omitempty"`   // telegram
	Addresses []string `json:"addresses,omitempty"` // email
	URL       string   `json:"url,omitempty"`       // webhook
}

// ChannelSecrets are write-only (by kind).
type ChannelSecrets struct {
	BotToken      string `json:"bot_token,omitempty"`      // telegram
	WebhookURL    string `json:"webhook_url,omitempty"`    // slack
	SigningSecret string `json:"signing_secret,omitempty"` // webhook (optional)
}

func (c ChannelSecrets) names() []string {
	var out []string
	if c.BotToken != "" {
		out = append(out, "bot_token")
	}
	if c.WebhookURL != "" {
		out = append(out, "webhook_url")
	}
	if c.SigningSecret != "" {
		out = append(out, "signing_secret")
	}
	sort.Strings(out)
	return out
}

// Channel is the API representation. Secrets lists which credentials are set.
type Channel struct {
	ID         uuid.UUID         `json:"id"`
	Name       string            `json:"name"`
	Kind       store.ChannelKind `json:"kind"`
	Config     ChannelConfig     `json:"config"`
	Secrets    []string          `json:"secrets"`
	Locale     *string           `json:"locale"`
	LastTestAt *time.Time        `json:"last_test_at"`
	LastTestOK *bool             `json:"last_test_ok"`
	Version    int32             `json:"version"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

func errNotFound() *apperr.Error {
	return apperr.New(apperr.CodeChannelNotFound, http.StatusNotFound, "notification channel not found")
}

func errOrgNotFound() *apperr.Error {
	return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
}

func (s *Service) secretsOf(c store.NotificationChannel) (ChannelSecrets, error) {
	var out ChannelSecrets
	raw, err := s.keys.Decrypt(c.SecretsEnc, c.ID[:])
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

func (s *Service) toChannel(c store.NotificationChannel) Channel {
	out := Channel{
		ID: c.ID, Name: c.Name, Kind: c.Kind, Secrets: []string{}, Locale: c.Locale, LastTestAt: c.LastTestAt,
		LastTestOK: c.LastTestOk, Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	_ = json.Unmarshal(c.Config, &out.Config)
	if sec, err := s.secretsOf(c); err == nil {
		out.Secrets = sec.names()
	}
	return out
}

// ChannelInput is POST /orgs/{id}/notification-channels and PATCH (kind ignored there;
// empty secrets keep the stored ones).
type ChannelInput struct {
	Name    string            `json:"name"`
	Kind    store.ChannelKind `json:"kind"`
	Config  ChannelConfig     `json:"config"`
	Secrets ChannelSecrets    `json:"secrets"`
	Locale  *string           `json:"locale"`
}

var (
	botTokenPattern = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{30,64}$`)
	chatIDPattern   = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

func httpURL(v string, httpsOnly bool) bool {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || u.User != nil || len(v) > 2048 {
		return false
	}
	return u.Scheme == "https" || (!httpsOnly && u.Scheme == "http")
}

// normalize validates in for kind, merging stored secrets (update) into empty fields.
func (in *ChannelInput) normalize(kind store.ChannelKind, stored *ChannelSecrets) []apperr.FieldError {
	var fields []apperr.FieldError
	add := func(field, rule, param string) {
		fields = append(fields, apperr.FieldError{Field: field, Rule: rule, Param: param})
	}
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 {
		add("name", "range", "1-100")
	}
	if in.Locale != nil {
		l := i18n.Normalize(*in.Locale)
		switch {
		case *in.Locale == "":
			in.Locale = nil // the recipient's language
		case l == "":
			add("locale", "oneof", "en km")
		default:
			in.Locale = &l
		}
	}
	if stored != nil {
		if in.Secrets.BotToken == "" {
			in.Secrets.BotToken = stored.BotToken
		}
		if in.Secrets.WebhookURL == "" {
			in.Secrets.WebhookURL = stored.WebhookURL
		}
		if in.Secrets.SigningSecret == "" {
			in.Secrets.SigningSecret = stored.SigningSecret
		}
	}
	c, sec := ChannelConfig{}, ChannelSecrets{}
	switch kind {
	case KindTelegram:
		c.ChatID = strings.TrimSpace(in.Config.ChatID)
		sec.BotToken = strings.TrimSpace(in.Secrets.BotToken)
		if !chatIDPattern.MatchString(c.ChatID) {
			add("config.chat_id", "pattern", "")
		}
		if !botTokenPattern.MatchString(sec.BotToken) {
			add("secrets.bot_token", "pattern", "")
		}
	case KindSlack:
		sec.WebhookURL = strings.TrimSpace(in.Secrets.WebhookURL)
		if !httpURL(sec.WebhookURL, true) {
			add("secrets.webhook_url", "url", "")
		}
	case KindEmail:
		if len(in.Config.Addresses) == 0 {
			add("config.addresses", "required", "")
		}
		if len(in.Config.Addresses) > MaxAddresses {
			add("config.addresses", "max", fmt.Sprint(MaxAddresses))
		}
		for i, a := range in.Config.Addresses {
			a = strings.ToLower(strings.TrimSpace(a))
			if p, err := mail.ParseAddress(a); err != nil || p.Address != a || p.Name != "" {
				add(fmt.Sprintf("config.addresses[%d]", i), "email", "")
				continue
			}
			if !slices.Contains(c.Addresses, a) {
				c.Addresses = append(c.Addresses, a)
			}
		}
	case KindWebhook:
		c.URL = strings.TrimSpace(in.Config.URL)
		sec.SigningSecret = in.Secrets.SigningSecret
		if !httpURL(c.URL, false) {
			add("config.url", "url", "")
		}
		if n := len(sec.SigningSecret); n > 0 && (n < 16 || n > 256) {
			add("secrets.signing_secret", "range", "16-256")
		}
	default:
		add("kind", "oneof", "telegram slack email webhook")
	}
	in.Config, in.Secrets = c, sec
	return fields
}

func (s *Service) seal(id uuid.UUID, sec ChannelSecrets) ([]byte, error) {
	raw, err := json.Marshal(sec)
	if err != nil {
		return nil, err
	}
	return s.keys.Encrypt(raw, id[:])
}

func channelAudit(c store.NotificationChannel) map[string]any {
	return map[string]any{"name": c.Name, "kind": c.Kind, "locale": c.Locale, "config": json.RawMessage(c.Config)}
}

// ListChannels returns an organization's channels (channel.view).
func (s *Service) ListChannels(ctx context.Context, orgID uuid.UUID) ([]Channel, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.ChannelView); err != nil {
		return nil, err
	}
	rows, err := q.ListChannels(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toChannel(r))
	}
	return out, nil
}

// CreateChannel adds a channel (channel.manage).
func (s *Service) CreateChannel(ctx context.Context, orgID uuid.UUID, in ChannelInput) (Channel, error) {
	var out Channel
	err := s.inTx(ctx, func(q *store.Queries) error {
		m, err := authz.Require(ctx, q, orgID, authz.ChannelManage)
		if err != nil {
			return err
		}
		if fields := in.normalize(in.Kind, nil); len(fields) > 0 {
			return apperr.Validation(fields)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		enc, err := s.seal(id, in.Secrets)
		if err != nil {
			return err
		}
		cfg, _ := json.Marshal(in.Config)
		c, err := q.CreateChannel(ctx, store.CreateChannelParams{
			ID: id, OrganizationID: orgID, Name: in.Name, Kind: in.Kind, Config: cfg, SecretsEnc: enc, Locale: in.Locale, CreatedBy: &m.UserID,
		})
		if database.IsUniqueViolation(err, "notification_channels_organization_id_name_key") {
			return apperr.New(apperr.CodeChannelNameTaken, http.StatusConflict, "a channel with this name exists")
		}
		if err != nil {
			return err
		}
		out = s.toChannel(c)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: "channel.create", ResourceType: "notification_channel", ResourceID: id.String(),
			After: channelAudit(c),
		})
	})
	return out, err
}

// loadChannel fetches a channel and checks the action on its organization. A channel of an
// organization the caller can't see answers CHANNEL_NOT_FOUND.
func (s *Service) loadChannel(ctx context.Context, q *store.Queries, id uuid.UUID, a authz.Action) (store.NotificationChannel, authz.Membership, error) {
	c, err := q.GetChannel(ctx, id)
	if database.IsNoRows(err) {
		return c, authz.Membership{}, errNotFound()
	}
	if err != nil {
		return c, authz.Membership{}, err
	}
	m, err := authz.Require(ctx, q, c.OrganizationID, a)
	if ae, ok := apperr.From(err); ok && ae.Code == apperr.CodeOrgNotFound {
		return c, m, errNotFound()
	}
	return c, m, err
}

// UpdateChannel replaces a channel's name, settings and language; empty secrets keep the
// stored ones (channel.manage, If-Match).
func (s *Service) UpdateChannel(ctx context.Context, id uuid.UUID, version int32, in ChannelInput) (Channel, error) {
	var out Channel
	err := s.inTx(ctx, func(q *store.Queries) error {
		before, _, err := s.loadChannel(ctx, q, id, authz.ChannelManage)
		if err != nil {
			return err
		}
		stored, err := s.secretsOf(before)
		if err != nil {
			stored = ChannelSecrets{} // unreadable (key removed): new values are required
		}
		if fields := in.normalize(before.Kind, &stored); len(fields) > 0 {
			return apperr.Validation(fields)
		}
		enc, err := s.seal(id, in.Secrets)
		if err != nil {
			return err
		}
		cfg, _ := json.Marshal(in.Config)
		c, err := q.UpdateChannel(ctx, store.UpdateChannelParams{
			ID: id, Name: in.Name, Config: cfg, SecretsEnc: enc, Locale: in.Locale, ExpectedVersion: version,
		})
		if database.IsNoRows(err) {
			return apperr.VersionConflict()
		}
		if database.IsUniqueViolation(err, "notification_channels_organization_id_name_key") {
			return apperr.New(apperr.CodeChannelNameTaken, http.StatusConflict, "a channel with this name exists")
		}
		if err != nil {
			return err
		}
		out = s.toChannel(c)
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &c.OrganizationID, Action: "channel.update", ResourceType: "notification_channel", ResourceID: id.String(),
			Before: channelAudit(before), After: channelAudit(c),
		})
	})
	return out, err
}

// DeleteChannel removes a channel (channel.manage). Channels still used by a rule's
// escalation are refused with the rules' names.
func (s *Service) DeleteChannel(ctx context.Context, id uuid.UUID) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		c, _, err := s.loadChannel(ctx, q, id, authz.ChannelManage)
		if err != nil {
			return err
		}
		rules, err := q.RulesUsingChannel(ctx, store.RulesUsingChannelParams{OrganizationID: c.OrganizationID, ChannelID: id.String()})
		if err != nil {
			return err
		}
		if len(rules) > 0 {
			return apperr.New(apperr.CodeChannelInUse, http.StatusConflict, "alert rules use this channel").
				WithDetails(map[string]any{"rules": rules})
		}
		if _, err := q.DeleteChannel(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &c.OrganizationID, Action: "channel.delete", ResourceType: "notification_channel", ResourceID: id.String(),
			Before: channelAudit(c),
		})
	})
}
