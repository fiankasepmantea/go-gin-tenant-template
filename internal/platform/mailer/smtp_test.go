package mailer

import (
	"bufio"
	"context"
	"io"
	"mime"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func listen() (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
}

// fakeServer is a tiny SMTP server (no TLS) that records what it receives.
type fakeServer struct {
	ln       net.Listener
	got      chan string // the DATA of each message
	rcpts    chan string
	authSeen chan string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := listen()
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, got: make(chan string, 4), rcpts: make(chan string, 8), authSeen: make(chan string, 4)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *fakeServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(line string) { _, _ = io.WriteString(c, line+"\r\n") }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			_, _ = io.WriteString(c, "250-fake\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(cmd, "AUTH"):
			s.authSeen <- strings.TrimSpace(line)
			w("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			s.rcpts <- strings.TrimSpace(line)
			w("250 ok")
		case cmd == "DATA":
			w("354 go")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			s.got <- sb.String()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func newTestSMTP(t *testing.T, s *fakeServer, user string) *SMTP {
	t.Helper()
	m, err := NewSMTP(SMTPConfig{
		Host: "127.0.0.1", Port: s.port(), From: "App <no-reply@example.com>",
		Security: SecurityNone, Username: user, Password: "pw", Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSMTP_SendsAWellFormedMessage(t *testing.T) {
	s := newFakeServer(t)
	m := newTestSMTP(t, s, "")
	err := m.Send(context.Background(), Message{
		To: []string{"alice@example.com"}, Subject: "Atur ulang kata sandi — Café",
		Text: "Halo,\nklik tautan ini: https://x.test/reset?token=abc.def\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := <-s.got
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("not a valid message: %v\n%s", err, raw)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if subj != "Atur ulang kata sandi — Café" {
		t.Errorf("subject = %q", subj)
	}
	if msg.Header.Get("To") != "alice@example.com" || !strings.Contains(msg.Header.Get("From"), "no-reply@example.com") {
		t.Errorf("headers: %v", msg.Header)
	}
	if msg.Header.Get("Message-Id") == "" || msg.Header.Get("Date") == "" || msg.Header.Get("Mime-Version") != "1.0" {
		t.Errorf("missing headers: %v", msg.Header)
	}
	body, _ := io.ReadAll(msg.Body)
	if !strings.Contains(string(body), "token=3Dabc.def") { // quoted-printable escapes "="
		t.Errorf("body = %q", body)
	}
	if got := <-s.rcpts; !strings.Contains(got, "alice@example.com") {
		t.Errorf("RCPT = %q", got)
	}
}

func TestSMTP_AuthIsSentWhenConfigured(t *testing.T) {
	s := newFakeServer(t)
	// no TLS and a password to a non-loopback host is refused at construction
	if _, err := NewSMTP(SMTPConfig{Host: "mail.example.com", Port: 25, From: "a@b.test", Security: SecurityNone, Username: "u", Password: "p"}); err == nil {
		t.Fatal("must refuse a password without encryption")
	}
	m := newTestSMTP(t, s, "mailer")
	if err := m.Send(context.Background(), Message{To: []string{"a@example.com"}, Subject: "s", Text: "t"}); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-s.authSeen:
		if !strings.HasPrefix(a, "AUTH PLAIN") {
			t.Errorf("auth = %q", a)
		}
	default:
		t.Fatal("no AUTH command was sent")
	}
}

func TestMessageValidation(t *testing.T) {
	bad := []Message{
		{Subject: "s"},
		{To: []string{"a@example.com\r\nBcc: evil@example.com"}, Subject: "s"},
		{To: []string{"Alice <a@example.com>"}, Subject: "s"},
		{To: []string{"not-an-address"}, Subject: "s"},
		{To: []string{"a@example.com"}, Subject: "hi\r\nBcc: evil@example.com"},
		{To: []string{"a@example.com"}, Subject: "  "},
	}
	for i, m := range bad {
		if err := m.Validate(); err == nil {
			t.Errorf("case %d must be refused: %+v", i, m)
		}
	}
	if err := (Message{To: []string{"a@example.com"}, Subject: "ok"}).Validate(); err != nil {
		t.Errorf("valid message refused: %v", err)
	}
}

func TestNewSMTP_Validation(t *testing.T) {
	ok := SMTPConfig{Host: "smtp.example.com", Port: 587, From: "no-reply@example.com"}
	if _, err := NewSMTP(ok); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*SMTPConfig){
		"no host":      func(c *SMTPConfig) { c.Host = "" },
		"bad port":     func(c *SMTPConfig) { c.Port = 0 },
		"bad from":     func(c *SMTPConfig) { c.From = "nope" },
		"from newline": func(c *SMTPConfig) { c.From = "a@b.test\r\nBcc: x@y.test" },
		"bad security": func(c *SMTPConfig) { c.Security = "ssl3" },
	} {
		c := ok
		mut(&c)
		if _, err := NewSMTP(c); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestSMTP_ConnectionRefusedAndTimeout(t *testing.T) {
	ln, _ := listen()
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	m, _ := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: port, From: "a@b.test", Security: SecurityNone, Timeout: time.Second})
	if err := m.Send(context.Background(), Message{To: []string{"a@example.com"}, Subject: "s"}); err == nil {
		t.Fatal("want a connection error")
	}

	// a server that accepts and never speaks
	silent, _ := listen()
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	m2, _ := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: silent.Addr().(*net.TCPAddr).Port, From: "a@b.test", Security: SecurityNone, Timeout: 300 * time.Millisecond})
	start := time.Now()
	if err := m2.Send(context.Background(), Message{To: []string{"a@example.com"}, Subject: "s"}); err == nil {
		t.Fatal("want a timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the timeout was not honored: %v", time.Since(start))
	}
}
