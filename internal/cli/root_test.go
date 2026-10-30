package cli

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimSpace(out.String())
}

func TestVersion(t *testing.T) {
	if got := run(t, "version"); !strings.HasPrefix(got, "version=") {
		t.Errorf("output = %q", got)
	}
}

func TestKeygen(t *testing.T) {
	key := run(t, "keygen")
	b, err := hex.DecodeString(key)
	if err != nil || len(b) != 32 {
		t.Fatalf("key must be 32 hex bytes, got %q (%v)", key, err)
	}
	if other := run(t, "keygen"); other == key {
		t.Error("each keygen must produce a different key")
	}
}
