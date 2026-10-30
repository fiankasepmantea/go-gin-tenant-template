package logger

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		" DEBUG ": slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range tests {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("an unknown level should error")
	}
}

func TestNew_Format(t *testing.T) {
	var prod bytes.Buffer
	l, err := New(&prod, "info", true)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hello", "k", "v")
	if !strings.HasPrefix(prod.String(), "{") || !strings.Contains(prod.String(), `"msg":"hello"`) {
		t.Errorf("production must be JSON: %q", prod.String())
	}

	var dev bytes.Buffer
	l, _ = New(&dev, "info", false)
	l.Info("hello")
	if strings.HasPrefix(dev.String(), "{") {
		t.Errorf("development must be text: %q", dev.String())
	}
}

func TestNew_LevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l, _ := New(&buf, "warn", true)
	l.Info("ignored")
	l.Warn("shown")
	if strings.Contains(buf.String(), "ignored") || !strings.Contains(buf.String(), "shown") {
		t.Errorf("wrong level filter: %q", buf.String())
	}
}
