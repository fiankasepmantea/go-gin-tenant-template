package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

func newEngine(mw ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw...)
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, RequestIDFrom(c)) })
	r.GET("/boom", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	return r
}

func TestRequestID_GeneratesAndEchoes(t *testing.T) {
	r := newEngine(RequestID())

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ping", nil))
	id := w.Header().Get(HeaderRequestID)
	if id == "" || w.Body.String() != id {
		t.Fatalf("header=%q body=%q", id, w.Body.String())
	}

	w = httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ping", nil)
	req.Header.Set(HeaderRequestID, "from-client")
	r.ServeHTTP(w, req)
	if got := w.Header().Get(HeaderRequestID); got != "from-client" {
		t.Errorf("the client request id must be used, got %q", got)
	}
}

func TestRequestID_RejectsOversized(t *testing.T) {
	r := newEngine(RequestID())
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ping", nil)
	req.Header.Set(HeaderRequestID, strings.Repeat("x", 500))
	r.ServeHTTP(w, req)
	if got := w.Header().Get(HeaderRequestID); len(got) > 128 {
		t.Errorf("oversized request id was used as-is: %d", len(got))
	}
}

func TestAccessLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	r := newEngine(RequestID(), AccessLog(log))

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ping", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/boom", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/nope", nil))

	out := buf.String()
	for _, want := range []string{`"route":"/ping"`, `"level":"ERROR"`, `"status":500`, `"route":"unmatched"`, `"level":"WARN"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log does not contain %s:\n%s", want, out)
		}
	}
}

type stubParser struct {
	claims *auth.Claims
	err    error
}

func (s stubParser) Parse(string) (*auth.Claims, error) { return s.claims, s.err }

func TestAuthenticate_SetsTenantFromTheToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenantID := "0191a000-0000-7000-8000-000000000001"
	tests := []struct {
		name       string
		claims     *auth.Claims
		wantTenant string
	}{
		{"tenant token", &auth.Claims{UserID: "u", TenantID: tenantID, TokenID: "t", Level: auth.LevelTenant}, tenantID},
		{"platform token has no tenant", &auth.Claims{UserID: "u", TokenID: "t", Level: auth.LevelPlatform}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotTenant string
			r := gin.New()
			r.GET("/x", Authenticate(stubParser{claims: tc.claims}), func(c *gin.Context) {
				gotTenant, _ = tenant.IDFrom(c.Request.Context())
				c.Status(http.StatusNoContent)
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
			req.Header.Set("Authorization", "bearer abc") // the scheme is case-insensitive
			req.Header.Set(tenant.DefaultHeader, "someone-elses-tenant")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d", w.Code)
			}
			if gotTenant != tc.wantTenant {
				t.Errorf("tenant in context = %q, want %q (the header must never be used)", gotTenant, tc.wantTenant)
			}
		})
	}
}

func TestAuthenticate_RejectsInvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	r := gin.New()
	r.GET("/x", Authenticate(stubParser{err: auth.ErrInvalidToken}), func(c *gin.Context) { called = true })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer abc")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized || called {
		t.Errorf("status=%d handlerCalled=%v", w.Code, called)
	}
}
