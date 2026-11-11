//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/app"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/session"
	tenantmod "github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/ratelimit"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
)

// mailbox collects the reset notices instead of sending mail.
type mailbox struct {
	mu   sync.Mutex
	sent []session.ResetNotice
}

func (m *mailbox) PasswordReset(_ context.Context, n session.ResetNotice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, n)
	return nil
}

func (m *mailbox) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *mailbox) last(t *testing.T) session.ResetNotice {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("no reset message was sent")
	}
	return m.sent[len(m.sent)-1]
}

type accountEnv struct {
	*authEnv
	r    http.Handler
	box  *mailbox
	acct *session.Account
}

// setupAccount serves the real modules with rate limiting (3 attempts per minute, in memory) and a mailbox.
func setupAccount(t *testing.T) *accountEnv {
	t.Helper()
	a := setupAuth(t)
	a.seed(t)

	limiter := ratelimit.New(ratelimit.NewMemoryStore(nil), nil)
	limits := session.Limits{Attempts: 3, Window: time.Minute}
	svc, err := session.NewService(session.Deps{
		Tx: session.NewTxRunner(a.app), Tenants: tenantmod.NewRepository(a.app), Users: user.NewRepository(a.app),
		Tokens: session.NewRepository(a.app), Hasher: fastHasher(t), Issuer: a.mgr,
		AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour, Limiter: limiter, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	box := &mailbox{}
	acct, err := session.NewAccount(session.AccountDeps{
		Tx: session.NewTxRunner(a.app), Tenants: tenantmod.NewRepository(a.app), Users: user.NewRepository(a.app),
		Tokens: session.NewRepository(a.app), Resets: session.NewResetRepository(a.app),
		Hasher: fastHasher(t), Notifier: box, Limiter: limiter, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	modules := app.Modules(app.Deps{
		Log: slog.New(slog.DiscardHandler), DB: a.app, Tokens: a.mgr, Sessions: svc, Account: acct,
		Resolver: tenantctx.Chain(tenantctx.HeaderResolver{}),
	})
	r := rest.NewRouter(&config.Config{Env: "testing"}, slog.New(slog.DiscardHandler), a.app, modules...)
	return &accountEnv{authEnv: a, r: r, box: box, acct: acct}
}

func (e *accountEnv) forgot(t *testing.T, tenantCode, identifier string) apiResp {
	t.Helper()
	res := call(t, e.r, http.MethodPost, "/api/v1/auth/password/forgot", "", `{"identifier":"`+identifier+`"}`, "X-Tenant-Code", tenantCode)
	e.acct.Wait()
	return res
}

func (e *accountEnv) reset(t *testing.T, token, pw string) apiResp {
	t.Helper()
	return call(t, e.r, http.MethodPost, "/api/v1/auth/password/reset", "", `{"token":"`+token+`","new_password":"`+pw+`"}`)
}

func (e *accountEnv) tokens(t *testing.T, tenantCode, user, pw string) (access, refresh string) {
	t.Helper()
	res := call(t, e.r, http.MethodPost, "/api/v1/auth/login", "", `{"username":"`+user+`","password":"`+pw+`"}`, "X-Tenant-Code", tenantCode)
	if res.Status != 200 {
		t.Fatalf("login %s/%s: %d %s", tenantCode, user, res.Status, res.Raw)
	}
	return jsonField(t, res.Raw, "access_token"), jsonField(t, res.Raw, "refresh_token")
}

func (e *accountEnv) status(t *testing.T, tenantCode, pw string) int {
	t.Helper()
	return call(t, e.r, http.MethodPost, "/api/v1/auth/login", "", `{"username":"alice","password":"`+pw+`"}`, "X-Tenant-Code", tenantCode).Status
}

func TestPassword_ResetFlow(t *testing.T) {
	e := setupAccount(t)
	_, refresh := e.tokens(t, "tokoa", "alice", "password-of-alice-a")

	// the same answer for a known and an unknown account
	known := e.forgot(t, "tokoa", "alice@a.test")
	unknown := e.forgot(t, "tokoa", "nobody@a.test")
	if known.Status != 202 || unknown.Status != 202 || known.Body.Message != unknown.Body.Message {
		t.Fatalf("answers differ: %d %q vs %d %q", known.Status, known.Body.Message, unknown.Status, unknown.Body.Message)
	}
	if e.box.count() != 1 {
		t.Fatalf("exactly one message for the known account, got %d", e.box.count())
	}
	n := e.box.last(t)
	if n.Email != "alice@a.test" || n.TenantCode != "tokoa" {
		t.Fatalf("notice = %+v", n)
	}

	// only the hash reached the database
	var stored int64
	if err := e.owner.Raw("SELECT count(*) FROM password_resets WHERE token_hash = convert_to(?, 'UTF8')", n.Token).Scan(&stored).Error; err != nil || stored != 0 {
		t.Fatalf("raw token in the database: %d %v", stored, err)
	}

	if res := e.reset(t, n.Token, "short"); res.Status != 422 {
		t.Fatalf("weak password: %d %s", res.Status, res.Raw)
	}
	if res := e.reset(t, n.Token, "a-brand-new-password-1"); res.Status != 204 {
		t.Fatalf("reset: %d %s", res.Status, res.Raw)
	}
	if e.status(t, "tokoa", "a-brand-new-password-1") != 200 {
		t.Fatal("the new password must work")
	}
	if e.status(t, "tokoa", "password-of-alice-a") != 401 {
		t.Fatal("the old password must not work")
	}
	if res := call(t, e.r, http.MethodPost, "/api/v1/auth/refresh", "", `{"refresh_token":"`+refresh+`"}`); res.Status != 401 {
		t.Fatalf("sessions from before the reset must end: %d", res.Status)
	}
	if res := e.reset(t, n.Token, "yet-another-password-2"); res.Status != 400 {
		t.Fatalf("a token works once: %d", res.Status)
	}
	// the other tenant's Alice is untouched
	if e.status(t, "tokob", "password-of-alice-b") != 200 {
		t.Fatal("tenant B must not be affected")
	}
}

func TestPassword_ResetTokenIsBoundToItsTenant(t *testing.T) {
	e := setupAccount(t)
	e.forgot(t, "tokoa", "alice")
	n := e.box.last(t)

	var tenantB string
	if err := e.owner.Raw("SELECT tenant_id FROM tenants WHERE code = 'tokob'").Scan(&tenantB).Error; err != nil {
		t.Fatal(err)
	}
	forged := tenantB + "." + strings.SplitN(n.Token, ".", 2)[1]
	if res := e.reset(t, forged, "a-brand-new-password-1"); res.Status != 400 {
		t.Fatalf("a token must not work under another tenant: %d", res.Status)
	}
	if res := e.reset(t, "platform."+strings.SplitN(n.Token, ".", 2)[1], "a-brand-new-password-1"); res.Status != 400 {
		t.Fatalf("a tenant token must not work as a platform token: %d", res.Status)
	}
	if e.status(t, "tokob", "password-of-alice-b") != 200 || e.status(t, "tokoa", "password-of-alice-a") != 200 {
		t.Fatal("no password may have changed")
	}
}

func TestPassword_ResetExpiredOrDeactivated(t *testing.T) {
	e := setupAccount(t)
	e.forgot(t, "tokoa", "alice")
	n := e.box.last(t)
	if err := e.owner.Exec("UPDATE password_resets SET expires_at = now() - interval '1 minute'").Error; err != nil {
		t.Fatal(err)
	}
	if res := e.reset(t, n.Token, "a-brand-new-password-1"); res.Status != 400 {
		t.Fatalf("expired: %d", res.Status)
	}

	e.forgot(t, "tokob", "alice")
	n = e.box.last(t)
	if err := e.owner.Exec("UPDATE users SET is_active = false WHERE username = 'alice' AND tenant_id = (SELECT tenant_id FROM tenants WHERE code='tokob')").Error; err != nil {
		t.Fatal(err)
	}
	if res := e.reset(t, n.Token, "a-brand-new-password-1"); res.Status != 400 {
		t.Fatalf("deactivated: %d", res.Status)
	}
	// no mail for a deactivated account
	before := e.box.count()
	e.forgot(t, "tokob", "alice")
	if e.box.count() != before {
		t.Fatal("a deactivated account must not get a message")
	}
}

func TestPassword_ConcurrentResetsOnlyOneWins(t *testing.T) {
	e := setupAccount(t)
	e.forgot(t, "tokoa", "alice")
	n := e.box.last(t)

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.reset(t, n.Token, "a-brand-new-password-1").Status == 204 {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d resets succeeded with one token", wins)
	}
}

func TestPassword_PlatformAdmin(t *testing.T) {
	e := setupAccount(t)
	res := call(t, e.r, http.MethodPost, "/api/v1/auth/platform/password/forgot", "", `{"identifier":"root@x.test"}`)
	e.acct.Wait()
	if res.Status != 202 {
		t.Fatalf("forgot: %d %s", res.Status, res.Raw)
	}
	n := e.box.last(t)
	if !strings.HasPrefix(n.Token, "platform.") || n.TenantCode != "" {
		t.Fatalf("notice = %+v", n)
	}
	if res := e.reset(t, n.Token, "a-brand-new-platform-pw"); res.Status != 204 {
		t.Fatalf("reset: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodPost, "/api/v1/auth/platform/login", "", `{"username":"root","password":"a-brand-new-platform-pw"}`); res.Status != 200 {
		t.Fatalf("login: %d %s", res.Status, res.Raw)
	}
}

func TestPassword_Change(t *testing.T) {
	e := setupAccount(t)
	access, refresh := e.tokens(t, "tokoa", "alice", "password-of-alice-a")
	url := "/api/v1/auth/password/change"

	if res := call(t, e.r, http.MethodPost, url, "", `{"current_password":"x","new_password":"y"}`); res.Status != 401 {
		t.Fatalf("no token: %d", res.Status)
	}
	wrong := call(t, e.r, http.MethodPost, url, access, `{"current_password":"not-the-password","new_password":"a-brand-new-password-1"}`)
	if wrong.Status != 422 || wrong.Body.Error.Code != "validation_failed" {
		t.Fatalf("wrong current: %d %s", wrong.Status, wrong.Raw)
	}
	if res := call(t, e.r, http.MethodPost, url, access, `{"current_password":"password-of-alice-a","new_password":"short"}`); res.Status != 422 {
		t.Fatalf("weak: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, url, access, `{"current_password":"password-of-alice-a","new_password":"password-of-alice-a"}`); res.Status != 422 {
		t.Fatalf("same: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, url, access, `{"current_password":"password-of-alice-a","new_password":"a-brand-new-password-1"}`); res.Status != 204 {
		t.Fatalf("change: %d %s", res.Status, res.Raw)
	}
	if e.status(t, "tokoa", "a-brand-new-password-1") != 200 || e.status(t, "tokoa", "password-of-alice-a") != 401 {
		t.Fatal("the password did not change")
	}
	if res := call(t, e.r, http.MethodPost, "/api/v1/auth/refresh", "", `{"refresh_token":"`+refresh+`"}`); res.Status != 401 {
		t.Fatalf("the refresh token must be revoked: %d", res.Status)
	}
	// tenant B's Alice is a different person
	if e.status(t, "tokob", "password-of-alice-b") != 200 {
		t.Fatal("tenant B must not be affected")
	}
}

func TestPassword_ChangeGuessingIsLimited(t *testing.T) {
	e := setupAccount(t)
	access, _ := e.tokens(t, "tokoa", "alice", "password-of-alice-a")
	for i := 0; i < 3; i++ {
		call(t, e.r, http.MethodPost, "/api/v1/auth/password/change", access, `{"current_password":"guess","new_password":"a-brand-new-password-1"}`)
	}
	res := call(t, e.r, http.MethodPost, "/api/v1/auth/password/change", access, `{"current_password":"password-of-alice-a","new_password":"a-brand-new-password-1"}`)
	if res.Status != 429 {
		t.Fatalf("after 3 wrong guesses even the right password is refused: %d %s", res.Status, res.Raw)
	}
}

func TestLogin_RateLimitedOverHTTP(t *testing.T) {
	e := setupAccount(t)
	for i := 0; i < 3; i++ {
		if st := e.status(t, "tokoa", "wrong-password"); st != 401 {
			t.Fatalf("attempt %d: %d", i, st)
		}
	}
	res := call(t, e.r, http.MethodPost, "/api/v1/auth/login", "", `{"username":"alice","password":"password-of-alice-a"}`, "X-Tenant-Code", "tokoa")
	if res.Status != 429 || res.Body.Error.Code != "rate_limited" {
		t.Fatalf("want 429 rate_limited, got %d %s", res.Status, res.Raw)
	}
	// the same user name in the other tenant is a different account
	if e.status(t, "tokob", "password-of-alice-b") != 200 {
		t.Fatal("another tenant must not be blocked")
	}
	var sessions int64
	if err := e.owner.Raw("SELECT count(*) FROM user_tokens WHERE tenant_id = (SELECT tenant_id FROM tenants WHERE code='tokoa')").Scan(&sessions).Error; err != nil || sessions != 0 {
		t.Fatalf("a blocked login must not create a session: %d %v", sessions, err)
	}
}

func TestPasswordResets_AreIsolatedByRLS(t *testing.T) {
	e := setupAccount(t)
	e.forgot(t, "tokoa", "alice")
	e.forgot(t, "tokob", "alice")

	var total int64
	if err := e.owner.Raw("SELECT count(*) FROM password_resets").Scan(&total).Error; err != nil || total != 2 {
		t.Fatalf("rows = %d %v", total, err)
	}
	// without a tenant the application role sees nothing (fail closed)
	var seen int64
	if err := e.app.Raw("SELECT count(*) FROM password_resets").Scan(&seen).Error; err != nil || seen != 0 {
		t.Fatalf("a session without a tenant saw %d rows (%v)", seen, err)
	}
}

func fastHasher(t *testing.T) *password.Hasher {
	t.Helper()
	h, err := password.New(password.Params{MemoryKiB: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func jsonField(t *testing.T, raw, field string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("not JSON: %v: %s", err, raw)
	}
	v, _ := m[field].(string)
	if v == "" {
		t.Fatalf("no %q in %s", field, raw)
	}
	return v
}
