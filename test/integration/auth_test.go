//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/cli"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/session"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
)

// authEnv wires the real service on the application role (RLS applies).
type authEnv struct {
	*env
	svc    *session.Service
	prov   *cli.Provisioner
	mgr    *auth.Manager
	router http.Handler
}

func setupAuth(t *testing.T) *authEnv {
	t.Helper()
	e := setup(t)

	hasher, err := password.New(password.Params{MemoryKiB: 64, Time: 1, Threads: 1}) // fast for tests
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.NewManager(auth.GenerateKeyHex(), "it", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := session.NewService(session.Deps{
		Tx:         session.NewTxRunner(e.app),
		Tenants:    tenant.NewRepository(e.app),
		Users:      user.NewRepository(e.app),
		Tokens:     session.NewRepository(e.app),
		Hasher:     hasher,
		Issuer:     mgr,
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	handler := session.NewHandler(svc, tenantctx.Chain(tenantctx.HeaderResolver{}), middleware.Authenticate(mgr), nil)
	router := rest.NewRouter(&config.Config{Env: "testing"}, slog.New(slog.DiscardHandler), e.app, handler)

	return &authEnv{env: e, svc: svc, prov: cli.NewProvisioner(e.app, hasher), mgr: mgr, router: router}
}

func (a *authEnv) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, c := range []string{"tokoa", "tokob"} {
		if _, err := a.prov.CreateTenant(ctx, c, "Toko "+c); err != nil {
			t.Fatalf("create tenant %s: %v", c, err)
		}
	}
	// the same username exists in both tenants, with different passwords
	if _, err := a.prov.CreateTenantUser(ctx, "tokoa", "alice", "alice@a.test", "Alice A", "password-of-alice-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prov.CreateTenantUser(ctx, "tokob", "alice", "alice@b.test", "Alice B", "password-of-alice-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prov.CreatePlatformAdmin(ctx, "root", "root@x.test", "Root", "password-of-the-root"); err != nil {
		t.Fatal(err)
	}
}

func (a *authEnv) count(t *testing.T, query string, args ...any) (n int64) {
	t.Helper()
	if err := a.owner.Raw(query, args...).Scan(&n).Error; err != nil { // owner bypasses RLS: ground truth
		t.Fatal(err)
	}
	return n
}

func TestProvision_Validation(t *testing.T) {
	a := setupAuth(t)
	ctx := context.Background()
	a.seed(t)

	if _, err := a.prov.CreateTenant(ctx, "tokoa", "Again"); !errors.Is(err, cli.ErrAlreadyExists) {
		t.Errorf("duplicate tenant: %v", err)
	}
	if _, err := a.prov.CreateTenantUser(ctx, "tokoa", "ALICE", "other@a.test", "Dup", "password-of-alice-a"); !errors.Is(err, cli.ErrAlreadyExists) {
		t.Errorf("duplicate username (case-insensitive): %v", err)
	}
	if _, err := a.prov.CreateTenantUser(ctx, "tokoa", "bob", "alice@a.test", "Dup", "password-of-bob-xx"); !errors.Is(err, cli.ErrAlreadyExists) {
		t.Errorf("duplicate email: %v", err)
	}
	if _, err := a.prov.CreateTenantUser(ctx, "ghost", "bob", "bob@a.test", "Bob", "password-of-bob-xx"); err == nil {
		t.Error("user in a missing tenant must fail")
	}
	if _, err := a.prov.CreatePlatformAdmin(ctx, "root", "other@x.test", "Dup", "password-of-the-root"); !errors.Is(err, cli.ErrAlreadyExists) {
		t.Errorf("duplicate platform admin: %v", err)
	}
	for name, fn := range map[string]func() error{
		"short password": func() error {
			_, err := a.prov.CreateTenantUser(ctx, "tokoa", "carol", "c@a.test", "C", "short")
			return err
		},
		"bad email": func() error {
			_, err := a.prov.CreateTenantUser(ctx, "tokoa", "carol", "not-an-email", "C", "long-enough-password")
			return err
		},
		"bad tenant code": func() error { _, err := a.prov.CreateTenant(ctx, "-bad-", "X"); return err },
		"one-letter code": func() error { _, err := a.prov.CreateTenant(ctx, "x", "X"); return err },
	} {
		if err := fn(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	// the stored value is a hash, and the platform admin belongs to no tenant
	if n := a.count(t, "SELECT count(*) FROM users WHERE password_hash LIKE '$argon2id$%'"); n != 3 {
		t.Errorf("hashed users = %d, want 3", n)
	}
	if n := a.count(t, "SELECT count(*) FROM users WHERE tenant_id IS NULL AND level = 'platform'"); n != 1 {
		t.Errorf("platform admins = %d, want 1", n)
	}
}

func TestAuth_LoginIsolatedPerTenant(t *testing.T) {
	a := setupAuth(t)
	a.seed(t)
	ctx := context.Background()

	tok, err := a.svc.Login(ctx, session.LoginInput{TenantCode: "tokoa", Username: "alice", Password: "password-of-alice-a", UserAgent: "it", IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	claims, err := a.mgr.Parse(tok.AccessToken)
	if err != nil || claims.Level != auth.LevelTenant {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	tenantA := a.owner.Raw("SELECT tenant_id FROM tenants WHERE code = 'tokoa'")
	var tid string
	if err := tenantA.Scan(&tid).Error; err != nil || claims.TenantID != tid {
		t.Errorf("token tenant = %q, want %q (err=%v)", claims.TenantID, tid, err)
	}
	if n := a.count(t, "SELECT count(*) FROM user_tokens WHERE tenant_id = ? AND user_token_id = ? AND revoked_at IS NULL", tid, claims.TokenID); n != 1 {
		t.Errorf("session rows = %d, want 1", n)
	}
	if n := a.count(t, "SELECT count(*) FROM user_tokens WHERE position(? in encode(token_hash, 'escape')) > 0", tok.RefreshToken); n != 0 {
		t.Error("the raw refresh token must not be stored")
	}

	// Alice of tenant B has another password; the two accounts never mix.
	for name, in := range map[string]session.LoginInput{
		"A's password in tenant B": {TenantCode: "tokob", Username: "alice", Password: "password-of-alice-a"},
		"B's password in tenant A": {TenantCode: "tokoa", Username: "alice", Password: "password-of-alice-b"},
		"unknown tenant":           {TenantCode: "ghost", Username: "alice", Password: "password-of-alice-a"},
		"platform admin as tenant": {TenantCode: "tokoa", Username: "root", Password: "password-of-the-root"},
		"empty password":           {TenantCode: "tokoa", Username: "alice", Password: ""},
	} {
		if _, err := a.svc.Login(ctx, in); !errors.Is(err, session.ErrInvalidCredentials) {
			t.Errorf("%s: err = %v, want ErrInvalidCredentials", name, err)
		}
	}
	if _, err := a.svc.Login(ctx, session.LoginInput{TenantCode: "tokob", Username: "alice", Password: "password-of-alice-b"}); err != nil {
		t.Errorf("alice of tenant B must be able to log in: %v", err)
	}
}

func TestAuth_SuspendedTenantAndInactiveUser(t *testing.T) {
	a := setupAuth(t)
	a.seed(t)
	ctx := context.Background()
	in := session.LoginInput{TenantCode: "tokoa", Username: "alice", Password: "password-of-alice-a"}

	if err := a.owner.Exec("UPDATE users SET is_active = false WHERE username = 'alice' AND tenant_id = (SELECT tenant_id FROM tenants WHERE code = 'tokoa')").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Login(ctx, in); !errors.Is(err, session.ErrInvalidCredentials) {
		t.Errorf("inactive user: %v", err)
	}
	if err := a.owner.Exec("UPDATE users SET is_active = true WHERE username = 'alice'").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Login(ctx, in); err != nil {
		t.Fatalf("reactivated user must log in: %v", err)
	}
	if err := a.owner.Exec("UPDATE tenants SET status = 'suspended' WHERE code = 'tokoa'").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Login(ctx, in); !errors.Is(err, session.ErrInvalidCredentials) {
		t.Errorf("suspended tenant: %v", err)
	}
}

func TestAuth_RefreshRotationAndReuseDetection(t *testing.T) {
	a := setupAuth(t)
	a.seed(t)
	ctx := context.Background()
	login := func() *session.Tokens {
		tok, err := a.svc.Login(ctx, session.LoginInput{TenantCode: "tokoa", Username: "alice", Password: "password-of-alice-a"})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	stolen, laptop := login(), login()
	rotated, err := a.svc.Refresh(ctx, stolen.RefreshToken, "it", "127.0.0.1")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if n := a.count(t, "SELECT count(*) FROM user_tokens WHERE revoked_at IS NULL"); n != 2 { // laptop + rotated
		t.Errorf("active sessions after rotation = %d, want 2", n)
	}

	// The old token comes back: the revocation of ALL sessions must survive (it is committed, not rolled back).
	if _, err := a.svc.Refresh(ctx, stolen.RefreshToken, "", ""); !errors.Is(err, session.ErrInvalidRefreshToken) {
		t.Fatalf("reuse: %v", err)
	}
	if n := a.count(t, "SELECT count(*) FROM user_tokens WHERE revoked_at IS NULL"); n != 0 {
		t.Errorf("active sessions after reuse = %d, want 0", n)
	}
	for name, tok := range map[string]*session.Tokens{"rotated": rotated, "laptop": laptop} {
		if _, err := a.svc.Refresh(ctx, tok.RefreshToken, "", ""); !errors.Is(err, session.ErrInvalidRefreshToken) {
			t.Errorf("%s must be dead: %v", name, err)
		}
	}
}

func TestAuth_RefreshIgnoresOtherTenantsRows(t *testing.T) {
	a := setupAuth(t)
	a.seed(t)
	ctx := context.Background()

	tok, err := a.svc.Login(ctx, session.LoginInput{TenantCode: "tokoa", Username: "alice", Password: "password-of-alice-a"})
	if err != nil {
		t.Fatal(err)
	}
	var tenantB string
	if err := a.owner.Raw("SELECT tenant_id FROM tenants WHERE code = 'tokob'").Scan(&tenantB).Error; err != nil {
		t.Fatal(err)
	}
	secret := strings.SplitN(tok.RefreshToken, ".", 2)[1]

	// Tenant A's secret presented under tenant B's scope must not be found (RLS + explicit filter).
	if _, err := a.svc.Refresh(ctx, tenantB+"."+secret, "", ""); !errors.Is(err, session.ErrInvalidRefreshToken) {
		t.Errorf("err = %v, want ErrInvalidRefreshToken", err)
	}
	// ...and as a platform token.
	if _, err := a.svc.Refresh(ctx, "platform."+secret, "", ""); !errors.Is(err, session.ErrInvalidRefreshToken) {
		t.Errorf("err = %v, want ErrInvalidRefreshToken", err)
	}
	// the real token still works afterwards
	if _, err := a.svc.Refresh(ctx, tok.RefreshToken, "", ""); err != nil {
		t.Errorf("the real token must still work: %v", err)
	}
}

func TestAuth_ConcurrentRefreshOnlyOneWins(t *testing.T) {
	a := setupAuth(t)
	a.seed(t)
	ctx := context.Background()
	tok, err := a.svc.Login(ctx, session.LoginInput{TenantCode: "tokoa", Username: "alice", Password: "password-of-alice-a"})
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, failed := 0, 0
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.svc.Refresh(ctx, tok.RefreshToken, "", "")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, session.ErrInvalidRefreshToken):
				failed++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if won != 1 || failed != n-1 {
		t.Errorf("won=%d failed=%d, want 1 and %d", won, failed, n-1)
	}
}

func TestAuth_PlatformAdminSession(t *testing.T) {
	a := setupAuth(t)
	a.seed(t)
	ctx := context.Background()

	tok, err := a.svc.LoginPlatform(ctx, session.LoginInput{Username: "root", Password: "password-of-the-root"})
	if err != nil {
		t.Fatalf("platform login: %v", err)
	}
	claims, err := a.mgr.Parse(tok.AccessToken)
	if err != nil || claims.Level != auth.LevelPlatform || claims.TenantID != "" {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	if n := a.count(t, "SELECT count(*) FROM user_tokens WHERE tenant_id IS NULL AND user_token_id = ?", claims.TokenID); n != 1 {
		t.Errorf("platform session rows = %d, want 1", n)
	}

	// A platform admin cannot log in through a tenant, and a tenant user cannot use the platform endpoint.
	if _, err := a.svc.LoginPlatform(ctx, session.LoginInput{Username: "alice", Password: "password-of-alice-a"}); !errors.Is(err, session.ErrInvalidCredentials) {
		t.Errorf("tenant user on platform endpoint: %v", err)
	}

	// refresh and logout work for platform sessions
	next, err := a.svc.Refresh(ctx, tok.RefreshToken, "", "")
	if err != nil {
		t.Fatalf("platform refresh: %v", err)
	}
	if err := a.svc.Logout(ctx, next.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Refresh(ctx, next.RefreshToken, "", ""); !errors.Is(err, session.ErrInvalidRefreshToken) {
		t.Errorf("after logout: %v", err)
	}
}

func TestAuth_UpgradesWeakPasswordHash(t *testing.T) {
	a := setupAuth(t)
	a.seed(t)
	weak, _ := password.New(password.Params{MemoryKiB: 32, Time: 1, Threads: 1})
	old, _ := weak.Hash("password-of-alice-a")
	if err := a.owner.Exec("UPDATE users SET password_hash = ? WHERE email = 'alice@a.test'", old).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := a.svc.Login(context.Background(), session.LoginInput{TenantCode: "tokoa", Username: "alice", Password: "password-of-alice-a"}); err != nil {
		t.Fatal(err)
	}
	var now string
	if err := a.owner.Raw("SELECT password_hash FROM users WHERE email = 'alice@a.test'").Scan(&now).Error; err != nil {
		t.Fatal(err)
	}
	if now == old || !strings.Contains(now, "m=64,t=1,p=1") {
		t.Errorf("the hash must be upgraded to the current parameters, got %s", now)
	}
}

// The whole thing through HTTP: login, a protected endpoint, refresh, logout.
func TestAuth_HTTPFlow(t *testing.T) {
	a := setupAuth(t)
	a.seed(t)

	do := func(method, path, body, bearer string, headers ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		w := httptest.NewRecorder()
		a.router.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}

	// tenant code from the header
	w := do(http.MethodPost, "/api/v1/auth/login", `{"username":"alice","password":"password-of-alice-a"}`, "", "X-Tenant-Code", "tokoa")
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	tokens := decode(w)
	access, refresh := tokens["access_token"].(string), tokens["refresh_token"].(string)

	me := do(http.MethodGet, "/api/v1/auth/me", "", access)
	if me.Code != http.StatusOK || decode(me)["level"] != "tenant" {
		t.Fatalf("me: %d %s", me.Code, me.Body)
	}
	if do(http.MethodGet, "/api/v1/auth/me", "", "").Code != http.StatusUnauthorized {
		t.Error("/me without a token must be 401")
	}

	// no tenant at all: the same answer as a wrong password
	w = do(http.MethodPost, "/api/v1/auth/login", `{"username":"alice","password":"password-of-alice-a"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("login without a tenant: %d", w.Code)
	}
	// tenant code in the body is the fallback
	w = do(http.MethodPost, "/api/v1/auth/login", `{"tenant_code":"tokob","username":"alice","password":"password-of-alice-b"}`, "")
	if w.Code != http.StatusOK {
		t.Errorf("login with tenant_code in the body: %d %s", w.Code, w.Body)
	}

	w = do(http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"`+refresh+`"}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	newRefresh := decode(w)["refresh_token"].(string)
	if w := do(http.MethodPost, "/api/v1/auth/logout", `{"refresh_token":"`+newRefresh+`"}`, ""); w.Code != http.StatusNoContent {
		t.Errorf("logout: %d", w.Code)
	}
	if w := do(http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"`+newRefresh+`"}`, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("refresh after logout: %d", w.Code)
	}

	// platform
	w = do(http.MethodPost, "/api/v1/auth/platform/login", `{"username":"root","password":"password-of-the-root"}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("platform login: %d %s", w.Code, w.Body)
	}
	if got := decode(do(http.MethodGet, "/api/v1/auth/me", "", decode(w)["access_token"].(string)))["level"]; got != "platform" {
		t.Errorf("platform /me level = %v", got)
	}
}
