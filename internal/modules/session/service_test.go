package session

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// ---- fakes (no database) ----

type fakeTokens struct {
	mu   sync.Mutex
	rows map[string]*UserToken // by id
}

func newFakeTokens() *fakeTokens { return &fakeTokens{rows: map[string]*UserToken{}} }

func (f *fakeTokens) Create(_ context.Context, t *UserToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *t
	f.rows[t.ID] = &c
	return nil
}

func (f *fakeTokens) FindByHash(_ context.Context, hash []byte) (*UserToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if bytes.Equal(r.TokenHash, hash) {
			c := *r
			return &c, nil
		}
	}
	return nil, core.ErrNotFound
}

func (f *fakeTokens) Revoke(_ context.Context, id string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok || r.RevokedAt != nil {
		return false, nil
	}
	r.RevokedAt = &at
	return true, nil
}

func (f *fakeTokens) RevokeAllForUser(_ context.Context, userID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.UserID == userID && r.RevokedAt == nil {
			t := at
			r.RevokedAt = &t
		}
	}
	return nil
}

func (f *fakeTokens) active(userID string) (n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.UserID == userID && r.RevokedAt == nil {
			n++
		}
	}
	return n
}

type fakeTenants map[string]*tenant.Tenant // by code

func (f fakeTenants) FindByCode(_ context.Context, code string) (*tenant.Tenant, error) {
	if t, ok := f[code]; ok {
		return t, nil
	}
	return nil, core.ErrNotFound
}

type fakeUsers struct {
	byUsername map[string]*user.User // key: tenantID + "/" + lower(username); platform uses "/"
	byID       map[string]*user.User
	setHash    []string
}

func (f *fakeUsers) FindByUsername(ctx context.Context, username string) (*user.User, error) {
	id, _ := tenantctx.IDFrom(ctx)
	if u, ok := f.byUsername[id+"/"+strings.ToLower(username)]; ok {
		return u, nil
	}
	return nil, core.ErrNotFound
}

func (f *fakeUsers) FindPlatformByUsername(_ context.Context, username string) (*user.User, error) {
	if u, ok := f.byUsername["/"+strings.ToLower(username)]; ok {
		return u, nil
	}
	return nil, core.ErrNotFound
}

func (f *fakeUsers) GetByID(ctx context.Context, id string) (*user.User, error) {
	tid, _ := tenantctx.IDFrom(ctx)
	if u, ok := f.byID[id]; ok && u.TenantID != nil && *u.TenantID == tid {
		return u, nil
	}
	return nil, core.ErrNotFound
}

func (f *fakeUsers) FindPlatformByID(_ context.Context, id string) (*user.User, error) {
	if u, ok := f.byID[id]; ok && u.TenantID == nil {
		return u, nil
	}
	return nil, core.ErrNotFound
}

func (f *fakeUsers) SetPasswordHash(_ context.Context, u *user.User, hash string) error {
	f.setHash = append(f.setHash, hash)
	u.PasswordHash = hash
	return nil
}

// ---- fixture ----

type fixture struct {
	svc    *Service
	tokens *fakeTokens
	users  *fakeUsers
	mgr    *auth.Manager
	hasher *password.Hasher
	now    time.Time

	tenantA, tenantB *tenant.Tenant
	alice            *user.User // active, tenant A
	platformAdmin    *user.User
}

const (
	alicePassword = "alice-correct-password"
	adminPassword = "platform-correct-password"
)

var fastParams = password.Params{MemoryKiB: 64, Time: 1, Threads: 1}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	hasher, err := password.New(fastParams)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.NewManager(auth.GenerateKeyHex(), "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{
		tokens: newFakeTokens(), hasher: hasher, mgr: mgr,
		now:     time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		tenantA: &tenant.Tenant{ID: idgen.New(), Code: "tokoa", Name: "Toko A", Status: tenant.StatusActive},
		tenantB: &tenant.Tenant{ID: idgen.New(), Code: "tokob", Name: "Toko B", Status: tenant.StatusActive},
	}
	aliceHash, _ := hasher.Hash(alicePassword)
	adminHash, _ := hasher.Hash(adminPassword)
	f.alice = &user.User{ID: idgen.New(), TenantID: &f.tenantA.ID, Level: auth.LevelTenant, Username: "Alice", PasswordHash: aliceHash, IsActive: true}
	f.platformAdmin = &user.User{ID: idgen.New(), Level: auth.LevelPlatform, Username: "root", PasswordHash: adminHash, IsActive: true}

	f.users = &fakeUsers{byUsername: map[string]*user.User{}, byID: map[string]*user.User{}}
	for _, u := range []*user.User{f.alice, f.platformAdmin} {
		key := "/"
		if u.TenantID != nil {
			key = *u.TenantID + "/"
		}
		f.users.byUsername[key+strings.ToLower(u.Username)] = u
		f.users.byID[u.ID] = u
	}

	svc, err := NewService(Deps{
		Tx:         func(ctx context.Context, _ string, fn func(context.Context) error) error { return fn(ctx) },
		Tenants:    fakeTenants{"tokoa": f.tenantA, "tokob": f.tenantB},
		Users:      f.users,
		Tokens:     f.tokens,
		Hasher:     hasher,
		Issuer:     mgr,
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
		Now:        func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f
}

func (f *fixture) loginAlice(t *testing.T) *Tokens {
	t.Helper()
	tok, err := f.svc.Login(context.Background(), LoginInput{TenantCode: "tokoa", Username: "alice", Password: alicePassword})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return tok
}

// ---- tests ----

func TestLogin_Success(t *testing.T) {
	f := newFixture(t)
	tok, err := f.svc.Login(context.Background(), LoginInput{
		TenantCode: " TokoA ", Username: "ALICE", Password: alicePassword, UserAgent: "test-agent", IP: "10.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}

	claims, err := f.mgr.Parse(tok.AccessToken)
	if err != nil {
		t.Fatalf("the access token must be valid: %v", err)
	}
	if claims.UserID != f.alice.ID || claims.TenantID != f.tenantA.ID || claims.Level != auth.LevelTenant {
		t.Errorf("claims = %+v", claims)
	}
	if tok.ExpiresIn != 15*time.Minute {
		t.Errorf("ExpiresIn = %v", tok.ExpiresIn)
	}
	if !strings.HasPrefix(tok.RefreshToken, f.tenantA.ID+".") {
		t.Errorf("refresh token must start with the tenant id, got %q", tok.RefreshToken)
	}

	// The session row exists, belongs to the tenant, and only the hash is stored.
	row, err := f.tokens.FindByHash(context.Background(), hashToken(tok.RefreshToken))
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != claims.TokenID || row.UserID != f.alice.ID || row.TenantID == nil || *row.TenantID != f.tenantA.ID {
		t.Errorf("row = %+v", row)
	}
	if bytes.Contains(row.TokenHash, []byte(tok.RefreshToken)) {
		t.Error("the raw token must not be stored")
	}
	if !row.ExpiresAt.Equal(f.now.Add(24 * time.Hour)) {
		t.Errorf("ExpiresAt = %v", row.ExpiresAt)
	}
}

func TestLogin_FailuresAreIndistinguishable(t *testing.T) {
	f := newFixture(t)
	f.tenantB.Status = tenant.StatusSuspended
	inactive := *f.alice
	inactive.ID, inactive.Username, inactive.IsActive = idgen.New(), "inactive", false
	f.users.byUsername[f.tenantA.ID+"/inactive"] = &inactive

	tests := []struct {
		name string
		in   LoginInput
	}{
		{"wrong password", LoginInput{TenantCode: "tokoa", Username: "alice", Password: "wrong"}},
		{"unknown user", LoginInput{TenantCode: "tokoa", Username: "nobody", Password: alicePassword}},
		{"unknown tenant", LoginInput{TenantCode: "nope", Username: "alice", Password: alicePassword}},
		{"malformed tenant code", LoginInput{TenantCode: "A B", Username: "alice", Password: alicePassword}},
		{"empty tenant code", LoginInput{TenantCode: "", Username: "alice", Password: alicePassword}},
		{"suspended tenant", LoginInput{TenantCode: "tokob", Username: "alice", Password: alicePassword}},
		{"inactive user with right password", LoginInput{TenantCode: "tokoa", Username: "inactive", Password: alicePassword}},
		{"user of another tenant", LoginInput{TenantCode: "tokob", Username: "alice", Password: alicePassword}},
		{"platform admin through the tenant endpoint", LoginInput{TenantCode: "tokoa", Username: "root", Password: adminPassword}},
		{"password too long", LoginInput{TenantCode: "tokoa", Username: "alice", Password: strings.Repeat("a", password.MaxLength+1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := f.svc.Login(context.Background(), tc.in)
			if !errors.Is(err, ErrInvalidCredentials) || tok != nil {
				t.Errorf("tok=%v err=%v, want ErrInvalidCredentials", tok, err)
			}
		})
	}
	if len(f.tokens.rows) != 0 {
		t.Errorf("failed logins must not create sessions, got %d", len(f.tokens.rows))
	}
}

func TestLoginPlatform(t *testing.T) {
	f := newFixture(t)

	tok, err := f.svc.LoginPlatform(context.Background(), LoginInput{Username: "ROOT", Password: adminPassword})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := f.mgr.Parse(tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Level != auth.LevelPlatform || claims.TenantID != "" || claims.UserID != f.platformAdmin.ID {
		t.Errorf("claims = %+v", claims)
	}
	if !strings.HasPrefix(tok.RefreshToken, "platform.") {
		t.Errorf("refresh token = %q", tok.RefreshToken)
	}

	// A tenant user cannot use the platform endpoint, even with the right password.
	if _, err := f.svc.LoginPlatform(context.Background(), LoginInput{Username: "alice", Password: alicePassword}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLogin_UpgradesWeakHash(t *testing.T) {
	f := newFixture(t)
	weak, _ := password.New(password.Params{MemoryKiB: 32, Time: 1, Threads: 1})
	f.alice.PasswordHash, _ = weak.Hash(alicePassword)
	old := f.alice.PasswordHash

	f.loginAlice(t)

	if len(f.users.setHash) != 1 || f.alice.PasswordHash == old {
		t.Fatalf("the hash must be upgraded once, setHash=%d", len(f.users.setHash))
	}
	if ok, rehash, _ := f.hasher.Verify(alicePassword, f.alice.PasswordHash); !ok || rehash {
		t.Errorf("the new hash must verify and use the current parameters (ok=%v rehash=%v)", ok, rehash)
	}
}

func TestLogin_MalformedStoredHash(t *testing.T) {
	f := newFixture(t)
	f.alice.PasswordHash = "garbage"
	_, err := f.svc.Login(context.Background(), LoginInput{TenantCode: "tokoa", Username: "alice", Password: alicePassword})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestRefresh_Rotates(t *testing.T) {
	f := newFixture(t)
	first := f.loginAlice(t)
	f.now = f.now.Add(time.Minute)

	second, err := f.svc.Refresh(context.Background(), first.RefreshToken, "agent", "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken || second.AccessToken == first.AccessToken {
		t.Error("refresh must issue new tokens")
	}
	if _, err := f.mgr.Parse(second.AccessToken); err != nil {
		t.Errorf("new access token invalid: %v", err)
	}
	if n := f.tokens.active(f.alice.ID); n != 1 {
		t.Errorf("exactly one active session expected after rotation, got %d", n)
	}

	// the new token can be refreshed again
	if _, err := f.svc.Refresh(context.Background(), second.RefreshToken, "", ""); err != nil {
		t.Errorf("chained refresh: %v", err)
	}
}

func TestRefresh_ReuseRevokesEverything(t *testing.T) {
	f := newFixture(t)
	stolen := f.loginAlice(t)
	other := f.loginAlice(t) // a second device
	rotated, err := f.svc.Refresh(context.Background(), stolen.RefreshToken, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// The old token is used again: someone copied it.
	if _, err := f.svc.Refresh(context.Background(), stolen.RefreshToken, "", ""); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("err = %v, want ErrInvalidRefreshToken", err)
	}
	if n := f.tokens.active(f.alice.ID); n != 0 {
		t.Fatalf("all sessions must be revoked after reuse, %d still active", n)
	}
	for name, tok := range map[string]*Tokens{"rotated": rotated, "other device": other} {
		if _, err := f.svc.Refresh(context.Background(), tok.RefreshToken, "", ""); !errors.Is(err, ErrInvalidRefreshToken) {
			t.Errorf("%s token must be dead, err = %v", name, err)
		}
	}
}

func TestRefresh_Invalid(t *testing.T) {
	f := newFixture(t)
	good := f.loginAlice(t)

	foreign := f.tenantB.ID + "." + strings.SplitN(good.RefreshToken, ".", 2)[1] // right secret, wrong tenant
	expired := f.loginAlice(t)
	f.now = f.now.Add(25 * time.Hour)

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no separator", "abc"},
		{"empty secret", f.tenantA.ID + "."},
		{"scope is not a uuid", "tokoa." + idgen.NewOpaqueToken()},
		{"uppercase uuid", strings.ToUpper(f.tenantA.ID) + "." + idgen.NewOpaqueToken()},
		{"unknown secret", f.tenantA.ID + "." + idgen.NewOpaqueToken()},
		{"another tenant's scope", foreign},
		{"expired", expired.RefreshToken},
		{"way too long", f.tenantA.ID + "." + strings.Repeat("a", 500)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.Refresh(context.Background(), tc.token, "", ""); !errors.Is(err, ErrInvalidRefreshToken) {
				t.Errorf("err = %v, want ErrInvalidRefreshToken", err)
			}
		})
	}
}

func TestRefresh_DeactivatedUser(t *testing.T) {
	f := newFixture(t)
	tok := f.loginAlice(t)
	f.alice.IsActive = false
	if _, err := f.svc.Refresh(context.Background(), tok.RefreshToken, "", ""); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("err = %v, want ErrInvalidRefreshToken", err)
	}
}

func TestRefresh_Platform(t *testing.T) {
	f := newFixture(t)
	tok, err := f.svc.LoginPlatform(context.Background(), LoginInput{Username: "root", Password: adminPassword})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.Refresh(context.Background(), tok.RefreshToken, "", "")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := f.mgr.Parse(next.AccessToken)
	if err != nil || claims.Level != auth.LevelPlatform {
		t.Errorf("claims=%+v err=%v", claims, err)
	}
}

func TestRefresh_ConcurrentUseOfOneToken(t *testing.T) {
	f := newFixture(t)
	tok := f.loginAlice(t)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.svc.Refresh(context.Background(), tok.RefreshToken, "", ""); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if succeeded != 1 {
		t.Errorf("exactly one concurrent refresh may win, got %d", succeeded)
	}
}

func TestLogout(t *testing.T) {
	f := newFixture(t)
	tok := f.loginAlice(t)

	if err := f.svc.Logout(context.Background(), tok.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(context.Background(), tok.RefreshToken, "", ""); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Errorf("a logged-out token must not refresh, err = %v", err)
	}

	// idempotent, and garbage is not an error
	for _, raw := range []string{tok.RefreshToken, "garbage", "", f.tenantA.ID + "." + idgen.NewOpaqueToken()} {
		if err := f.svc.Logout(context.Background(), raw); err != nil {
			t.Errorf("Logout(%q) = %v, want nil", raw, err)
		}
	}
}

func TestLogout_OnlyEndsThatSession(t *testing.T) {
	f := newFixture(t)
	phone, laptop := f.loginAlice(t), f.loginAlice(t)

	if err := f.svc.Logout(context.Background(), phone.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(context.Background(), laptop.RefreshToken, "", ""); err != nil {
		t.Errorf("the other session must keep working: %v", err)
	}
}

func TestNewService_Validation(t *testing.T) {
	f := newFixture(t)
	good := Deps{
		Tx:      func(ctx context.Context, _ string, fn func(context.Context) error) error { return fn(ctx) },
		Tenants: fakeTenants{}, Users: f.users, Tokens: f.tokens, Hasher: f.hasher, Issuer: f.mgr,
		AccessTTL: time.Minute, RefreshTTL: time.Hour,
	}
	if _, err := NewService(good); err != nil {
		t.Fatalf("valid deps: %v", err)
	}

	noTx := good
	noTx.Tx = nil
	badTTL := good
	badTTL.RefreshTTL = badTTL.AccessTTL
	for name, d := range map[string]Deps{"missing dependency": noTx, "refresh not longer than access": badTTL} {
		if _, err := NewService(d); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestParseRefreshToken(t *testing.T) {
	id := idgen.New()
	tests := []struct {
		raw       string
		wantLevel string
		wantTID   string
		wantOK    bool
	}{
		{id + ".abc", auth.LevelTenant, id, true},
		{"platform.abc", auth.LevelPlatform, "", true},
		{"platform.", "", "", false},
		{"platform", "", "", false},
		{"Platform.abc", "", "", false},
		{"", "", "", false},
		{"urn:uuid:" + id + ".abc", "", "", false},
		{"{" + id + "}.abc", "", "", false},
	}
	for _, tc := range tests {
		level, tid, ok := parseRefreshToken(tc.raw)
		if level != tc.wantLevel || tid != tc.wantTID || ok != tc.wantOK {
			t.Errorf("parseRefreshToken(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.raw, level, tid, ok, tc.wantLevel, tc.wantTID, tc.wantOK)
		}
	}
}
