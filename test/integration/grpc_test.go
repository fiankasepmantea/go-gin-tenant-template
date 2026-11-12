//go:build integration

package integration

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/rbac"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	grpcsrv "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc"
	authv1 "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc/gen/tenantapp/auth/v1"
)

// grpcClient serves the real AuthService (real rbac.Service, real transaction function, the application database
// role under RLS) over an in-memory connection.
func grpcClient(t *testing.T, e *rbacEnv) authv1.AuthServiceClient {
	t.Helper()
	az := rbac.NewService(endpoint.NewRepository(e.app), rbac.NewRepository(e.app),
		rbac.Options{EndpointTTL: time.Millisecond, AccessTTL: time.Millisecond})
	return serveAuth(t, e.mgr, az, grpcsrv.DBTx(e.app))
}

// serveAuth serves the AuthService over an in-memory connection.
func serveAuth(t *testing.T, mgr *auth.Manager, az grpcsrv.Authorizer, tx grpcsrv.TxFunc) authv1.AuthServiceClient {
	t.Helper()
	as, err := grpcsrv.NewAuthServer(mgr, az, tx, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := grpcsrv.NewServer(grpcsrv.Options{Auth: as})
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
	return authv1.NewAuthServiceClient(conn)
}

func allowed(t *testing.T, c authv1.AuthServiceClient, token, method, path string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := c.Authorize(ctx, &authv1.AuthorizeRequest{AccessToken: token, Method: method, Path: path})
	if err != nil {
		t.Fatalf("Authorize %s %s: %v", method, path, err)
	}
	return r.GetAllowed()
}

func TestGRPC_AuthService(t *testing.T) {
	e := setupRBAC(t)
	c := grpcClient(t, e)
	ctx := context.Background()
	tokA := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	tokB := login(t, e.r, "/api/v1/auth/login", "tokob", "alice", "password-of-alice-b")

	// Validate: who owns the token
	var tenantA string
	if err := e.owner.Raw("SELECT tenant_id FROM tenants WHERE code = 'tokoa'").Scan(&tenantA).Error; err != nil {
		t.Fatal(err)
	}
	v, err := c.Validate(ctx, &authv1.ValidateRequest{AccessToken: tokA})
	if err != nil || v.GetTenantId() != tenantA || v.GetLevel() != "tenant" || v.GetUserId() == "" {
		t.Fatalf("Validate: %+v %v", v, err)
	}
	if _, err := c.Validate(ctx, &authv1.ValidateRequest{AccessToken: tokA + "x"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a tampered token: %v", err)
	}

	// default deny: nothing is registered yet, then registered but not granted
	if allowed(t, c, tokA, "GET", "/api/v1/groups") {
		t.Fatal("unregistered endpoint must be refused")
	}
	e.sync(t)
	if allowed(t, c, tokA, "GET", "/api/v1/groups") {
		t.Fatal("a user without a group must be refused")
	}

	// grant GET /groups to tenant A's group only
	if _, err := e.prov.CreateGroup(ctx, "tokoa", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "alice", "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "viewer", false, []string{"GET /api/v1/groups"}); err != nil {
		t.Fatal(err)
	}
	settle()
	if !allowed(t, c, tokA, "GET", "/api/v1/groups") {
		t.Fatal("the granted endpoint must be allowed")
	}
	if !allowed(t, c, tokA, "get", " /api/v1/groups"[1:]) { // the method is case-insensitive
		t.Fatal("lower case method")
	}
	if allowed(t, c, tokA, "POST", "/api/v1/groups") {
		t.Fatal("an endpoint that was not granted must be refused")
	}
	if allowed(t, c, tokB, "GET", "/api/v1/groups") {
		t.Fatal("tenant B must not inherit the grants of tenant A")
	}

	// a platform admin has no access to tenant-scope endpoints
	pt := login(t, e.r, "/api/v1/auth/platform/login", "", "root", "password-of-the-root")
	if allowed(t, c, pt, "GET", "/api/v1/groups") {
		t.Fatal("platform token on a tenant endpoint must be refused")
	}
	if v, err := c.Validate(ctx, &authv1.ValidateRequest{AccessToken: pt}); err != nil || v.GetLevel() != "platform" || v.GetTenantId() != "" {
		t.Fatalf("Validate platform: %+v %v", v, err)
	}

	// an inactive user is Unauthenticated, as the REST API answers 401
	if err := e.owner.Exec("UPDATE users SET is_active = false WHERE username = 'alice' AND tenant_id = ?", tenantA).Error; err != nil {
		t.Fatal(err)
	}
	settle()
	_, err = c.Authorize(ctx, &authv1.AuthorizeRequest{AccessToken: tokA, Method: "GET", Path: "/api/v1/groups"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("inactive user: %v", err)
	}
}

// TestGRPC_AgreesWithREST asks both transports the same questions: they must never disagree.
func TestGRPC_AgreesWithREST(t *testing.T) {
	e := setupRBAC(t)
	c := grpcClient(t, e)
	ctx := context.Background()
	e.sync(t)
	if _, err := e.prov.CreateGroup(ctx, "tokoa", "editor", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "alice", "editor"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "editor", false,
		[]string{"GET /api/v1/groups", "POST /api/v1/groups", "GET /api/v1/groups/:id"}); err != nil {
		t.Fatal(err)
	}
	settle()
	tok := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")

	id := "00000000-0000-7000-8000-000000000001"
	for _, q := range []struct{ method, pattern, url, body string }{
		{http.MethodGet, "/api/v1/groups", "/api/v1/groups", ""},
		{http.MethodPost, "/api/v1/groups", "/api/v1/groups", `{"name":"x"}`},
		{http.MethodGet, "/api/v1/groups/:id", "/api/v1/groups/" + id, ""},
		{http.MethodPatch, "/api/v1/groups/:id", "/api/v1/groups/" + id, `{"name":"y"}`},
		{http.MethodDelete, "/api/v1/groups/:id", "/api/v1/groups/" + id, ""},
		{http.MethodGet, "/api/v1/rbac/endpoints", "/api/v1/rbac/endpoints", ""},
	} {
		st, _ := e.code(t, q.method, q.url, tok, q.body)
		rest := st != http.StatusForbidden && st != http.StatusUnauthorized
		if grpcAllowed := allowed(t, c, tok, q.method, q.pattern); grpcAllowed != rest {
			t.Errorf("%s %s: REST status %d, gRPC allowed=%v", q.method, q.pattern, st, grpcAllowed)
		}
	}
}
