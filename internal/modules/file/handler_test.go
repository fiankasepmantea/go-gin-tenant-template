package file

import (
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"photo.png":              "photo.png",
		`C:\Users\me\photo.png`:  "photo.png",
		"../../etc/passwd":       "passwd",
		"a/b/":                   "file",
		"":                       "file",
		"..":                     "file",
		"  spaced name.pdf  ":    "spaced name.pdf",
		"bad\x00\r\nname.png":    "badname.png",
		"laporan akhir é 日本.pdf": "laporan akhir é 日本.pdf",
	}
	for in, want := range cases {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanName(strings.Repeat("a", 400)); len([]rune(got)) != maxNameRunes {
		t.Errorf("long name has %d characters", len([]rune(got)))
	}
}

func TestNewHandler_Defaults(t *testing.T) {
	h := NewHandler(nil, nil, Options{})
	if h.max != DefaultMaxBytes || !h.allowed["application/pdf"] || h.allowed["image/svg+xml"] {
		t.Fatalf("defaults: max=%d allowed=%v", h.max, h.allowed)
	}
}
