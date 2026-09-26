package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/jobs"
	mailpkg "github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/tlsprobe"
)

// sendError is a delivery failure described without secrets (URLs with tokens never appear).
type sendError struct{ msg string }

func (e *sendError) Error() string { return e.msg }

func failf(format string, args ...any) error { return &sendError{msg: fmt.Sprintf(format, args...)} }

// post sends JSON and reports failures without the URL (it may contain a token).
func (s *Service) post(ctx context.Context, target string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return failf("invalid address")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OpsHub-Notifier")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		switch {
		case safehttp.IsBlocked(err):
			return failf("address not allowed (OPSHUB_OUTBOUND_ALLOWED_CIDRS)")
		case errors.Is(err, context.DeadlineExceeded):
			return failf("timed out")
		}
		return failf("connection failed: %s", tlsprobe.ShortErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return failf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// target is where one channel delivers, with its secrets opened.
type target struct {
	c       store.NotificationChannel
	cfg     ChannelConfig
	secrets ChannelSecrets
}

func (s *Service) openTarget(c store.NotificationChannel) (target, error) {
	t := target{c: c}
	_ = json.Unmarshal(c.Config, &t.cfg)
	sec, err := s.secretsOf(c)
	if err != nil {
		return t, failf("the channel's credentials can't be decrypted; save them again")
	}
	t.secrets = sec
	return t, nil
}

// localeFor is the language of a message for one recipient: the channel's, else the
// member's (email), else the default.
func (s *Service) localeFor(t target, memberLocale string) string {
	if t.c.Locale != nil {
		return *t.c.Locale
	}
	if l := i18n.Normalize(memberLocale); l != "" {
		return l
	}
	return s.cfg.DefaultLocale
}

// email sends one message per address, each in its language. build returns the template
// name and data for a locale.
func (s *Service) email(ctx context.Context, t target, build func(locale string) (string, map[string]any)) error {
	rows, err := store.New(s.pool).MemberLocalesByEmail(ctx, store.MemberLocalesByEmailParams{OrganizationID: t.c.OrganizationID, Emails: t.cfg.Addresses})
	if err != nil {
		return err
	}
	member := map[string]string{}
	for _, r := range rows {
		member[r.Email] = r.Locale
	}
	var failed []string
	for _, addr := range t.cfg.Addresses {
		loc := s.localeFor(t, member[addr])
		name, data := build(loc)
		msg, err := s.renderer.Render(name, loc, data)
		if err != nil {
			return err
		}
		msg.To = addr
		if err := s.mailer.Send(ctx, msg); err != nil {
			s.logger.WarnContext(ctx, "alert email failed", "channel_id", t.c.ID, "error", err)
			failed = append(failed, addr)
		}
	}
	if len(failed) > 0 {
		return failf("email to %s failed", strings.Join(failed, ", "))
	}
	return nil
}

// sendChat delivers text (and, for webhooks, payload) to a non-email channel.
func (s *Service) sendChat(ctx context.Context, t target, text string, payload WebhookPayload) error {
	switch t.c.Kind {
	case KindTelegram:
		body, _ := json.Marshal(map[string]any{"chat_id": t.cfg.ChatID, "text": text, "disable_web_page_preview": true})
		return s.post(ctx, strings.TrimRight(s.cfg.TelegramAPI, "/")+"/bot"+t.secrets.BotToken+"/sendMessage", body, nil)
	case KindSlack:
		body, _ := json.Marshal(map[string]string{"text": text})
		return s.post(ctx, t.secrets.WebhookURL, body, nil)
	case KindWebhook:
		payload.Text = text
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		headers := map[string]string{"X-OpsHub-Event": payload.Event, "X-OpsHub-Delivery": uuid.NewString()}
		if t.secrets.SigningSecret != "" {
			mac := hmac.New(sha256.New, []byte(t.secrets.SigningSecret))
			mac.Write(body)
			headers["X-OpsHub-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		return s.post(ctx, t.cfg.URL, body, headers)
	}
	return failf("unknown channel kind %q", t.c.Kind)
}

func orgJSON(id uuid.UUID, slug, name string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"id": id.String(), "slug": slug, "name": name})
	return b
}

// TestResult is the outcome of POST /notification-channels/{id}/test.
type TestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// TestChannel sends a test message now and records the outcome (channel.manage).
func (s *Service) TestChannel(ctx context.Context, id uuid.UUID) (TestResult, error) {
	q := store.New(s.pool)
	c, _, err := s.loadChannel(ctx, q, id, authz.ChannelManage)
	if err != nil {
		return TestResult{}, err
	}
	org, err := q.GetOrgSlugName(ctx, c.OrganizationID)
	if err != nil {
		return TestResult{}, err
	}
	t, err := s.openTarget(c)
	if err == nil {
		data := map[string]any{"Channel": c.Name, "Org": org.Name}
		if c.Kind == KindEmail {
			err = s.email(ctx, t, func(string) (string, map[string]any) { return mailpkg.TemplateAlertTest, data })
		} else {
			loc := s.localeFor(t, "")
			text := "🔔 " + s.bundle.T(loc, "notify.test.title", nil) + "\n" + s.bundle.T(loc, "notify.test.body", data)
			err = s.sendChat(ctx, t, text, WebhookPayload{Event: "test", Org: orgJSON(c.OrganizationID, org.Slug, org.Name)})
		}
	}
	res := TestResult{OK: err == nil}
	var se *sendError
	switch {
	case errors.As(err, &se):
		res.Error = se.msg
	case err != nil:
		return TestResult{}, err
	}
	if err := q.SetChannelTest(ctx, store.SetChannelTestParams{ID: id, Ok: &res.OK}); err != nil {
		return TestResult{}, err
	}
	return res, nil
}

// Deliver sends one alert notification (the NotifyArgs worker). Failures are recorded on
// the alert's timeline and returned so River retries; the last attempt gives up.
func (s *Service) Deliver(ctx context.Context, a jobs.NotifyArgs, attempt, maxAttempts int) error {
	q := store.New(s.pool)
	row, err := q.GetAlert(ctx, a.AlertID)
	if database.IsNoRows(err) {
		return nil // deleted with its organization
	}
	if err != nil {
		return err
	}
	c, err := q.GetChannel(ctx, a.ChannelID)
	if database.IsNoRows(err) {
		return nil // the channel was deleted meanwhile
	}
	if err != nil {
		return err
	}
	org, err := q.GetOrgSlugName(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	al := row.Alert
	t, err := s.openTarget(c)
	if err == nil {
		if c.Kind == KindEmail {
			err = s.email(ctx, t, func(loc string) (string, map[string]any) {
				m := s.render(loc, al, org.Slug, a.Event, a.Step)
				summary := m.Summary
				if m.Extra != "" {
					summary += " " + m.Extra
				}
				return mailpkg.TemplateAlert, map[string]any{"Title": m.Title, "Summary": summary, "Severity": m.Severity, "URL": m.URL}
			})
		} else {
			m := s.render(s.localeFor(t, ""), al, org.Slug, a.Event, a.Step)
			var d Details
			_ = json.Unmarshal(al.Details, &d)
			err = s.sendChat(ctx, t, m.Text(), WebhookPayload{
				Event: "alert." + a.Event, Org: orgJSON(c.OrganizationID, org.Slug, org.Name),
				Alert: &WebhookAlert{
					ID: al.ID, Rule: al.RuleName, RuleID: al.RuleID, Kind: string(al.RuleKind), Severity: string(al.Severity),
					Status: string(al.Status), SubjectType: al.SubjectType, SubjectID: al.SubjectID, Subject: al.SubjectName,
					Labels: al.SubjectLabels, Details: d, StartedAt: al.StartedAt, ResolvedAt: al.ResolvedAt, URL: m.URL,
				},
			})
		}
	}
	var se *sendError
	if err != nil && !errors.As(err, &se) {
		return err // a database or template problem: retry without recording
	}
	ev := store.InsertAlertEventParams{AlertID: al.ID, Kind: "notified", ChannelID: &c.ID, ChannelName: c.Name, Detail: a.Event}
	if err != nil {
		ev.Kind = "notify_failed"
		ev.Detail = fmt.Sprintf("%s (attempt %d of %d)", se.msg, attempt, maxAttempts)
	}
	if rerr := q.InsertAlertEvent(ctx, ev); rerr != nil {
		return rerr
	}
	if err != nil && attempt >= maxAttempts {
		return river.JobCancel(err)
	}
	return err
}
