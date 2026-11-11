package grpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	authv1 "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc/gen/tenantapp/auth/v1"
)

type fakeTokens map[string]*auth.Claims

func (f fakeTokens) Parse(t string) (*auth.Claims, error) {
	if c, ok := f[t]; ok {
		return c, nil
	}
	return nil, auth.ErrInvalidToken
}

type fakeAuthorizer struct {
	decision auth.Decision
	err      error
	panics   bool
	gotCtx   context.Context
	gotClaim *auth.Claims
	method   string
	route    string
}

func (f *fakeAuthorizer) Authorize(ctx context.Context, c *auth.Claims, method, route string) (auth.Decision, error) {
	if f.panics {
		panic("boom")
	}
	f.gotCtx, f.gotClaim, f.method, f.route = ctx, c, method, route
	return f.decision, f.err
}

type env struct {
	client authv1.AuthServiceClient
	conn   *grpclib.ClientConn
	az     *fakeAuthorizer
	logs   *bytes.Buffer
	level  string // level the tx function saw
	txErr  error
}

const (
	tokTenant   = "tenant-token"
	tokPlatform = "platform-token"
	tenantID    = "11111111-1111-4111-8111-111111111111"
)

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{az: &fakeAuthorizer{}, logs: &bytes.Buffer{}}
	tokens := fakeTokens{
		tokTenant:   {UserID: "u1", TenantID: tenantID, TokenID: "s1", Level: auth.LevelTenant},
		tokPlatform: {UserID: "p1", TokenID: "s2", Level: auth.LevelPlatform},
	}
	log := slog.New(slog.NewTextHandler(e.logs, nil))
	tx := func(ctx context.Context, level string, fn func(context.Context) error) error {
		e.level = level
		if e.txErr != nil {
			return e.txErr
		}
		return fn(ctx)
	}
	as, err := NewAuthServer(tokens, e.az, tx, log)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Options{Auth: as, Log: log, Reflection: true})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Shutdown(time.Second) })

	conn, err := grpclib.NewClient("passthrough:///bufnet",
		grpclib.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpclib.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	e.conn, e.client = conn, authv1.NewAuthServiceClient(conn)
	return e
}

func ctx5(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %v (%v), want %v", got, err, want)
	}
}

func TestValidate(t *testing.T) {
	e := newEnv(t)
	r, err := e.client.Validate(ctx5(t), &authv1.ValidateRequest{AccessToken: tokTenant})
	if err != nil || r.GetUserId() != "u1" || r.GetTenantId() != tenantID || r.GetLevel() != auth.LevelTenant {
		t.Fatalf("tenant token: %+v %v", r, err)
	}
	r, err = e.client.Validate(ctx5(t), &authv1.ValidateRequest{AccessToken: "  " + tokPlatform + " "})
	if err != nil || r.GetTenantId() != "" || r.GetLevel() != auth.LevelPlatform {
		t.Fatalf("platform token: %+v %v", r, err)
	}
	_, err = e.client.Validate(ctx5(t), &authv1.ValidateRequest{AccessToken: "forged"})
	wantCode(t, err, codes.Unauthenticated)
	_, err = e.client.Validate(ctx5(t), &authv1.ValidateRequest{})
	wantCode(t, err, codes.InvalidArgument)
	_, err = e.client.Validate(ctx5(t), &authv1.ValidateRequest{AccessToken: strings.Repeat("x", maxTokenLen+1)})
	wantCode(t, err, codes.Unauthenticated)
}

func TestAuthorizeVerdicts(t *testing.T) {
	e := newEnv(t)
	call := func() (*authv1.AuthorizeResponse, error) {
		return e.client.Authorize(ctx5(t), &authv1.AuthorizeRequest{AccessToken: tokTenant, Method: "get", Path: "/api/v1/groups/:id"})
	}

	e.az.decision = auth.Decision{Verdict: auth.VerdictAllowed, Reason: "allowed"}
	if r, err := call(); err != nil || !r.GetAllowed() {
		t.Fatalf("allowed: %+v %v", r, err)
	}
	// what the decision maker was asked, and in which context
	if e.az.method != "GET" || e.az.route != "/api/v1/groups/:id" || e.az.gotClaim.UserID != "u1" {
		t.Errorf("asked %s %s for %+v", e.az.method, e.az.route, e.az.gotClaim)
	}
	if id, ok := tenant.IDFrom(e.az.gotCtx); !ok || id != tenantID {
		t.Errorf("the tenant of the token must be in the context, got %q %v", id, ok)
	}
	if e.level != auth.LevelTenant {
		t.Errorf("transaction level = %q", e.level)
	}

	e.az.decision = auth.Decision{Verdict: auth.VerdictForbidden, Reason: "not_granted"}
	if r, err := call(); err != nil || r.GetAllowed() {
		t.Fatalf("forbidden must be allowed=false without an error: %+v %v", r, err)
	}
	if !strings.Contains(e.logs.String(), "reason=not_granted") {
		t.Errorf("the reason of the refusal belongs in the log:\n%s", e.logs)
	}

	e.az.decision = auth.Decision{Verdict: auth.VerdictUnauthenticated, Reason: "user_inactive"}
	_, err := call()
	wantCode(t, err, codes.Unauthenticated)

	// a platform admin: no tenant in the context, level platform
	e.az.decision = auth.Decision{Verdict: auth.VerdictAllowed}
	if _, err := e.client.Authorize(ctx5(t), &authv1.AuthorizeRequest{AccessToken: tokPlatform, Method: "GET", Path: "/api/v1/tenants"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := tenant.IDFrom(e.az.gotCtx); ok || e.level != auth.LevelPlatform {
		t.Errorf("platform call: tenant in ctx=%v level=%q", ok, e.level)
	}
}

func TestAuthorizeFailsClosed(t *testing.T) {
	e := newEnv(t)
	req := &authv1.AuthorizeRequest{AccessToken: tokTenant, Method: "GET", Path: "/api/v1/groups"}

	e.az.err = errors.New("database is down")
	r, err := e.client.Authorize(ctx5(t), req)
	wantCode(t, err, codes.Internal)
	if r.GetAllowed() {
		t.Fatal("an error must never be an allow")
	}
	if strings.Contains(err.Error(), "database") {
		t.Errorf("internal details leaked to the caller: %v", err)
	}

	e.az.err = nil
	e.txErr = errors.New("cannot begin transaction")
	_, err = e.client.Authorize(ctx5(t), req)
	wantCode(t, err, codes.Internal)

	e.txErr = nil
	e.az.panics = true
	r, err = e.client.Authorize(ctx5(t), req)
	wantCode(t, err, codes.Internal)
	if r.GetAllowed() {
		t.Fatal("a panic must never be an allow")
	}
	// the server survives it
	e.az.panics = false
	e.az.decision = auth.Decision{Verdict: auth.VerdictAllowed}
	if r, err = e.client.Authorize(ctx5(t), req); err != nil || !r.GetAllowed() {
		t.Fatalf("after a panic: %+v %v", r, err)
	}
}

func TestAuthorizeRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	for label, req := range map[string]*authv1.AuthorizeRequest{
		"no method":      {AccessToken: tokTenant, Path: "/x"},
		"unknown method": {AccessToken: tokTenant, Method: "FETCH", Path: "/x"},
		"no path":        {AccessToken: tokTenant, Method: "GET"},
		"relative path":  {AccessToken: tokTenant, Method: "GET", Path: "api/v1/groups"},
		"path too long":  {AccessToken: tokTenant, Method: "GET", Path: "/" + strings.Repeat("a", maxPathLen)},
		"no token":       {Method: "GET", Path: "/x"},
		"checked first":  {AccessToken: "forged", Method: "NOPE", Path: "/x"},
	} {
		_, err := e.client.Authorize(ctx5(t), req)
		if c := status.Code(err); c != codes.InvalidArgument {
			t.Errorf("%s: code = %v", label, c)
		}
	}
	// a forged token is refused as Unauthenticated, and the decision maker is never asked
	e.az.gotClaim = nil
	_, err := e.client.Authorize(ctx5(t), &authv1.AuthorizeRequest{AccessToken: "forged", Method: "GET", Path: "/x"})
	wantCode(t, err, codes.Unauthenticated)
	if e.az.gotClaim != nil {
		t.Error("the authorizer was asked about a forged token")
	}
}

func TestTokensAreNeverLogged(t *testing.T) {
	e := newEnv(t)
	e.az.decision = auth.Decision{Verdict: auth.VerdictForbidden, Reason: "not_granted"}
	_, _ = e.client.Authorize(ctx5(t), &authv1.AuthorizeRequest{AccessToken: tokTenant, Method: "GET", Path: "/x"})
	_, _ = e.client.Validate(ctx5(t), &authv1.ValidateRequest{AccessToken: tokTenant})
	_, _ = e.client.Validate(ctx5(t), &authv1.ValidateRequest{AccessToken: "forged-token-value"})
	if out := e.logs.String(); strings.Contains(out, tokTenant) || strings.Contains(out, "forged-token-value") {
		t.Errorf("an access token reached the log:\n%s", out)
	}
	if !strings.Contains(e.logs.String(), "grpc_call") {
		t.Error("calls must be logged")
	}
}

func TestHealthAndUnknownService(t *testing.T) {
	e := newEnv(t)
	h := healthpb.NewHealthClient(e.conn)
	r, err := h.Check(ctx5(t), &healthpb.HealthCheckRequest{Service: authv1.AuthService_ServiceDesc.ServiceName})
	if err != nil || r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health: %+v %v", r, err)
	}
	_, err = h.Check(ctx5(t), &healthpb.HealthCheckRequest{Service: "nope"})
	wantCode(t, err, codes.NotFound)
}

func TestNewAuthServerRequiresItsParts(t *testing.T) {
	if _, err := NewAuthServer(nil, &fakeAuthorizer{}, DBTx(nil), nil); err == nil {
		t.Error("tokens are required")
	}
	if _, err := NewAuthServer(fakeTokens{}, nil, DBTx(nil), nil); err == nil {
		t.Error("an authorizer is required")
	}
	if _, err := NewAuthServer(fakeTokens{}, &fakeAuthorizer{}, nil, nil); err == nil {
		t.Error("a transaction function is required")
	}
	if _, err := NewServer(Options{}); err == nil {
		t.Error("NewServer needs the AuthServer")
	}
}
