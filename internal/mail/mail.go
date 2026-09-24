// Package mail renders localized transactional emails and delivers them over SMTP.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/i18n"
)

// Message is a rendered email.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Templates known to the renderer. Each has i18n messages email.<name>.{subject,heading,body[,button]}.
const (
	TemplateVerifyEmail       = "verify_email"
	TemplateResetPassword     = "reset_password"
	TemplatePasswordChanged   = "password_changed"
	TemplateAccountExists     = "account_exists"
	TemplateTwoFactorEnabled  = "two_factor_enabled"
	TemplateTwoFactorDisabled = "two_factor_disabled"
)

// Templates lists every template (used by tests to check both locales render).
var Templates = []string{TemplateVerifyEmail, TemplateResetPassword, TemplatePasswordChanged, TemplateAccountExists, TemplateTwoFactorEnabled, TemplateTwoFactorDisabled}

//go:embed templates/*
var templateFS embed.FS

var (
	htmlLayout = htmltemplate.Must(htmltemplate.ParseFS(templateFS, "templates/layout.html"))
	textLayout = texttemplate.Must(texttemplate.ParseFS(templateFS, "templates/layout.txt"))
)

// Renderer produces localized messages from the i18n bundle.
type Renderer struct {
	Bundle *i18n.Bundle
}

type layoutData struct {
	Lang, Subject, Greeting, Heading, Body, Button, URL, LinkFallback, Footer string
}

// Render builds the message for template name in locale. data may contain "Name" (the
// recipient's display name), "URL" (the call-to-action link) and template-specific keys.
func (r *Renderer) Render(name, locale string, data map[string]any) (Message, error) {
	loc := i18n.Normalize(locale)
	if loc == "" {
		loc = i18n.Fallback
	}
	msg := func(key string) (string, bool) {
		return r.Bundle.Lookup(loc, "email."+name+"."+key, data)
	}
	subject, ok := msg("subject")
	if !ok {
		return Message{}, fmt.Errorf("mail: unknown template %q", name)
	}
	heading, _ := msg("heading")
	body, _ := msg("body")
	button, _ := msg("button")
	d := layoutData{Lang: loc, Subject: subject, Heading: heading, Body: body}
	if n, _ := data["Name"].(string); n != "" {
		d.Greeting, _ = r.Bundle.Lookup(loc, "email.greeting", data)
	}
	if u, _ := data["URL"].(string); u != "" {
		d.URL, d.Button = u, button
		d.LinkFallback, _ = r.Bundle.Lookup(loc, "email.link_fallback", nil)
	}
	d.Footer, _ = r.Bundle.Lookup(loc, "email.footer", nil)

	var html, text bytes.Buffer
	if err := htmlLayout.Execute(&html, d); err != nil {
		return Message{}, fmt.Errorf("mail: render html: %w", err)
	}
	if err := textLayout.Execute(&text, d); err != nil {
		return Message{}, fmt.Errorf("mail: render text: %w", err)
	}
	return Message{Subject: subject, Text: text.String(), HTML: html.String()}, nil
}

// SMTPSender sends mail through an SMTP relay.
type SMTPSender struct {
	Config config.SMTPConfig
	// Timeout bounds the whole SMTP conversation (default 30s).
	Timeout time.Duration
}

func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	from, err := mail.ParseAddress(s.Config.From)
	if err != nil {
		return fmt.Errorf("mail: from address: %w", err)
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("mail: recipient address: %w", err)
	}
	raw, err := BuildMIME(from, to, m, time.Now())
	if err != nil {
		return err
	}

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr := net.JoinHostPort(s.Config.Host, strconv.Itoa(s.Config.Port))
	tlsCfg := &tls.Config{ServerName: s.Config.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	dialer := &net.Dialer{}
	if s.Config.TLSMode == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: connect %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, s.Config.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: smtp handshake: %w", err)
	}
	defer func() { _ = c.Close() }()

	if s.Config.TLSMode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("mail: server does not support STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}
	if s.Config.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Config.Username, s.Config.Password, s.Config.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: end DATA: %w", err)
	}
	return c.Quit()
}

// BuildMIME renders a multipart/alternative UTF-8 message. Header values come from parsed
// addresses and RFC 2047-encoded subjects, so they cannot inject headers.
func BuildMIME(from, to *mail.Address, m Message, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	domain := "opshub.local"
	if _, d, ok := strings.Cut(from.Address, "@"); ok {
		domain = d
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)

	h := []string{
		"From: " + from.String(),
		"To: " + to.String(),
		"Subject: " + mime.BEncoding.Encode("UTF-8", strings.ReplaceAll(m.Subject, "\n", " ")),
		"Date: " + now.Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(id) + "@" + domain + ">",
		"MIME-Version: 1.0",
		"Auto-Submitted: auto-generated",
		`Content-Type: multipart/alternative; boundary="` + mw.Boundary() + `"`,
	}
	var out bytes.Buffer
	out.WriteString(strings.Join(h, "\r\n") + "\r\n\r\n")

	for _, part := range []struct{ ctype, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype + "; charset=UTF-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(pw)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	out.Write(buf.Bytes())
	return out.Bytes(), nil
}
