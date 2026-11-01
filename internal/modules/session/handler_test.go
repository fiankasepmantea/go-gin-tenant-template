package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
)

type fakeSvc struct {
	loginIn    LoginInput
	platformIn LoginInput
	refreshTok string
	logoutTok  string
	err        error
}

var okTokens = &Tokens{AccessToken: "acc", RefreshToken: "ref", ExpiresIn: 15 * time.Minute}

func (f *fakeSvc) Login(_ context.Context, in LoginInput) (*Tokens, error) {
	f.loginIn = in
	return okTokens, f.err
}

func (f *fakeSvc) LoginPlatform(_ context.Context, in LoginInput) (*Tokens, error) {
	f.platformIn = in
	return okTokens, f.err
}

func (f *fakeSvc) Refresh(_ context.Context, tok, _, _ string) (*Tokens, error) {
	f.refreshTok = tok
	return okTokens, f.err
}

func (f *fakeSvc) Logout(_ context.Context, tok string) error {
	f.logoutTok = tok
	return f.err
}

func newTestRouter(svc authService, mgr *auth.Manager) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	resolver := tenant.Chain(tenant.HeaderResolver{})
	NewHandler(svc, resolver, middleware.Authenticate(mgr), nil).RegisterRoutes(r.Group("/api/v1"))
	return r
}

func post(r http.Handler, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the error envelope: %q", w.Body.String())
	}
	return body.Error.Code
}

func TestHandler_LoginSuccess(t *testing.T) {
	svc := &fakeSvc{}
	r := newTestRouter(svc, nil)

	w := post(r, "/api/v1/auth/login", `{"tenant_code":"tokoa","username":"alice","password":"secret-password"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var got tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "acc" || got.RefreshToken != "ref" || got.TokenType != "Bearer" || got.ExpiresIn != 900 {
		t.Errorf("response = %+v", got)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, tokens must not be cached", cc)
	}
	if svc.loginIn.TenantCode != "tokoa" || svc.loginIn.Username != "alice" {
		t.Errorf("service input = %+v", svc.loginIn)
	}
}

func TestHandler_HeaderBeatsBodyForTenant(t *testing.T) {
	svc := &fakeSvc{}
	r := newTestRouter(svc, nil)

	post(r, "/api/v1/auth/login", `{"tenant_code":"from-body","username":"a","password":"b"}`, "X-Tenant-Code", "from-header")
	if svc.loginIn.TenantCode != "from-header" {
		t.Errorf("tenant = %q, the header must win over the body", svc.loginIn.TenantCode)
	}

	post(r, "/api/v1/auth/login", `{"tenant_code":"from-body","username":"a","password":"b"}`)
	if svc.loginIn.TenantCode != "from-body" {
		t.Errorf("tenant = %q, the body is the fallback", svc.loginIn.TenantCode)
	}
}

func TestHandler_PlatformLoginIgnoresTenant(t *testing.T) {
	svc := &fakeSvc{}
	r := newTestRouter(svc, nil)

	w := post(r, "/api/v1/auth/platform/login", `{"tenant_code":"tokoa","username":"root","password":"secret-password"}`, "X-Tenant-Code", "tokoa")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if svc.platformIn.TenantCode != "" || svc.platformIn.Username != "root" {
		t.Errorf("service input = %+v", svc.platformIn)
	}
}

func TestHandler_BadRequests(t *testing.T) {
	r := newTestRouter(&fakeSvc{}, nil)
	tests := []struct {
		name, path, body string
	}{
		{"not json", "/api/v1/auth/login", `nope`},
		{"empty body", "/api/v1/auth/login", ``},
		{"missing username", "/api/v1/auth/login", `{"password":"x"}`},
		{"missing password", "/api/v1/auth/login", `{"username":"x"}`},
		{"password too long", "/api/v1/auth/login", `{"username":"x","password":"` + strings.Repeat("a", 1025) + `"}`},
		{"tenant code too long", "/api/v1/auth/login", `{"tenant_code":"` + strings.Repeat("a", 64) + `","username":"x","password":"y"}`},
		{"body too large", "/api/v1/auth/login", `{"username":"x","password":"` + strings.Repeat("a", 20<<10) + `"}`},
		{"platform missing password", "/api/v1/auth/platform/login", `{"username":"x"}`},
		{"refresh missing token", "/api/v1/auth/refresh", `{}`},
		{"logout missing token", "/api/v1/auth/logout", `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := post(r, tc.path, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			if code := errorCode(t, w); code != "invalid_request" {
				t.Errorf("code = %q", code)
			}
		})
	}
}

func TestHandler_ErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"invalid credentials", ErrInvalidCredentials, http.StatusUnauthorized, "invalid_credentials"},
		{"wrapped invalid credentials", errors.Join(errors.New("x"), ErrInvalidCredentials), http.StatusUnauthorized, "invalid_credentials"},
		{"invalid refresh token", ErrInvalidRefreshToken, http.StatusUnauthorized, "invalid_refresh_token"},
		{"unexpected error is hidden", errors.New("pq: password authentication failed for user secret_db_user"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter(&fakeSvc{err: tc.err}, nil)
			for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/platform/login"} {
				w := post(r, path, `{"username":"a","password":"b"}`)
				if w.Code != tc.wantStatus || errorCode(t, w) != tc.wantCode {
					t.Errorf("%s: status=%d code=%q, want %d %q", path, w.Code, errorCode(t, w), tc.wantStatus, tc.wantCode)
				}
				if strings.Contains(w.Body.String(), "secret_db_user") {
					t.Errorf("%s: internal error details leaked to the client: %s", path, w.Body)
				}
			}
			w := post(r, "/api/v1/auth/refresh", `{"refresh_token":"x"}`)
			if w.Code != tc.wantStatus {
				t.Errorf("refresh status = %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}

func TestHandler_RefreshAndLogout(t *testing.T) {
	svc := &fakeSvc{}
	r := newTestRouter(svc, nil)

	if w := post(r, "/api/v1/auth/refresh", `{"refresh_token":"  tok-1  "}`); w.Code != http.StatusOK || svc.refreshTok != "tok-1" {
		t.Errorf("refresh: status=%d token=%q", w.Code, svc.refreshTok)
	}
	if w := post(r, "/api/v1/auth/logout", `{"refresh_token":"tok-2"}`); w.Code != http.StatusNoContent || svc.logoutTok != "tok-2" {
		t.Errorf("logout: status=%d token=%q", w.Code, svc.logoutTok)
	}
	svc.err = errors.New("boom")
	if w := post(r, "/api/v1/auth/logout", `{"refresh_token":"tok-3"}`); w.Code != http.StatusInternalServerError {
		t.Errorf("logout error status = %d", w.Code)
	}
}

func TestHandler_Me(t *testing.T) {
	mgr, err := auth.NewManager(auth.GenerateKeyHex(), "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRouter(&fakeSvc{}, mgr)
	tenantID := "0191a000-0000-7000-8000-000000000001"
	token, err := mgr.Issue(auth.Claims{UserID: "u-1", TenantID: tenantID, TokenID: "t-1", Level: auth.LevelTenant})
	if err != nil {
		t.Fatal(err)
	}

	get := func(authz string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/auth/me", nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := get("Bearer " + token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["user_id"] != "u-1" || got["tenant_id"] != tenantID || got["level"] != auth.LevelTenant {
		t.Errorf("body = %v", got)
	}

	for name, header := range map[string]string{
		"no header":    "",
		"wrong scheme": "Basic " + token,
		"empty bearer": "Bearer ",
		"garbage":      "Bearer not-a-token",
		"token only":   token,
	} {
		if w := get(header); w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: status=%d, want 401 with WWW-Authenticate", name, w.Code)
		}
	}
}
