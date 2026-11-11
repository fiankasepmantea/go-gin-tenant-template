package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
)

type fakeAccount struct {
	reqIn    ResetRequest
	resetTok string
	resetPw  string
	changeIn ChangeInput
	err      error
	called   int
}

func (f *fakeAccount) RequestReset(_ context.Context, in ResetRequest) error {
	f.called++
	f.reqIn = in
	return f.err
}

func (f *fakeAccount) ResetPassword(_ context.Context, tok, pw, _ string) error {
	f.called++
	f.resetTok, f.resetPw = tok, pw
	return f.err
}

func (f *fakeAccount) ChangePassword(_ context.Context, in ChangeInput) error {
	f.called++
	f.changeIn = in
	return f.err
}

func accountRouter(acct accountService, mgr *auth.Manager) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewHandler(&fakeSvc{}, tenant.Chain(tenant.HeaderResolver{}), middleware.Authenticate(mgr), nil)
	if acct != nil {
		h.WithAccount(acct)
	}
	h.RegisterRoutes(r.Group("/api/v1"))
	return r
}

func TestPasswordRoutesExistOnlyWithAnAccountService(t *testing.T) {
	w := post(accountRouter(nil, nil), "/api/v1/auth/password/forgot", `{"identifier":"a"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("without WithAccount the route must not exist, got %d", w.Code)
	}
}

func TestHandler_Forgot(t *testing.T) {
	acct := &fakeAccount{}
	r := accountRouter(acct, nil)

	w := post(r, "/api/v1/auth/password/forgot", `{"tenant_code":"from-body","identifier":"alice"}`, "X-Tenant-Code", "from-header")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var body struct {
		Success bool   `json:"success"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !body.Success || body.Code != 202 || body.Message == "" {
		t.Fatalf("body = %s (%v)", w.Body, err)
	}
	if acct.reqIn.TenantCode != "from-header" || acct.reqIn.Identifier != "alice" || acct.reqIn.Platform {
		t.Errorf("input = %+v", acct.reqIn)
	}

	// the platform endpoint ignores any tenant
	post(r, "/api/v1/auth/platform/password/forgot", `{"tenant_code":"x","identifier":"root"}`, "X-Tenant-Code", "y")
	if !acct.reqIn.Platform {
		t.Errorf("platform input = %+v", acct.reqIn)
	}

	// a service failure that is not a rate limit is a 500 without details
	acct.err = errors.New("db down: secret-detail")
	w = post(r, "/api/v1/auth/password/forgot", `{"identifier":"alice"}`)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret-detail") {
		t.Errorf("status = %d, body = %s", w.Code, w.Body)
	}
}

func TestHandler_ForgotValidation(t *testing.T) {
	acct := &fakeAccount{}
	r := accountRouter(acct, nil)
	for _, body := range []string{``, `{}`, `{"identifier":""}`, `{"identifier":"` + strings.Repeat("a", 300) + `"}`, `not json`} {
		if w := post(r, "/api/v1/auth/password/forgot", body); w.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d", body, w.Code)
		}
	}
	if acct.called != 0 {
		t.Error("the service must not be called for an invalid body")
	}
}

func TestHandler_Reset(t *testing.T) {
	acct := &fakeAccount{}
	r := accountRouter(acct, nil)

	w := post(r, "/api/v1/auth/password/reset", `{"token":" tok.en ","new_password":"a-new-password-1"}`)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if acct.resetTok != "tok.en" || acct.resetPw != "a-new-password-1" {
		t.Errorf("service input = %q %q", acct.resetTok, acct.resetPw)
	}

	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ErrInvalidResetToken: {400, "invalid_request"},
		fmt.Errorf("%w: %w", ErrWeakPassword, password.ErrTooShort): {422, "validation_failed"},
		&RateLimitedError{RetryAfter: 90 * time.Second}:             {429, "rate_limited"},
	} {
		acct.err = err
		w = post(r, "/api/v1/auth/password/reset", `{"token":"t.k","new_password":"x"}`)
		if w.Code != want.status || errorCode(t, w) != want.code {
			t.Errorf("%v: %d %s, want %d %s", err, w.Code, w.Body, want.status, want.code)
		}
		if want.status == 429 && w.Header().Get("Retry-After") != "90" {
			t.Errorf("Retry-After = %q", w.Header().Get("Retry-After"))
		}
	}
}

func TestHandler_ChangeNeedsAToken(t *testing.T) {
	mgr, _ := auth.NewManager(auth.GenerateKeyHex(), "t", 15*time.Minute)
	acct := &fakeAccount{}
	r := accountRouter(acct, mgr)

	body := `{"current_password":"old-password-1","new_password":"new-password-2"}`
	if w := post(r, "/api/v1/auth/password/change", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	if acct.called != 0 {
		t.Fatal("the service must not be reached without a token")
	}

	tok, _ := mgr.Issue(auth.Claims{UserID: "u1", TenantID: "t1", TokenID: "j1", Level: auth.LevelTenant})
	w := post(r, "/api/v1/auth/password/change", body, "Authorization", "Bearer "+tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	// the identity comes from the token, nothing in the body can change it
	if acct.changeIn.UserID != "u1" || acct.changeIn.TenantID != "t1" || acct.changeIn.Level != auth.LevelTenant ||
		acct.changeIn.Current != "old-password-1" || acct.changeIn.New != "new-password-2" {
		t.Errorf("input = %+v", acct.changeIn)
	}

	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ErrWrongPassword:      {422, "validation_failed"},
		ErrAccountUnavailable: {401, "unauthorized"},
		fmt.Errorf("%w: the new password must differ", ErrWeakPassword): {422, "validation_failed"},
	} {
		acct.err = err
		w = post(r, "/api/v1/auth/password/change", body, "Authorization", "Bearer "+tok)
		if w.Code != want.status || errorCode(t, w) != want.code {
			t.Errorf("%v: %d %s", err, w.Code, w.Body)
		}
	}
}

func TestHandler_LoginRateLimited(t *testing.T) {
	svc := &fakeSvc{err: &RateLimitedError{RetryAfter: 1500 * time.Millisecond}}
	r := newTestRouter(svc, nil)
	w := post(r, "/api/v1/auth/login", `{"tenant_code":"t","username":"a","password":"b"}`)
	if w.Code != http.StatusTooManyRequests || errorCode(t, w) != "rate_limited" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Retry-After"); got != "2" { // rounded up
		t.Errorf("Retry-After = %q", got)
	}
}
