package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/mailer"
)

type captureMailer struct{ got []mailer.Message }

func (c *captureMailer) Send(_ context.Context, m mailer.Message) error {
	c.got = append(c.got, m)
	return nil
}

// testToken is built at run time: a literal that looks like a random token is reported by secret scanners.
var testToken = "tid." + strings.Repeat("x", 12)

func notice() ResetNotice {
	return ResetNotice{Email: "alice@a.test", Name: "Alice", TenantCode: "tokoa", Token: testToken, ExpiresAt: time.Now().Add(30 * time.Minute)}
}

func TestMailNotifier_LinkTemplate(t *testing.T) {
	m := &captureMailer{}
	n, err := NewMailNotifier(m, "Toko", "https://{tenant}.example.com/reset?token={token}")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.PasswordReset(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	msg := m.got[0]
	if len(msg.To) != 1 || msg.To[0] != "alice@a.test" || !strings.Contains(msg.Subject, "Toko") {
		t.Errorf("message = %+v", msg)
	}
	for _, want := range []string{"https://tokoa.example.com/reset?token=" + testToken, "Hello Alice", "30 minutes", "ignore this message"} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("body lacks %q:\n%s", want, msg.Text)
		}
	}
}

func TestMailNotifier_WithoutLinkTheTokenIsInTheBody(t *testing.T) {
	m := &captureMailer{}
	n, _ := NewMailNotifier(m, "", "")
	if err := n.PasswordReset(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.got[0].Text, testToken) || !strings.Contains(m.got[0].Text, "the application") {
		t.Errorf("body:\n%s", m.got[0].Text)
	}
}

func TestMailNotifier_EscapesAndRefusesBadInput(t *testing.T) {
	m := &captureMailer{}
	n, _ := NewMailNotifier(m, "Toko", "https://app.example.com/reset?token={token}")
	in := notice()
	in.Token = "a&b=c d"
	in.Name = "Evil\r\nBcc: x@y.test"
	if err := n.PasswordReset(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.got[0].Text, "token=a%26b%3Dc+d") {
		t.Errorf("the token must be URL-escaped:\n%s", m.got[0].Text)
	}
	if strings.Contains(m.got[0].Text, "Evil") {
		t.Errorf("a name with line breaks must not reach the body:\n%s", m.got[0].Text)
	}

	for _, tpl := range []string{"javascript:alert(1)", "ftp://x.test/{token}", "/relative?{token}", "https://"} {
		if _, err := NewMailNotifier(m, "Toko", tpl); err == nil {
			t.Errorf("template %q must be refused", tpl)
		}
	}
	if _, err := NewMailNotifier(nil, "Toko", ""); err == nil {
		t.Error("a mailer is required")
	}
}
