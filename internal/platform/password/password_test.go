package password

import (
	"errors"
	"strings"
	"testing"
)

// Small parameters keep the tests fast; production uses DefaultParams.
var fast = Params{MemoryKiB: 64, Time: 1, Threads: 1}

func newFast(t *testing.T) *Hasher {
	t.Helper()
	h, err := New(fast)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHashAndVerify(t *testing.T) {
	h := newFast(t)
	enc, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected format: %s", enc)
	}

	tests := []struct {
		name  string
		plain string
		want  bool
	}{
		{"correct password", "correct horse battery staple", true},
		{"wrong password", "correct horse battery stapl", false},
		{"empty password", "", false},
		{"different case", "Correct horse battery staple", false},
		{"too long is rejected without hashing", strings.Repeat("a", MaxLength+1), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, rehash, err := h.Verify(tc.plain, enc)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.want {
				t.Errorf("ok = %v, want %v", ok, tc.want)
			}
			if rehash {
				t.Error("same parameters must not ask for a rehash")
			}
		})
	}
}

func TestHashIsSalted(t *testing.T) {
	h := newFast(t)
	a, _ := h.Hash("same")
	b, _ := h.Hash("same")
	if a == b {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestHashRejectsTooLong(t *testing.T) {
	if _, err := newFast(t).Hash(strings.Repeat("a", MaxLength+1)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
}

func TestNeedsRehashWhenParamsChange(t *testing.T) {
	weak, _ := New(Params{MemoryKiB: 32, Time: 1, Threads: 1})
	strong := newFast(t)

	enc, _ := weak.Hash("secret")
	ok, rehash, err := strong.Verify("secret", enc)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !rehash {
		t.Error("a hash made with weaker parameters must be flagged for rehash")
	}
}

func TestVerifyMalformedHash(t *testing.T) {
	h := newFast(t)
	good, _ := h.Hash("x")
	tests := map[string]string{
		"empty":             "",
		"plain text":        "not-a-hash",
		"wrong algorithm":   strings.Replace(good, "argon2id", "argon2i", 1),
		"wrong version":     strings.Replace(good, "v=19", "v=16", 1),
		"missing parts":     "$argon2id$v=19$m=64,t=1,p=1$c2FsdA",
		"bad base64":        "$argon2id$v=19$m=64,t=1,p=1$!!!$!!!",
		"unknown parameter": "$argon2id$v=19$m=64,t=1,x=1$c2FsdHNhbHRzYWx0$" + strings.Repeat("A", 22),
		"memory too large":  "$argon2id$v=19$m=4294967295,t=1,p=1$c2FsdHNhbHRzYWx0$" + strings.Repeat("A", 22),
		"zero iterations":   "$argon2id$v=19$m=64,t=0,p=1$c2FsdHNhbHRzYWx0$" + strings.Repeat("A", 22),
		"threads overflow":  "$argon2id$v=19$m=64,t=1,p=300$c2FsdHNhbHRzYWx0$" + strings.Repeat("A", 22),
	}
	for name, enc := range tests {
		t.Run(name, func(t *testing.T) {
			ok, _, err := h.Verify("x", enc)
			if err == nil || ok {
				t.Errorf("ok=%v err=%v, want an error and ok=false", ok, err)
			}
		})
	}
}

func TestNewValidatesParams(t *testing.T) {
	bad := []Params{
		{MemoryKiB: 0, Time: 1, Threads: 1},
		{MemoryKiB: 64, Time: 0, Threads: 1},
		{MemoryKiB: 64, Time: 1, Threads: 0},
		{MemoryKiB: maxMemoryKiB + 1, Time: 1, Threads: 1},
	}
	for _, p := range bad {
		if _, err := New(p); err == nil {
			t.Errorf("New(%+v) must fail", p)
		}
	}
}

func TestDummyDoesNotPanic(t *testing.T) {
	newFast(t).Dummy("anything")
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name  string
		plain string
		want  error
	}{
		{"long enough", "0123456789", nil},
		{"one short", "012345678", ErrTooShort},
		{"empty", "", ErrTooShort},
		{"counts characters, not bytes", "pässwörd12", nil},
		{"maximum", strings.Repeat("a", MaxLength), nil},
		{"too long", strings.Repeat("a", MaxLength+1), ErrTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.plain); !errors.Is(err, tc.want) {
				t.Errorf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}
