package middleware

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
)

type fakeAuthorizer struct {
	decision auth.Decision
	err      error
	gotRoute string
	gotVerb  string
	gotUser  string
}

func (f *fakeAuthorizer) Authorize(_ context.Context, c *auth.Claims, method, route string) (auth.Decision, error) {
	f.gotRoute, f.gotVerb, f.gotUser = route, method, c.UserID
	return f.decision, f.err
}

type staticParser struct{ claims *auth.Claims }

func (p staticParser) Parse(string) (*auth.Claims, error) { return p.claims, nil }

func authzRouter(az Authorizer, log *slog.Logger, withAuthn bool, reached *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	claims := &auth.Claims{UserID: "u1", TenantID: "0198f6a0-0000-7000-8000-00000000000a", Level: auth.LevelTenant}
	chain := []gin.HandlerFunc{}
	if withAuthn {
		chain = append(chain, Authenticate(staticParser{claims}))
	}
	chain = append(chain, Authorize(az, log), func(c *gin.Context) { *reached = true; c.Status(http.StatusNoContent) })
	r.GET("/things/:id", chain...)
	return r
}

func doAuthz(r *gin.Engine, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAuthorizeMiddleware(t *testing.T) {
	boom := errors.New("pq: connection reset, host=db-internal-7")
	tests := []struct {
		name        string
		az          *fakeAuthorizer
		wantStatus  int
		wantCode    string
		wantReached bool
	}{
		{"allowed", &fakeAuthorizer{decision: auth.Decision{Verdict: auth.VerdictAllowed}}, 204, "", true},
		{"forbidden", &fakeAuthorizer{decision: auth.Decision{Verdict: auth.VerdictForbidden, Reason: "not_granted"}}, 403, "forbidden", false},
		{"unauthenticated", &fakeAuthorizer{decision: auth.Decision{Verdict: auth.VerdictUnauthenticated, Reason: "user_inactive"}}, 401, "unauthorized", false},
		{"error fails closed", &fakeAuthorizer{err: boom}, 500, "internal_error", false},
		{"zero decision is a refusal", &fakeAuthorizer{}, 403, "forbidden", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reached bool
			w := doAuthz(authzRouter(tt.az, nil, true, &reached), "/things/0198f6a0-1c2d-7abc-8def-0123456789ab")
			if w.Code != tt.wantStatus || reached != tt.wantReached {
				t.Fatalf("status %d reached=%v body=%s", w.Code, reached, w.Body)
			}
			if tt.wantCode != "" && !strings.Contains(w.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Errorf("body = %s, want code %s", w.Body, tt.wantCode)
			}
			for _, secret := range []string{"db-internal-7", "not_granted", "user_inactive"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Errorf("the response leaks %q: %s", secret, w.Body)
				}
			}
		})
	}
}

func TestAuthorizeUsesTheRoutePatternNotTheURL(t *testing.T) {
	az := &fakeAuthorizer{decision: auth.Decision{Verdict: auth.VerdictAllowed}}
	var reached bool
	doAuthz(authzRouter(az, nil, true, &reached), "/things/0198f6a0-1c2d-7abc-8def-0123456789ab")
	if az.gotRoute != "/things/:id" || az.gotVerb != "GET" || az.gotUser != "u1" {
		t.Errorf("authorizer got route=%q method=%q user=%q; it must get the pattern", az.gotRoute, az.gotVerb, az.gotUser)
	}
}

func TestAuthorizeWithoutAuthenticateRefuses(t *testing.T) {
	az := &fakeAuthorizer{decision: auth.Decision{Verdict: auth.VerdictAllowed}}
	var reached bool
	w := doAuthz(authzRouter(az, nil, false, &reached), "/things/x")
	if w.Code != 401 || reached {
		t.Errorf("status %d reached=%v: Authorize without Authenticate must refuse", w.Code, reached)
	}
}

func TestAuthorizeLogsTheReasonButOnlyForRefusals(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	var reached bool

	allow := &fakeAuthorizer{decision: auth.Decision{Verdict: auth.VerdictAllowed, Reason: "allowed"}}
	doAuthz(authzRouter(allow, log, true, &reached), "/things/x")
	if buf.Len() != 0 {
		t.Errorf("an allowed request must not be logged here: %s", buf.String())
	}

	deny := &fakeAuthorizer{decision: auth.Decision{Verdict: auth.VerdictForbidden, Reason: "not_granted"}}
	doAuthz(authzRouter(deny, log, true, &reached), "/things/x")
	out := buf.String()
	for _, want := range []string{"access_denied", "reason=not_granted", "user_id=u1", "route=/things/:id"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
}
