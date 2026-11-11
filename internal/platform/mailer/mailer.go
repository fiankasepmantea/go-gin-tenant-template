// Package mailer sends plain-text e-mail. SMTP is the production implementation; LogMailer is for development.
//
// Only what the template needs is here (transactional mail such as password reset): one recipient list, a
// subject and a text body. For HTML, attachments or a provider API (SES, Resend, ...) implement Mailer.
package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
)

// Message is one e-mail.
type Message struct {
	To      []string // plain addresses, "user@example.com"
	Subject string
	Text    string
}

// Mailer sends messages. Send must be safe for concurrent use.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// Validate checks the message. Header injection (CR or LF in a header value) is refused here, for every
// implementation.
func (m Message) Validate() error {
	if len(m.To) == 0 {
		return errors.New("mailer: no recipient")
	}
	if len(m.To) > 50 {
		return errors.New("mailer: too many recipients")
	}
	for _, a := range m.To {
		if err := validAddress(a); err != nil {
			return err
		}
	}
	if strings.TrimSpace(m.Subject) == "" || strings.ContainsAny(m.Subject, "\r\n") {
		return errors.New("mailer: invalid subject")
	}
	return nil
}

func validAddress(a string) error {
	if strings.ContainsAny(a, "\r\n") {
		return fmt.Errorf("mailer: invalid address %q", a)
	}
	p, err := mail.ParseAddress(a)
	if err != nil || p.Name != "" || p.Address != a {
		return fmt.Errorf("mailer: invalid address %q", a)
	}
	return nil
}

// LogMailer writes the message to the log instead of sending it. With ShowBody it logs the text too, which
// is how a developer reads a reset link without an SMTP server. Never enable ShowBody in production: a reset
// link in a log is a way to take over an account.
type LogMailer struct {
	Log      *slog.Logger
	ShowBody bool
}

func (l LogMailer) Send(ctx context.Context, m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	attrs := []any{"to", strings.Join(m.To, ","), "subject", m.Subject}
	if l.ShowBody {
		attrs = append(attrs, "body", m.Text)
	}
	log.InfoContext(ctx, "mail not sent (no SMTP configured)", attrs...)
	return nil
}
