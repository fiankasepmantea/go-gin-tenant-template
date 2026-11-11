package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Security is how the connection to the SMTP server is protected.
type Security string

const (
	SecurityStartTLS Security = "starttls" // plain connection upgraded with STARTTLS (port 587)
	SecurityTLS      Security = "tls"      // TLS from the first byte (port 465)
	SecurityNone     Security = "none"     // no encryption: only for a local test server such as Mailpit
)

// SMTPConfig is the connection to the SMTP server.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string // empty = no authentication
	Password string
	From     string   // "no-reply@example.com" or "App <no-reply@example.com>"
	Security Security // default starttls
	Timeout  time.Duration
	// LocalName is announced in EHLO. Default "localhost".
	LocalName string
}

// SMTP sends mail through an SMTP server. It opens one connection per message, which is right for the low
// volume of transactional mail; use a provider API or a queue for bulk mail.
type SMTP struct {
	cfg  SMTPConfig
	from *mail.Address
	// now is replaced in tests.
	now func() time.Time
}

// NewSMTP validates the configuration.
func NewSMTP(cfg SMTPConfig) (*SMTP, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("mailer: SMTP host is required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, errors.New("mailer: SMTP port is not valid")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil || strings.ContainsAny(cfg.From, "\r\n") {
		return nil, errors.New("mailer: SMTP from address is not valid")
	}
	switch cfg.Security {
	case "":
		cfg.Security = SecurityStartTLS
	case SecurityStartTLS, SecurityTLS, SecurityNone:
	default:
		return nil, fmt.Errorf("mailer: unknown security %q (starttls|tls|none)", cfg.Security)
	}
	if cfg.Username != "" && cfg.Security == SecurityNone && !isLoopback(cfg.Host) {
		return nil, errors.New("mailer: refusing to send an SMTP password without encryption")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.LocalName == "" {
		cfg.LocalName = "localhost"
	}
	return &SMTP{cfg: cfg, from: from, now: time.Now}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Send delivers the message. It honors ctx and the configured timeout.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	body, err := s.build(m)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	c, err := s.connect(ctx)
	if err != nil {
		return err
	}
	// Quit politely on success; Close always, so a failed session never leaks the connection.
	defer func() { _ = c.Close() }()

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("mailer: smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("mailer: smtp MAIL FROM: %w", err)
	}
	for _, to := range m.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("mailer: smtp RCPT TO: %w", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mailer: smtp DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("mailer: smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: smtp end of data: %w", err)
	}
	return c.Quit()
}

func (s *SMTP) connect(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mailer: connect %s: %w", addr, err)
	}
	// One deadline for the whole conversation, so a server that stops answering cannot hang a request.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}

	if s.cfg.Security == SecurityTLS {
		conn = tls.Client(conn, tlsCfg)
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mailer: smtp greeting: %w", err)
	}
	if err := c.Hello(s.cfg.LocalName); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mailer: smtp EHLO: %w", err)
	}
	if s.cfg.Security == SecurityStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			_ = c.Close()
			// Never fall back to plain text: that would send the password (and the reset link) in the clear.
			return nil, errors.New("mailer: the server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("mailer: smtp STARTTLS: %w", err)
		}
	}
	return c, nil
}

// build creates the RFC 5322 message: UTF-8 text, quoted-printable, headers encoded.
func (s *SMTP) build(m Message) ([]byte, error) {
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("mailer: message id: %w", err)
	}
	domain := "localhost"
	if at := strings.LastIndex(s.from.Address, "@"); at >= 0 {
		domain = s.from.Address[at+1:]
	}

	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", s.from.String())
	h("To", strings.Join(m.To, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", s.now().UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="utf-8"`)
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")

	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(m.Text, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
