package mailer

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestLogMailer_BodyOnlyWhenAsked(t *testing.T) {
	m := Message{To: []string{"a@example.com"}, Subject: "Reset", Text: "secret-link-123"}
	for _, show := range []bool{false, true} {
		var buf bytes.Buffer
		l := LogMailer{Log: slog.New(slog.NewTextHandler(&buf, nil)), ShowBody: show}
		if err := l.Send(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(buf.String(), "secret-link-123"); got != show {
			t.Errorf("ShowBody=%v: body logged = %v\n%s", show, got, buf.String())
		}
	}
	if err := (LogMailer{}).Send(context.Background(), Message{Subject: "x"}); err == nil {
		t.Error("an invalid message must be refused")
	}
}
