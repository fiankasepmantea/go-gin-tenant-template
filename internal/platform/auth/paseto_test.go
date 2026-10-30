package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(GenerateKeyHex(), "test-issuer", 15*time.Minute)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return m
}

func TestIssueAndParse_RoundTrip(t *testing.T) {
	m := newTestManager(t)

	tests := []Claims{
		{UserID: "u-1", TenantID: "t-1", TokenID: "tok-1", Level: LevelTenant},
		{UserID: "u-2", TenantID: "", TokenID: "tok-2", Level: LevelPlatform},
	}
	for _, want := range tests {
		token, err := m.Issue(want)
		if err != nil {
			t.Fatalf("Issue(%+v) error = %v", want, err)
		}
		if !strings.HasPrefix(token, "v4.local.") {
			t.Fatalf("token must start with v4.local., got %q", token[:12])
		}
		got, err := m.Parse(token)
		if err != nil {
			t.Fatalf("Parse() error = %v", err)
		}
		if *got != want {
			t.Errorf("claims = %+v, want %+v", *got, want)
		}
	}
}

func TestIssue_RejectsInvalidClaims(t *testing.T) {
	m := newTestManager(t)

	tests := map[string]Claims{
		"no user":                  {TokenID: "x", Level: LevelPlatform},
		"no token id":              {UserID: "u", Level: LevelPlatform},
		"tenant without tenant id": {UserID: "u", TokenID: "x", Level: LevelTenant},
		"unknown level":            {UserID: "u", TokenID: "x", Level: "root"},
	}
	for name, c := range tests {
		if _, err := m.Issue(c); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParse_Rejects(t *testing.T) {
	m := newTestManager(t)
	claims := Claims{UserID: "u", TenantID: "t", TokenID: "x", Level: LevelTenant}
	token, err := m.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("different key", func(t *testing.T) {
		other := newTestManager(t)
		if _, err := other.Parse(token); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("tampered token", func(t *testing.T) {
		tampered := token[:len(token)-2] + "AA"
		if _, err := m.Parse(tampered); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("garbage", func(t *testing.T) {
		if _, err := m.Parse("not-a-token"); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		m.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
		defer func() { m.now = time.Now }()
		if _, err := m.Parse(token); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("different issuer", func(t *testing.T) {
		other := *m
		other.issuer = "someone-else"
		if _, err := other.Parse(token); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestNewManager_Invalid(t *testing.T) {
	if _, err := NewManager("not-hex", "i", time.Minute); err == nil {
		t.Error("an invalid key should error")
	}
	if _, err := NewManager(GenerateKeyHex(), "i", 0); err == nil {
		t.Error("a ttl of 0 should error")
	}
}
