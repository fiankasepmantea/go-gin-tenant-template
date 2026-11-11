package session

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/mailer"
)

// ResetNotice is what a user needs to finish a password reset.
type ResetNotice struct {
	Email      string
	Name       string
	TenantCode string // empty for a platform admin
	Token      string
	ExpiresAt  time.Time
}

// Notifier delivers the reset token to the user. MailNotifier is the SMTP implementation; a project may replace
// it (SMS, push). It must never log the token in production.
type Notifier interface {
	PasswordReset(ctx context.Context, n ResetNotice) error
}

// MailNotifier sends the reset e-mail.
type MailNotifier struct {
	mail    mailer.Mailer
	appName string
	linkTpl string
	now     func() time.Time
}

// NewMailNotifier creates the notifier. linkTemplate is the page of your front end that takes the token, with the
// placeholders {token} and {tenant}, for example "https://{tenant}.example.com/reset-password?token={token}".
// Empty means the e-mail contains the token itself, to paste in the app.
func NewMailNotifier(m mailer.Mailer, appName, linkTemplate string) (*MailNotifier, error) {
	if m == nil {
		return nil, errors.New("session: a mailer is required")
	}
	if strings.TrimSpace(appName) == "" {
		appName = "the application"
	}
	if linkTemplate != "" {
		u, err := url.Parse(strings.NewReplacer("{token}", "x", "{tenant}", "x").Replace(linkTemplate))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, errors.New("session: the reset link template must be an http(s) URL")
		}
	}
	return &MailNotifier{mail: m, appName: appName, linkTpl: linkTemplate, now: time.Now}, nil
}

func (n *MailNotifier) PasswordReset(ctx context.Context, in ResetNotice) error {
	minutes := int(in.ExpiresAt.Sub(n.now()).Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	var how, what string
	if n.linkTpl != "" {
		what = "link"
		link := strings.NewReplacer(
			"{token}", url.QueryEscape(in.Token),
			"{tenant}", url.PathEscape(in.TenantCode),
		).Replace(n.linkTpl)
		how = "Open this link to choose a new password:\n\n" + link
	} else {
		what = "code"
		how = "Use this code in " + n.appName + " to choose a new password:\n\n" + in.Token
	}
	text := fmt.Sprintf(`Hello %s,

Someone asked to reset the password of your account at %s.

%s

The %s works once and expires in %d minutes. If you did not ask for this, ignore this message:
your password stays as it is.
`, displayName(in), n.appName, how, what, minutes)

	return n.mail.Send(ctx, mailer.Message{
		To:      []string{in.Email},
		Subject: "Reset your password — " + n.appName,
		Text:    text,
	})
}

func displayName(in ResetNotice) string {
	if s := strings.TrimSpace(in.Name); s != "" && !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return "there"
}
