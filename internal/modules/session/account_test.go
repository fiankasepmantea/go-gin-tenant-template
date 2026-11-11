package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/ratelimit"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// ---- more fakes ----

func (f *fakeUsers) FindByLogin(ctx context.Context, identifier string) (*user.User, error) {
	id, _ := tenantctx.IDFrom(ctx)
	for _, u := range f.byID {
		if u.TenantID != nil && *u.TenantID == id &&
			(strings.EqualFold(u.Username, identifier) || strings.EqualFold(u.Email, identifier)) {
			return u, nil
		}
	}
	return nil, core.ErrNotFound
}

func (f *fakeUsers) FindPlatformByLogin(_ context.Context, identifier string) (*user.User, error) {
	for _, u := range f.byID {
		if u.TenantID == nil && (strings.EqualFold(u.Username, identifier) || strings.EqualFold(u.Email, identifier)) {
			return u, nil
		}
	}
	return nil, core.ErrNotFound
}

type fakeResets struct {
	mu   sync.Mutex
	rows map[string]*PasswordReset
}

func (f *fakeResets) Create(_ context.Context, p *PasswordReset) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *p
	f.rows[p.ID] = &cp
	return nil
}

func (f *fakeResets) FindByHash(_ context.Context, hash []byte) (*PasswordReset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if string(r.TokenHash) == string(hash) {
			cp := *r
			return &cp, nil
		}
	}
	return nil, core.ErrNotFound
}

func (f *fakeResets) Consume(_ context.Context, id string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	if r == nil || r.UsedAt != nil || !at.Before(r.ExpiresAt) {
		return false, nil
	}
	r.UsedAt = &at
	return true, nil
}

func (f *fakeResets) InvalidateForUser(_ context.Context, userID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.UserID == userID && r.UsedAt == nil {
			r.UsedAt = &at
		}
	}
	return nil
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent []ResetNotice
	err  error
}

func (f *fakeNotifier) PasswordReset(_ context.Context, n ResetNotice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	return f.err
}

func (f *fakeNotifier) last(t *testing.T) ResetNotice {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("no message was sent")
	}
	return f.sent[len(f.sent)-1]
}

type accountFixture struct {
	*fixture
	acct   *Account
	resets *fakeResets
	notify *fakeNotifier
	clock  *time.Time
}

func newAccountFixture(t *testing.T, withLimiter bool) *accountFixture {
	t.Helper()
	f := newFixture(t)
	f.alice.Email = "alice@a.test"
	f.alice.Name = "Alice A"
	f.platformAdmin.Email = "root@x.test"
	now := f.now
	af := &accountFixture{fixture: f, resets: &fakeResets{rows: map[string]*PasswordReset{}}, notify: &fakeNotifier{}, clock: &now}

	deps := AccountDeps{
		Tx:      func(ctx context.Context, _ string, fn func(context.Context) error) error { return fn(ctx) },
		Tenants: fakeTenants{"tokoa": f.tenantA, "tokob": f.tenantB}, Users: f.users, Tokens: f.tokens,
		Resets: af.resets, Hasher: f.hasher, Notifier: af.notify,
		Limits: Limits{Attempts: 3, Window: time.Minute},
		Now:    func() time.Time { return now },
	}
	if withLimiter {
		deps.Limiter = ratelimit.New(ratelimit.NewMemoryStore(func() time.Time { return now }), nil)
	}
	acct, err := NewAccount(deps)
	if err != nil {
		t.Fatal(err)
	}
	af.acct = acct
	return af
}

const newPassword = "a-brand-new-password-1"

func (a *accountFixture) request(t *testing.T, tenantCode, identifier string) {
	t.Helper()
	if err := a.acct.RequestReset(context.Background(), ResetRequest{TenantCode: tenantCode, Identifier: identifier, IP: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	a.acct.Wait()
}

// ---- forgot / reset ----

func TestReset_FullFlow(t *testing.T) {
	a := newAccountFixture(t, false)
	old := a.loginAlice(t) // a session that must end with the reset

	a.request(t, " TokoA ", "ALICE@a.test") // by e-mail, any case
	n := a.notify.last(t)
	if n.Email != "alice@a.test" || n.TenantCode != "tokoa" || n.Name != "Alice A" {
		t.Errorf("notice = %+v", n)
	}
	if !strings.HasPrefix(n.Token, a.tenantA.ID+".") {
		t.Errorf("token must carry the tenant: %q", n.Token)
	}
	if !n.ExpiresAt.Equal(a.f().Add(30 * time.Minute)) {
		t.Errorf("ExpiresAt = %v", n.ExpiresAt)
	}
	// only the hash is stored
	for _, r := range a.resets.rows {
		if strings.Contains(string(r.TokenHash), n.Token) {
			t.Error("the raw token must not be stored")
		}
	}

	if err := a.acct.ResetPassword(context.Background(), n.Token, newPassword, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	// the new password works, the old one does not, the old session is gone
	if _, err := a.svc.Login(context.Background(), LoginInput{TenantCode: "tokoa", Username: "alice", Password: newPassword}); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	if _, err := a.svc.Login(context.Background(), LoginInput{TenantCode: "tokoa", Username: "alice", Password: alicePassword}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the old password must stop working: %v", err)
	}
	if _, err := a.svc.Refresh(context.Background(), old.RefreshToken, "", ""); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("old sessions must end: %v", err)
	}
	// single use
	if err := a.acct.ResetPassword(context.Background(), n.Token, "another-new-password-2", ""); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("second use: %v", err)
	}
}

func (a *accountFixture) f() time.Time { return a.now }

func TestRequestReset_SilentForUnknownAccounts(t *testing.T) {
	a := newAccountFixture(t, false)
	inactive := *a.alice
	inactive.ID = "u-inactive"
	inactive.Username, inactive.Email, inactive.IsActive = "sleepy", "sleepy@a.test", false
	a.users.byID[inactive.ID] = &inactive

	for name, req := range map[string]ResetRequest{
		"unknown tenant": {TenantCode: "nope", Identifier: "alice"},
		"bad tenant":     {TenantCode: "A B!", Identifier: "alice"},
		"unknown user":   {TenantCode: "tokoa", Identifier: "ghost"},
		"other tenant":   {TenantCode: "tokob", Identifier: "alice"},
		"inactive user":  {TenantCode: "tokoa", Identifier: "sleepy"},
	} {
		if err := a.acct.RequestReset(context.Background(), req); err != nil {
			t.Errorf("%s: must answer like a success, got %v", name, err)
		}
	}
	a.acct.Wait()
	if len(a.notify.sent) != 0 || len(a.resets.rows) != 0 {
		t.Fatalf("nothing may be created or sent: %d mails, %d rows", len(a.notify.sent), len(a.resets.rows))
	}
}

func TestRequestReset_NewRequestReplacesTheOld(t *testing.T) {
	a := newAccountFixture(t, false)
	a.request(t, "tokoa", "alice")
	first := a.notify.last(t).Token
	a.request(t, "tokoa", "alice")
	second := a.notify.last(t).Token

	if err := a.acct.ResetPassword(context.Background(), first, newPassword, ""); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("the older token must be dead: %v", err)
	}
	if err := a.acct.ResetPassword(context.Background(), second, newPassword, ""); err != nil {
		t.Fatalf("the latest token: %v", err)
	}
}

func TestResetPassword_Rejections(t *testing.T) {
	a := newAccountFixture(t, false)
	ctx := context.Background()
	a.request(t, "tokoa", "alice")
	tok := a.notify.last(t).Token

	for name, token := range map[string]string{
		"empty":        "",
		"no scope":     "abc",
		"bad scope":    "not-a-uuid.abc",
		"unknown":      a.tenantA.ID + ".unknown-token",
		"wrong tenant": a.tenantB.ID + "." + strings.SplitN(tok, ".", 2)[1],
	} {
		if err := a.acct.ResetPassword(ctx, token, newPassword, ""); !errors.Is(err, ErrInvalidResetToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a weak password is refused before the token is spent
	if err := a.acct.ResetPassword(ctx, tok, "short", ""); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak: %v", err)
	}
	if err := a.acct.ResetPassword(ctx, tok, newPassword, ""); err != nil {
		t.Fatalf("the token must still work after a refused password: %v", err)
	}
}

func TestResetPassword_ExpiredAndDeactivated(t *testing.T) {
	a := newAccountFixture(t, false)
	ctx := context.Background()

	a.request(t, "tokoa", "alice")
	tok := a.notify.last(t).Token
	*a.clock = a.clock.Add(31 * time.Minute)
	if err := a.acct.ResetPassword(ctx, tok, newPassword, ""); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("expired: %v", err)
	}

	*a.clock = a.f()
	a.request(t, "tokoa", "alice")
	tok = a.notify.last(t).Token
	a.alice.IsActive = false
	if err := a.acct.ResetPassword(ctx, tok, newPassword, ""); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("deactivated after the request: %v", err)
	}
}

func TestResetPassword_Platform(t *testing.T) {
	a := newAccountFixture(t, false)
	if err := a.acct.RequestReset(context.Background(), ResetRequest{Platform: true, Identifier: "root", IP: "1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	a.acct.Wait()
	n := a.notify.last(t)
	if !strings.HasPrefix(n.Token, "platform.") || n.TenantCode != "" {
		t.Fatalf("notice = %+v", n)
	}
	if err := a.acct.ResetPassword(context.Background(), n.Token, newPassword, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.LoginPlatform(context.Background(), LoginInput{Username: "root", Password: newPassword}); err != nil {
		t.Fatalf("platform login with the new password: %v", err)
	}
}

func TestResetPassword_ConcurrentUseOfOneToken(t *testing.T) {
	a := newAccountFixture(t, false)
	a.request(t, "tokoa", "alice")
	tok := a.notify.last(t).Token

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.acct.ResetPassword(context.Background(), tok, newPassword, ""); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("exactly one request may win, %d did", ok)
	}
}

func TestRequestReset_NotifierFailureDoesNotChangeTheAnswer(t *testing.T) {
	a := newAccountFixture(t, false)
	a.notify.err = errors.New("smtp down")
	if err := a.acct.RequestReset(context.Background(), ResetRequest{TenantCode: "tokoa", Identifier: "alice"}); err != nil {
		t.Fatalf("a mail failure must not be visible to the caller: %v", err)
	}
	a.acct.Wait()
}

func TestRequestReset_RateLimited(t *testing.T) {
	a := newAccountFixture(t, true)
	for i := 0; i < 3; i++ {
		a.request(t, "tokoa", "alice")
	}
	err := a.acct.RequestReset(context.Background(), ResetRequest{TenantCode: "tokoa", Identifier: "alice", IP: "10.0.0.1"})
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("want a rate limit, got %v", err)
	}
	// unknown accounts are counted the same way: the limit says nothing about existence
	for i := 0; i < 3; i++ {
		_ = a.acct.RequestReset(context.Background(), ResetRequest{TenantCode: "tokoa", Identifier: "ghost", IP: "10.0.0.1"})
	}
	if err := a.acct.RequestReset(context.Background(), ResetRequest{TenantCode: "tokoa", Identifier: "ghost", IP: "10.0.0.1"}); !errors.As(err, &rl) {
		t.Fatalf("unknown account must be limited too: %v", err)
	}
}

// ---- change ----

func (a *accountFixture) change(cur, next string) error {
	return a.acct.ChangePassword(context.Background(), ChangeInput{
		UserID: a.alice.ID, TenantID: a.tenantA.ID, Level: auth.LevelTenant, Current: cur, New: next, IP: "10.0.0.1",
	})
}

func TestChangePassword(t *testing.T) {
	a := newAccountFixture(t, false)
	tok := a.loginAlice(t)

	if err := a.change("wrong-current-password", newPassword); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong current: %v", err)
	}
	if err := a.change(alicePassword, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak: %v", err)
	}
	if err := a.change(alicePassword, alicePassword); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("same password: %v", err)
	}
	// nothing changed so far: the session is alive
	if _, err := a.svc.Refresh(context.Background(), tok.RefreshToken, "", ""); err != nil {
		t.Fatalf("refused attempts must not end sessions: %v", err)
	}

	if err := a.change(alicePassword, newPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Login(context.Background(), LoginInput{TenantCode: "tokoa", Username: "alice", Password: newPassword}); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if got := a.tokens.active(a.alice.ID); got != 1 { // only the login just above
		t.Fatalf("all earlier sessions must end, %d are active", got)
	}
}

func TestChangePassword_Platform(t *testing.T) {
	a := newAccountFixture(t, false)
	err := a.acct.ChangePassword(context.Background(), ChangeInput{
		UserID: a.platformAdmin.ID, Level: auth.LevelPlatform, Current: adminPassword, New: newPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.LoginPlatform(context.Background(), LoginInput{Username: "root", Password: newPassword}); err != nil {
		t.Fatal(err)
	}
}

func TestChangePassword_UnavailableAccountAndRateLimit(t *testing.T) {
	a := newAccountFixture(t, true)
	gone := ChangeInput{UserID: "no-such-user", TenantID: a.tenantA.ID, Level: auth.LevelTenant, Current: "x", New: newPassword}
	if err := a.acct.ChangePassword(context.Background(), gone); !errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("missing user: %v", err)
	}
	a.alice.IsActive = false
	if err := a.change(alicePassword, newPassword); !errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("inactive user: %v", err)
	}
	a.alice.IsActive = true

	for i := 0; i < 3; i++ {
		_ = a.change("wrong-current-password", newPassword)
	}
	var rl *RateLimitedError
	if err := a.change(alicePassword, newPassword); !errors.As(err, &rl) {
		t.Fatalf("guessing the current password must be limited, even with the right one: %v", err)
	}
}

func TestNewAccount_Validation(t *testing.T) {
	if _, err := NewAccount(AccountDeps{}); err == nil {
		t.Fatal("missing dependencies must be refused")
	}
}
