package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/jobs"
	mailpkg "github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

const botToken = "123456:ABCdefGhIJKlmnoPQRstuVWxyz0123456789" // #nosec G101 -- test value

type noJobs struct{}

func (noJobs) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

type user struct {
	id    uuid.UUID
	email string
	ctx   context.Context
}

func newUser(t *testing.T, locale string) user {
	t.Helper()
	now := time.Now()
	email := "nt-" + uuid.NewString()[:8] + "@example.com"
	u, err := store.New(pgtest.Pool(t)).CreateUser(context.Background(), store.CreateUserParams{
		Email: email, DisplayName: "Pager " + uuid.NewString()[:4], Locale: locale, Timezone: "UTC", EmailVerifiedAt: &now,
	})
	require.NoError(t, err)
	return user{id: u.ID, email: email, ctx: authn.WithPrincipal(context.Background(), authn.Principal{Kind: authn.KindSession, UserID: u.ID, SessionID: uuid.New()})}
}

func codeOf(t *testing.T, err error) apperr.Code {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.From(err)
	require.True(t, ok, "expected apperr, got %v", err)
	return ae.Code
}

func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	ae, ok := apperr.From(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, apperr.CodeValidation, ae.Code, "%v", err)
	var out []string
	for _, f := range ae.Details["fields"].([]apperr.FieldError) {
		out = append(out, f.Field+":"+f.Rule)
	}
	return out
}

// outbox records sent emails.
type outbox struct {
	mu   sync.Mutex
	msgs []mailpkg.Message
	fail bool
}

func (o *outbox) Send(_ context.Context, m mailpkg.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail {
		return errors.New("smtp down")
	}
	o.msgs = append(o.msgs, m)
	return nil
}

// hook records requests to fake Telegram/Slack/webhook endpoints.
type hook struct {
	mu     sync.Mutex
	reqs   []*http.Request
	bodies []string
	status int
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reqs = append(h.reqs, r)
	h.bodies = append(h.bodies, string(b))
	if h.status != 0 {
		w.WriteHeader(h.status)
	}
}

func (h *hook) last(t *testing.T) (*http.Request, string) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	require.NotEmpty(t, h.reqs)
	return h.reqs[len(h.reqs)-1], h.bodies[len(h.bodies)-1]
}

type env struct {
	svc                           *Service
	mail                          *outbox
	hook                          *hook
	url                           string // plain HTTP (Telegram API, webhooks)
	tlsURL                        string // HTTPS (Slack webhooks must be https)
	q                             *store.Queries
	orgID                         uuid.UUID
	orgSlug                       string
	owner, admin, dev, viewer, km user
	outsider                      user
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "k1", Key: []byte(strings.Repeat("n", 32))}})
	require.NoError(t, err)
	bundle, err := i18n.NewBundle()
	require.NoError(t, err)
	h := &hook{}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tlsSrv := httptest.NewTLSServer(h)
	t.Cleanup(tlsSrv.Close)
	box := &outbox{}
	e := &env{
		svc: NewService(pool, keys, bundle, box, Config{
			PublicURL: "https://ops.example.com", DefaultLocale: "en", TelegramAPI: srv.URL,
			OutboundAllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		}, slog.New(slog.NewTextHandler(io.Discard, nil))),
		mail: box, hook: h, url: srv.URL, tlsURL: tlsSrv.URL, q: store.New(pool),
		owner: newUser(t, "en"), admin: newUser(t, "en"), dev: newUser(t, "en"), viewer: newUser(t, "en"), km: newUser(t, "km"),
		outsider: newUser(t, "en"),
	}
	// Trusts the fake HTTPS server (the SSRF guard itself is tested in safehttp).
	e.svc.client = tlsSrv.Client()
	orgs := org.NewService(pool, noJobs{}, org.Config{PublicURL: "https://ops.example.com"})
	e.orgSlug = "nt-" + strings.ToLower(uuid.NewString()[:8])
	o, err := orgs.Create(e.owner.ctx, org.CreateInput{Name: "Pager Co", Slug: e.orgSlug})
	require.NoError(t, err)
	e.orgID = o.ID
	for u, role := range map[*user]authz.Role{&e.admin: authz.Admin, &e.dev: authz.Developer, &e.viewer: authz.Viewer, &e.km: authz.Viewer} {
		require.NoError(t, e.q.AddOrganizationMember(context.Background(), store.AddOrganizationMemberParams{OrganizationID: o.ID, UserID: u.id, Role: role}))
	}
	return e
}

func (e *env) channel(t *testing.T, in ChannelInput) Channel {
	t.Helper()
	c, err := e.svc.CreateChannel(e.admin.ctx, e.orgID, in)
	require.NoError(t, err)
	return c
}

// firingAlert inserts a firing alert of a CPU rule for a server named web-1.
func (e *env) firingAlert(t *testing.T) store.Alert {
	t.Helper()
	d, _ := json.Marshal(Details{Value: new(93.5), Threshold: new(90.0), Metric: "cpu"})
	var a store.Alert
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), `INSERT INTO alerts (organization_id, rule_name, rule_kind, severity,
		subject_type, subject_id, subject_name, status, details, started_at)
		VALUES ($1, 'CPU high', 'asset_metric', 'critical', 'asset', $2, 'web-1', 'firing', $3, now() - interval '5 minutes') RETURNING id`,
		e.orgID, uuid.New(), d).Scan(&a.ID))
	return a
}

func TestChannelValidation(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		in   ChannelInput
		want []string
	}{
		{ChannelInput{Kind: KindTelegram, Config: ChannelConfig{ChatID: "chat"}, Secrets: ChannelSecrets{BotToken: "nope"}},
			[]string{"name:range", "config.chat_id:pattern", "secrets.bot_token:pattern"}},
		{ChannelInput{Name: "s", Kind: KindSlack, Secrets: ChannelSecrets{WebhookURL: "http://hooks.slack.com/x"}}, []string{"secrets.webhook_url:url"}},
		{ChannelInput{Name: "m", Kind: KindEmail}, []string{"config.addresses:required"}},
		{ChannelInput{Name: "m", Kind: KindEmail, Config: ChannelConfig{Addresses: []string{"ops@example.com", "Ops <x@example.com>", "nope"}}},
			[]string{"config.addresses[1]:email", "config.addresses[2]:email"}},
		{ChannelInput{Name: "w", Kind: KindWebhook, Config: ChannelConfig{URL: "ftp://x"}, Secrets: ChannelSecrets{SigningSecret: "short"}},
			[]string{"config.url:url", "secrets.signing_secret:range"}},
		{ChannelInput{Name: "p", Kind: "pager", Locale: new("fr")}, []string{"kind:oneof", "locale:oneof"}},
	} {
		_, err := e.svc.CreateChannel(e.admin.ctx, e.orgID, c.in)
		assert.ElementsMatch(t, c.want, fieldsOf(t, err), "%+v", c.in)
	}
	c := e.channel(t, ChannelInput{Name: "mail", Kind: KindEmail, Config: ChannelConfig{Addresses: []string{" Ops@Example.com ", "ops@example.com"}}})
	assert.Equal(t, []string{"ops@example.com"}, c.Config.Addresses)
	_, err := e.svc.CreateChannel(e.admin.ctx, e.orgID, ChannelInput{Name: "mail", Kind: KindWebhook, Config: ChannelConfig{URL: "https://x.example.com"}})
	assert.Equal(t, apperr.CodeChannelNameTaken, codeOf(t, err))
}

func TestChannelPermissionsAndWriteOnlySecrets(t *testing.T) {
	e := newEnv(t)
	in := ChannelInput{Name: "tg", Kind: KindTelegram, Config: ChannelConfig{ChatID: "-100123"}, Secrets: ChannelSecrets{BotToken: botToken}, Locale: new("km")}
	_, err := e.svc.CreateChannel(e.dev.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err), "developers see channels, admins manage them")
	c := e.channel(t, in)
	assert.Equal(t, []string{"bot_token"}, c.Secrets)
	assert.Equal(t, "km", *c.Locale)

	list, err := e.svc.ListChannels(e.dev.ctx, e.orgID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	raw, _ := json.Marshal(list)
	assert.NotContains(t, string(raw), botToken, "credentials are write-only")
	_, err = e.svc.ListChannels(e.viewer.ctx, e.orgID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	_, err = e.svc.ListChannels(e.outsider.ctx, e.orgID)
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
	_, err = e.svc.TestChannel(e.outsider.ctx, c.ID)
	assert.Equal(t, apperr.CodeChannelNotFound, codeOf(t, err))

	// Updating without secrets keeps them; an empty locale means "the recipient's".
	c, err = e.svc.UpdateChannel(e.admin.ctx, c.ID, c.Version, ChannelInput{Name: "telegram", Config: ChannelConfig{ChatID: "@ops_alerts"}, Locale: new("")})
	require.NoError(t, err)
	assert.Equal(t, []string{"bot_token"}, c.Secrets)
	assert.Nil(t, c.Locale)
	stored, err := e.q.GetChannel(context.Background(), c.ID)
	require.NoError(t, err)
	assert.NotContains(t, string(stored.SecretsEnc), botToken, "sealed at rest")
	sec, err := e.svc.secretsOf(stored)
	require.NoError(t, err)
	assert.Equal(t, botToken, sec.BotToken)
	_, err = e.svc.UpdateChannel(e.admin.ctx, c.ID, c.Version-1, ChannelInput{Name: "x", Config: ChannelConfig{ChatID: "1"}})
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))

	// A channel that rules notify can't be deleted.
	esc, _ := json.Marshal([]map[string]any{{"after_minutes": 0, "channel_ids": []string{c.ID.String()}}})
	_, err = pgtest.Pool(t).Exec(context.Background(), `INSERT INTO alert_rules (organization_id, name, kind, escalation) VALUES ($1, 'Down', 'monitor_down', $2)`, e.orgID, esc)
	require.NoError(t, err)
	err = e.svc.DeleteChannel(e.admin.ctx, c.ID)
	assert.Equal(t, apperr.CodeChannelInUse, codeOf(t, err))
	ae, _ := apperr.From(err)
	assert.Equal(t, []string{"Down"}, ae.Details["rules"])
	_, err = pgtest.Pool(t).Exec(context.Background(), `DELETE FROM alert_rules WHERE organization_id = $1`, e.orgID)
	require.NoError(t, err)
	require.NoError(t, e.svc.DeleteChannel(e.admin.ctx, c.ID))
}

func TestTestMessages(t *testing.T) {
	e := newEnv(t)
	ctx := e.admin.ctx
	tg := e.channel(t, ChannelInput{Name: "tg", Kind: KindTelegram, Config: ChannelConfig{ChatID: "-100123"}, Secrets: ChannelSecrets{BotToken: botToken}, Locale: new("km")})
	res, err := e.svc.TestChannel(ctx, tg.ID)
	require.NoError(t, err)
	assert.True(t, res.OK, res.Error)
	r, body := e.hook.last(t)
	assert.Equal(t, "/bot"+botToken+"/sendMessage", r.URL.Path)
	assert.Contains(t, body, `"chat_id":"-100123"`)
	assert.Contains(t, body, "សារសាកល្បង", "the channel's language")

	const signing = "a-long-signing-secret-value"
	wh := e.channel(t, ChannelInput{Name: "wh", Kind: KindWebhook, Config: ChannelConfig{URL: e.url + "/hooks/opshub"}, Secrets: ChannelSecrets{SigningSecret: signing}})
	res, err = e.svc.TestChannel(ctx, wh.ID)
	require.NoError(t, err)
	assert.True(t, res.OK)
	r, body = e.hook.last(t)
	assert.Equal(t, "test", r.Header.Get("X-OpsHub-Event"))
	mac := hmac.New(sha256.New, []byte(signing))
	mac.Write([]byte(body))
	assert.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-OpsHub-Signature"))
	assert.Contains(t, body, `"slug":"`+e.orgSlug+`"`)

	// Failures are reported without the URL (it may hold a token) and recorded.
	e.hook.status = http.StatusBadGateway
	res, err = e.svc.TestChannel(ctx, tg.ID)
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Equal(t, "HTTP 502", res.Error)
	e.hook.status = 0
	down := e.channel(t, ChannelInput{Name: "down", Kind: KindSlack, Secrets: ChannelSecrets{WebhookURL: "https://127.0.0.1:1/services/T000/B000/SECRETPART"}})
	res, err = e.svc.TestChannel(ctx, down.ID)
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Equal(t, "connection failed: connection refused", res.Error)
	assert.NotContains(t, res.Error, "SECRETPART")
	stored, err := e.q.GetChannel(context.Background(), down.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.LastTestOk)
	assert.False(t, *stored.LastTestOk)

	// Email: each address in its member's language, the default for others.
	mail := e.channel(t, ChannelInput{Name: "mail", Kind: KindEmail, Config: ChannelConfig{Addresses: []string{e.km.email, "oncall@example.com"}}})
	res, err = e.svc.TestChannel(ctx, mail.ID)
	require.NoError(t, err)
	assert.True(t, res.OK)
	require.Len(t, e.mail.msgs, 2)
	subjects := map[string]string{}
	for _, m := range e.mail.msgs {
		subjects[m.To] = m.Subject
	}
	assert.Equal(t, "សារសាកល្បងពី OpsHub", subjects[e.km.email])
	assert.Equal(t, "Test message from OpsHub", subjects["oncall@example.com"])
	e.mail.fail = true
	res, err = e.svc.TestChannel(ctx, mail.ID)
	require.NoError(t, err)
	assert.False(t, res.OK)
}

func TestDeliver(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.firingAlert(t)
	sl := e.channel(t, ChannelInput{Name: "slack", Kind: KindSlack, Secrets: ChannelSecrets{WebhookURL: e.tlsURL + "/services/x"}})

	require.NoError(t, e.svc.Deliver(ctx, jobs.NotifyArgs{AlertID: a.ID, ChannelID: sl.ID, Event: EventFiring}, 1, 5))
	_, body := e.hook.last(t)
	var msg map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &msg))
	assert.Equal(t, "🔴 [Critical] Alert firing: CPU high\nweb-1: CPU at 93.5% (limit 90%).\nhttps://ops.example.com/o/"+e.orgSlug+"/monitoring/alerts/"+a.ID.String(), msg["text"])

	// An escalation step says so; Khmer channels get Khmer.
	km := e.channel(t, ChannelInput{Name: "km", Kind: KindSlack, Secrets: ChannelSecrets{WebhookURL: e.tlsURL + "/services/km"}, Locale: new("km")})
	require.NoError(t, e.svc.Deliver(ctx, jobs.NotifyArgs{AlertID: a.ID, ChannelID: km.ID, Event: EventFiring, Step: 1}, 1, 5))
	_, body = e.hook.last(t)
	require.NoError(t, json.Unmarshal([]byte(body), &msg))
	assert.Contains(t, msg["text"], "ការជូនដំណឹងកំពុងសកម្ម៖ CPU high")
	assert.Contains(t, msg["text"], "(ជំហាន 2)")

	wh := e.channel(t, ChannelInput{Name: "wh", Kind: KindWebhook, Config: ChannelConfig{URL: e.url + "/hook"}})
	_, err := pgtest.Pool(t).Exec(ctx, `UPDATE alerts SET status = 'resolved', resolved_at = started_at + interval '2 hours 5 minutes' WHERE id = $1`, a.ID)
	require.NoError(t, err)
	require.NoError(t, e.svc.Deliver(ctx, jobs.NotifyArgs{AlertID: a.ID, ChannelID: wh.ID, Event: EventResolved}, 1, 5))
	r, body := e.hook.last(t)
	assert.Equal(t, "alert.resolved", r.Header.Get("X-OpsHub-Event"))
	assert.Empty(t, r.Header.Get("X-OpsHub-Signature"), "unsigned without a secret")
	var payload WebhookPayload
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.NotNil(t, payload.Alert)
	assert.Equal(t, "asset_metric", payload.Alert.Kind)
	assert.Equal(t, "cpu", payload.Alert.Details.Metric)
	assert.Contains(t, payload.Text, "Resolved after 2h 5m.")

	// Failures are recorded per attempt and retried; the last attempt gives up.
	e.hook.status = http.StatusInternalServerError
	err = e.svc.Deliver(ctx, jobs.NotifyArgs{AlertID: a.ID, ChannelID: sl.ID, Event: EventFiring}, 2, 5)
	require.Error(t, err)
	var cancel *river.JobCancelError
	assert.False(t, errors.As(err, &cancel))
	err = e.svc.Deliver(ctx, jobs.NotifyArgs{AlertID: a.ID, ChannelID: sl.ID, Event: EventFiring}, 5, 5)
	assert.True(t, errors.As(err, &cancel))
	events, err := e.q.ListAlertEvents(ctx, a.ID)
	require.NoError(t, err)
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Kind+":"+ev.ChannelName+":"+ev.Detail)
	}
	assert.Equal(t, []string{
		"notified:slack:firing", "notified:km:firing", "notified:wh:resolved",
		"notify_failed:slack:HTTP 500 (attempt 2 of 5)", "notify_failed:slack:HTTP 500 (attempt 5 of 5)",
	}, kinds)

	// A deleted channel or alert: nothing to do.
	require.NoError(t, e.svc.Deliver(ctx, jobs.NotifyArgs{AlertID: a.ID, ChannelID: uuid.New(), Event: EventFiring}, 1, 5))
	require.NoError(t, e.svc.Deliver(ctx, jobs.NotifyArgs{AlertID: uuid.New(), ChannelID: sl.ID, Event: EventFiring}, 1, 5))
}

func TestRenderEveryKind(t *testing.T) {
	e := newEnv(t)
	since := time.Date(2026, 9, 26, 3, 4, 0, 0, time.UTC)
	notAfter := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	days := 7
	for kind, c := range map[store.AlertRuleKind]struct {
		d    Details
		want string
	}{
		store.AlertRuleKindMonitorDown:    {Details{Error: "timed out"}, "shop is down: timed out"},
		store.AlertRuleKindMonitorLatency: {Details{Value: new(1250.0), Threshold: new(800.0)}, "shop responds in 1250 ms (limit 800 ms)."},
		store.AlertRuleKindAssetOffline:   {Details{Since: &since}, "shop: the agent stopped reporting at 2026-09-26 03:04 UTC."},
		store.AlertRuleKindCertificate:    {Details{Days: &days, NotAfter: &notAfter}, "shop: the TLS certificate expires in 7 days (2026-10-03)."},
	} {
		raw, _ := json.Marshal(c.d)
		m := e.svc.render("en", store.Alert{RuleName: "r", RuleKind: kind, Severity: store.AlertSeverityWarning, SubjectName: "shop", Details: raw}, "o", EventFiring, 0)
		assert.Equal(t, c.want, m.Summary, kind)
		km := e.svc.render("km", store.Alert{RuleName: "r", RuleKind: kind, Severity: store.AlertSeverityWarning, SubjectName: "shop", Details: raw}, "o", EventFiring, 0)
		assert.NotEqual(t, m.Summary, km.Summary, "translated: %s", kind)
	}
	raw, _ := json.Marshal(Details{Error: "certificate is not valid for this name"})
	m := e.svc.render("en", store.Alert{RuleKind: store.AlertRuleKindCertificate, SubjectName: "shop", Details: raw}, "o", EventFiring, 0)
	assert.Equal(t, "shop: the TLS certificate check failed: certificate is not valid for this name", m.Summary)
	expired, before := -3, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	raw, _ = json.Marshal(Details{Days: &expired, NotAfter: &before})
	m = e.svc.render("en", store.Alert{RuleKind: store.AlertRuleKindCertificate, SubjectName: "shop", Details: raw}, "o", EventFiring, 0)
	assert.Equal(t, "shop: the TLS certificate expired on 2026-09-28.", m.Summary)
	assert.Equal(t, "45s", formatDuration(45*time.Second))
	assert.Equal(t, "3d 2h", formatDuration(74*time.Hour))
}
