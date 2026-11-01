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

func TestReadPassword(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"with newline", "s3cret-password\n", "s3cret-password", false},
		{"windows newline", "s3cret-password\r\n", "s3cret-password", false},
		{"no newline at end", "s3cret-password", "s3cret-password", false},
		{"only the first line", "first\nsecond\n", "first", false},
		{"keeps inner and edge spaces", " a b \n", " a b ", false},
		{"empty stdin", "", "", true},
		{"only a newline", "\n", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readPassword(strings.NewReader(tc.in))
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("readPassword(%q) = (%q, %v), want (%q, err=%v)", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestProvisionCommandsRequireFlags(t *testing.T) {
	// No database is touched: the commands fail on validation before connecting.
	openProvisioner = func() (*Provisioner, func(), error) {
		t.Fatal("must not connect to the database")
		return nil, nil, nil
	}
	for _, args := range [][]string{
		{"tenant", "create"},
		{"tenant", "create", "--code", "acme"},
		{"user", "create", "--tenant", "acme", "--username", "u", "--email", "a@b.c", "--name", "N"}, // no --password-stdin
		{"admin", "create", "--username", "u", "--email", "a@b.c", "--name", "N"},                    // no --password-stdin
	} {
		cmd := NewRootCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}
