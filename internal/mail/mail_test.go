package mail

import (
	"bufio"
	"context"
	"mime"
	"net"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/i18n"
)

func renderer(t *testing.T) *Renderer {
	t.Helper()
	b, err := i18n.NewBundle()
	require.NoError(t, err)
	return &Renderer{Bundle: b}
}

func TestRenderAllTemplatesInBothLocales(t *testing.T) {
	r := renderer(t)
	for _, name := range Templates {
		for _, loc := range []string{"en", "km"} {
			m, err := r.Render(name, loc, map[string]any{"Name": "Dara", "URL": "https://ops.example.com/x?token=abc", "Org": "Angkor Tech", "Inviter": "Sokha", "Role": "Developer"})
			require.NoError(t, err, name+"/"+loc)
			assert.NotEmpty(t, m.Subject)
			assert.NotContains(t, m.Subject, "email.", "untranslated key in %s/%s", name, loc)
			assert.Contains(t, m.HTML, `lang="`+loc+`"`)
			assert.Contains(t, m.Text, "https://ops.example.com/x?token=abc")
		}
	}
}

func TestRenderKhmerAndEscaping(t *testing.T) {
	r := renderer(t)
	m, err := r.Render(TemplateVerifyEmail, "km-KH", map[string]any{"Name": `<script>alert(1)</script>`, "URL": "https://ops.example.com/verify?t=1"})
	require.NoError(t, err)
	assert.Equal(t, "បញ្ជាក់អាសយដ្ឋានអ៊ីមែលរបស់អ្នក", m.Subject)
	assert.NotContains(t, m.HTML, "<script>", "display names are HTML-escaped")
	assert.Contains(t, m.HTML, "line-height:1.8", "Khmer gets taller lines")

	_, err = r.Render("nope", "en", nil)
	assert.Error(t, err)
}

func TestBuildMIMEEncodesHeaders(t *testing.T) {
	from, _ := netmail.ParseAddress("OpsHub <noreply@ops.example.com>")
	to, _ := netmail.ParseAddress("dara@example.com")
	raw, err := BuildMIME(from, to, Message{Subject: "បញ្ជាក់\r\nBcc: evil@example.com", Text: "សួស្តី", HTML: "<p>សួស្តី</p>"}, time.Unix(0, 0))
	require.NoError(t, err)

	msg, err := netmail.ReadMessage(strings.NewReader(string(raw)))
	require.NoError(t, err)
	assert.Empty(t, msg.Header.Get("Bcc"), "subject newlines cannot inject headers")
	subject, err := new(mimeDecoder).DecodeHeader(msg.Header.Get("Subject"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(subject, "បញ្ជាក់"))
	assert.Contains(t, msg.Header.Get("Content-Type"), "multipart/alternative")
	assert.Contains(t, msg.Header.Get("Message-ID"), "@ops.example.com>")
}

// fakeSMTP accepts one message and returns its DATA section.
func fakeSMTP(t *testing.T) (port int, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
		reply := func(s string) { _, _ = rw.WriteString(s + "\r\n"); _ = rw.Flush() }
		reply("220 fake ESMTP")
		for {
			line, err := rw.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				reply("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				reply("250 ok")
			case cmd == "DATA":
				reply("354 go ahead")
				var sb strings.Builder
				for {
					l, err := rw.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					sb.WriteString(l)
				}
				got <- sb.String()
				reply("250 queued")
			case cmd == "QUIT":
				reply("221 bye")
				return
			default:
				reply("502 unknown")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}

func TestSMTPSenderPlain(t *testing.T) {
	port, got := fakeSMTP(t)
	s := &SMTPSender{Config: config.SMTPConfig{Host: "127.0.0.1", Port: port, From: "OpsHub <noreply@ops.example.com>", TLSMode: "none"}, Timeout: 5 * time.Second}
	err := s.Send(context.Background(), Message{To: "dara@example.com", Subject: "Hi", Text: "hello", HTML: "<p>hello</p>"})
	require.NoError(t, err)
	select {
	case data := <-got:
		assert.Contains(t, data, "To: <dara@example.com>")
		assert.Contains(t, data, "hello")
	case <-time.After(5 * time.Second):
		t.Fatal("no message received")
	}
}

func TestSMTPSenderRequiresSTARTTLSWhenConfigured(t *testing.T) {
	port, _ := fakeSMTP(t)
	s := &SMTPSender{Config: config.SMTPConfig{Host: "127.0.0.1", Port: port, From: "a@b.co", TLSMode: "starttls"}, Timeout: 5 * time.Second}
	err := s.Send(context.Background(), Message{To: "dara@example.com", Subject: "Hi"})
	assert.ErrorContains(t, err, "STARTTLS")
}

type mimeDecoder = mime.WordDecoder
