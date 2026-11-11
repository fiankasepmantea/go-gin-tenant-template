package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/ratelimit"
)

// limitedFixture is the normal fixture with a rate limiter (3 attempts per minute) on the same fakes.
func limitedFixture(t *testing.T) (*fixture, *time.Time) {
	t.Helper()
	f := newFixture(t)
	now := f.now
	store := ratelimit.NewMemoryStore(func() time.Time { return now })
	svc, err := NewService(Deps{
		Tx:      func(ctx context.Context, _ string, fn func(context.Context) error) error { return fn(ctx) },
		Tenants: fakeTenants{"tokoa": f.tenantA, "tokob": f.tenantB}, Users: f.users, Tokens: f.tokens,
		Hasher: f.hasher, Issuer: f.mgr, AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour,
		Limiter: ratelimit.New(store, nil), Limits: Limits{Attempts: 3, Window: time.Minute},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f, &now
}

func attempt(f *fixture, user, pw, ip string) error {
	_, err := f.svc.Login(context.Background(), LoginInput{TenantCode: "tokoa", Username: user, Password: pw, IP: ip})
	return err
}

func TestLogin_RateLimitedAfterFailures(t *testing.T) {
	f, now := limitedFixture(t)

	for i := 0; i < 3; i++ {
		if err := attempt(f, "alice", "wrong", "10.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// the correct password is refused too: the lock does not look at the password
	err := attempt(f, "alice", alicePassword, "10.0.0.1")
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 || rl.RetryAfter > time.Minute {
		t.Fatalf("want *RateLimitedError, got %v", err)
	}
	if got := f.tokens.active(f.alice.ID); got != 0 {
		t.Fatalf("a blocked login must not create a session: %d", got)
	}

	// another address is not affected by the (user, IP) counter
	if err := attempt(f, "alice", alicePassword, "10.0.0.2"); err != nil {
		t.Fatalf("other IP: %v", err)
	}

	// the window ends: the first address may try again
	*now = now.Add(61 * time.Second)
	if err := attempt(f, "alice", alicePassword, "10.0.0.1"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestLogin_SuccessResetsOnlyTheAccountCounter(t *testing.T) {
	f, _ := limitedFixture(t)
	// two failures for alice from one address, then a success: alice's counter starts again
	_ = attempt(f, "alice", "wrong", "10.0.0.1")
	_ = attempt(f, "alice", "wrong", "10.0.0.1")
	if err := attempt(f, "alice", alicePassword, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_ = attempt(f, "alice", "wrong", "10.0.0.1")
	}
	if err := attempt(f, "alice", alicePassword, "10.0.0.1"); err != nil {
		t.Fatalf("the counter must have been reset by the success: %v", err)
	}
}

func TestLogin_OneAddressGuessingManyUsersIsStopped(t *testing.T) {
	f, _ := limitedFixture(t)
	// per-IP limit is 6 x 3 = 18 failures, whatever usernames are tried
	var last error
	for i := 0; i < 20; i++ {
		last = attempt(f, "user"+string(rune('a'+i)), "wrong", "10.9.9.9")
	}
	var rl *RateLimitedError
	if !errors.As(last, &rl) {
		t.Fatalf("20 failures from one address must end in a block, got %v", last)
	}
	// a success from the same address does NOT clear the address counter
	_ = attempt(f, "alice", alicePassword, "10.9.9.9")
	if err := attempt(f, "alice", "wrong", "10.9.9.9"); !errors.As(err, &rl) {
		t.Fatalf("the address stays blocked: %v", err)
	}
}

func TestLogin_UnknownTenantAndUserCountToo(t *testing.T) {
	f, _ := limitedFixture(t)
	for i := 0; i < 3; i++ {
		_, _ = f.svc.Login(context.Background(), LoginInput{TenantCode: "nope", Username: "ghost", Password: "x", IP: "10.0.0.3"})
	}
	_, err := f.svc.Login(context.Background(), LoginInput{TenantCode: "nope", Username: "ghost", Password: "x", IP: "10.0.0.3"})
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("an unknown user must be limited like a known one (no way to tell them apart): %v", err)
	}
}

func TestLoginPlatform_IsLimitedSeparately(t *testing.T) {
	f, _ := limitedFixture(t)
	for i := 0; i < 3; i++ {
		_, _ = f.svc.LoginPlatform(context.Background(), LoginInput{Username: "root", Password: "wrong", IP: "10.0.0.5"})
	}
	var rl *RateLimitedError
	if _, err := f.svc.LoginPlatform(context.Background(), LoginInput{Username: "root", Password: adminPassword, IP: "10.0.0.5"}); !errors.As(err, &rl) {
		t.Fatalf("platform login must be limited: %v", err)
	}
	// the tenant login of the same address is a different counter
	if err := attempt(f, "alice", alicePassword, "10.0.0.5"); err != nil {
		t.Fatalf("tenant login from the same address: %v", err)
	}
}

func TestLogin_WithoutLimiterNeverLimits(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 50; i++ {
		_ = attempt(f, "alice", "wrong", "10.0.0.1")
	}
	if err := attempt(f, "alice", alicePassword, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
}
