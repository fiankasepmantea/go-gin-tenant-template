//go:build integration

package integration

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/app"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/cli"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/rbac"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/session"
	tenantmod "github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/migrate"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	grpcsrv "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc"
	authv1 "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc/gen/tenantapp/auth/v1"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
)

var migDir = filepath.Join("..", "..", "migrations")

func TestMigrate_Runner(t *testing.T) {
	e := setup(t) // tables exist (applied by hand), no history
	ctx := context.Background()

	st, err := migrate.Current(ctx, e.owner)
	if err != nil || st.Version != 0 || !st.HasData {
		t.Fatalf("hand-made database: %+v %v", st, err)
	}
	if _, err := migrate.Up(ctx, e.owner, migDir); err == nil {
		t.Fatal("Up must refuse a database with tables and no history")
	}
	all, _ := migrate.Load(migDir)
	latest := migrate.Latest(all)
	if err := migrate.Baseline(ctx, e.owner, migDir, latest); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Baseline(ctx, e.owner, migDir, latest); err == nil {
		t.Fatal("a second baseline must be refused")
	}
	if applied, err := migrate.Up(ctx, e.owner, migDir); err != nil || len(applied) != 0 {
		t.Fatalf("after baseline: %v %v", applied, err)
	}
	if err := migrate.Baseline(ctx, e.owner, migDir, 999999); err == nil {
		t.Fatal("unknown version must be refused")
	}
}

func TestMigrate_FreshDatabaseAndConcurrency(t *testing.T) {
	e := setup(t)
	// an empty database: drop everything setup() applied
	if err := e.owner.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ { // three deploys at the same moment
		go func() { _, err := migrate.Up(ctx, e.owner, migDir); errs <- err }()
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	all, _ := migrate.Load(migDir)
	st, err := migrate.Current(ctx, e.owner)
	if err != nil || st.Version != migrate.Latest(all) || st.Dirty {
		t.Fatalf("state: %+v %v", st, err)
	}
	var n int
	if err := e.owner.Raw("SELECT count(*) FROM tenants").Scan(&n).Error; err != nil {
		t.Fatalf("tables must exist: %v", err)
	}
}

type dedEnv struct {
	shared, ded *env
	router      *database.Router
	prov        *cli.Provisioner
	mgr         *auth.Manager
	api         http.Handler
}

func setupDedicated(t *testing.T) *dedEnv {
	t.Helper()
	shared, ded := setup(t), setup(t)
	ctx := context.Background()
	all, _ := migrate.Load(migDir)
	if err := migrate.Baseline(ctx, ded.owner, migDir, migrate.Latest(all)); err != nil {
		t.Fatal(err)
	}
	// the application role reads the version (a table made by the migration owner needs this grant)
	if err := ded.owner.Exec("GRANT SELECT ON schema_migrations TO " + appRole).Error; err != nil {
		t.Fatal(err)
	}
	router := database.NewRouter(database.RouterConfig{
		Control: shared.app, URLs: map[string]string{"acme": ded.appURL},
		Pool: database.Pool{MaxOpen: 5, MaxIdle: 1}, LocateTTL: time.Millisecond, RetryAfter: time.Millisecond,
	})
	t.Cleanup(router.Close)

	hasher, err := password.New(password.Params{MemoryKiB: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.NewManager(auth.GenerateKeyHex(), "it", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := session.NewService(session.Deps{
		Tx: router.InTenantTx, Tenants: tenantmod.NewRepository(shared.app), Users: user.NewRepository(shared.app),
		Tokens: session.NewRepository(shared.app), Hasher: hasher, Issuer: mgr,
		AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	ropts := rbac.Options{EndpointTTL: time.Millisecond, AccessTTL: time.Millisecond}
	modules := app.Modules(app.Deps{
		Log: slog.New(slog.DiscardHandler), DB: shared.app, Router: router, Tokens: mgr, Sessions: svc,
		Resolver: tenantctx.Chain(tenantctx.HeaderResolver{}), RBAC: ropts,
	})
	api := rest.NewRouter(&config.Config{Env: "testing"}, slog.New(slog.DiscardHandler), shared.app, modules...)

	prov := cli.NewProvisionerWithRouter(router, hasher, migDir)
	if _, err := prov.SyncEndpoints(ctx, app.Routes(modules), false); err != nil {
		t.Fatal(err)
	}
	return &dedEnv{shared: shared, ded: ded, router: router, prov: prov, mgr: mgr, api: api}
}

// provision creates a tenant (shared or dedicated), an admin group with every grant and a user in it.
func (d *dedEnv) provision(t *testing.T, code, ref string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := d.prov.CreateTenantIn(ctx, code, "Tenant "+code, ref); err != nil {
		t.Fatalf("create %s: %v", code, err)
	}
	if _, err := d.prov.CreateTenantUser(ctx, code, "alice", "alice@"+code+".test", "Alice", "password-of-"+code); err != nil {
		t.Fatal(err)
	}
	if _, err := d.prov.CreateGroup(ctx, code, "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.prov.SetUserGroup(ctx, code, "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.prov.GrantEndpoints(ctx, code, "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	return login(t, d.api, "/api/v1/auth/login", code, "alice", "password-of-"+code)
}

func rows(t *testing.T, e *env, q string) int {
	t.Helper()
	var n int
	if err := e.owner.Raw(q).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDedicated_TenantsLiveInTheirOwnDatabase(t *testing.T) {
	d := setupDedicated(t)
	tokPlain := d.provision(t, "plain", "")
	tokAcme := d.provision(t, "acme", "acme")

	// where the rows are: acme's user, group and session only in the dedicated database
	if n := rows(t, d.shared, "SELECT count(*) FROM users u JOIN tenants t USING (tenant_id) WHERE t.code = 'acme'"); n != 0 {
		t.Fatalf("acme users in the shared database: %d", n)
	}
	if n := rows(t, d.ded, "SELECT count(*) FROM users u JOIN tenants t USING (tenant_id) WHERE t.code = 'acme'"); n != 1 {
		t.Fatalf("acme users in its own database: %d", n)
	}
	if n := rows(t, d.shared, "SELECT count(*) FROM users u JOIN tenants t USING (tenant_id) WHERE t.code = 'plain'"); n != 1 {
		t.Fatalf("plain users in the shared database: %d", n)
	}
	if n := rows(t, d.ded, "SELECT count(*) FROM tenants"); n != 1 {
		t.Fatalf("the dedicated database holds only its own tenant row: %d", n)
	}
	if n := rows(t, d.shared, "SELECT count(*) FROM tenants WHERE code = 'acme' AND db_ref = 'acme'"); n != 1 {
		t.Fatalf("control row with db_ref: %d", n)
	}
	// the endpoints were mirrored with the same ids
	if a, b := rows(t, d.shared, "SELECT count(*) FROM endpoints"), rows(t, d.ded, "SELECT count(*) FROM endpoints"); a == 0 || a != b {
		t.Fatalf("endpoints shared=%d dedicated=%d", a, b)
	}

	// the API works for both, through the same code
	for name, tok := range map[string]string{"plain": tokPlain, "acme": tokAcme} {
		if st, _ := callCode(t, d.api, http.MethodPost, tok, `{"name":"sales-`+name+`"}`); st != 201 {
			t.Fatalf("%s: create group: %d", name, st)
		}
		if st, _ := callCode(t, d.api, http.MethodGet, tok, ""); st != 200 {
			t.Fatalf("%s: list groups: %d", name, st)
		}
	}
	if n := rows(t, d.ded, "SELECT count(*) FROM groups WHERE name = 'sales-acme'"); n != 1 {
		t.Fatalf("group of acme must be in the dedicated database: %d", n)
	}
	if n := rows(t, d.shared, "SELECT count(*) FROM groups WHERE name = 'sales-acme'"); n != 0 {
		t.Fatalf("group of acme leaked into the shared database: %d", n)
	}
	// the list of acme shows only acme's groups
	res := call(t, d.api, http.MethodGet, "/api/v1/groups", tokAcme, "")
	if res.Status != 200 || strings.Contains(res.Raw, "sales-plain") {
		t.Fatalf("isolation: %s", res.Raw)
	}
}

func callCode(t *testing.T, r http.Handler, method, token, body string) (int, string) {
	t.Helper()
	res := call(t, r, method, "/api/v1/groups", token, body)
	return res.Status, res.Body.Error.Code
}

func TestDedicated_OutageAffectsOnlyThatTenant(t *testing.T) {
	d := setupDedicated(t)
	tokPlain := d.provision(t, "plain", "")
	tokAcme := d.provision(t, "acme", "acme")

	d.ded.stop() // the dedicated database goes down
	settle()

	if st, code := callCode(t, d.api, http.MethodGet, tokAcme, ""); st != 503 || code != "service_unavailable" {
		t.Fatalf("acme during the outage: %d %s", st, code)
	}
	if st, _ := callCode(t, d.api, http.MethodGet, tokPlain, ""); st != 200 {
		t.Fatalf("plain must keep working: %d", st)
	}
	if err := d.router.InTenantTx(tenantctx.WithID(context.Background(), "00000000-0000-0000-0000-000000000000"),
		auth.LevelTenant, func(context.Context) error { return nil }); err != nil && errors.Is(err, database.ErrUnavailable) {
		t.Fatalf("an unknown tenant is served by the shared database: %v", err)
	}
}

func TestDedicated_CreateTenantRefusals(t *testing.T) {
	d := setupDedicated(t)
	ctx := context.Background()
	if _, err := d.prov.CreateTenantIn(ctx, "x1", "X", "nope"); err == nil {
		t.Fatal("a ref without a URL must be refused")
	}
	if _, err := d.prov.CreateTenantIn(ctx, "x2", "X", "Bad Ref"); err == nil {
		t.Fatal("an invalid ref must be refused")
	}
	// a database that is not migrated to the latest version
	if err := d.ded.owner.Exec("UPDATE schema_migrations SET version = 1").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := d.prov.CreateTenantIn(ctx, "x3", "X", "acme"); err == nil {
		t.Fatal("a database behind the migrations must be refused")
	}
	if n := rows(t, d.shared, "SELECT count(*) FROM tenants WHERE code IN ('x1','x2','x3')"); n != 0 {
		t.Fatalf("a refused tenant must not leave a row behind: %d", n)
	}
}

func TestDedicated_FailedCopyIsUndone(t *testing.T) {
	d := setupDedicated(t)
	ctx := context.Background()
	// make the copy in the dedicated database fail: a tenant with the same code is already there
	if err := d.ded.owner.Exec(`INSERT INTO tenants (tenant_id, code, name, status) VALUES (gen_random_uuid(), 'clash', 'C', 'active')`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := d.prov.CreateTenantIn(ctx, "clash", "C", "acme"); err == nil {
		t.Fatal("expected a failure")
	}
	if n := rows(t, d.shared, "SELECT count(*) FROM tenants WHERE code = 'clash'"); n != 0 {
		t.Fatalf("the control row must be removed after a failed copy: %d", n)
	}
}

func TestDedicated_SyncMirrorsNewEndpoints(t *testing.T) {
	d := setupDedicated(t)
	d.provision(t, "acme", "acme")
	// an endpoint appears in the shared database only (as after a deploy), then sync mirrors it
	if err := d.shared.owner.Exec(`INSERT INTO endpoints (endpoint_id, method, path, name, scope, is_public)
		VALUES (gen_random_uuid(), 'GET', '/api/v1/extra', 'extra', 'tenant', false)`).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := d.prov.MirrorEndpoints(context.Background()); err != nil || n != 1 {
		t.Fatalf("mirror: %d %v", n, err)
	}
	if n := rows(t, d.ded, "SELECT count(*) FROM endpoints WHERE path = '/api/v1/extra'"); n != 1 {
		t.Fatalf("mirrored: %d", n)
	}
	// nothing changed: nothing written
	if n, err := d.prov.MirrorEndpoints(context.Background()); err != nil || n != 0 {
		t.Fatalf("idempotent: %d %v", n, err)
	}
}

func TestDedicated_GRPCAgreesWithREST(t *testing.T) {
	d := setupDedicated(t)
	tokPlain := d.provision(t, "plain", "")
	tokAcme := d.provision(t, "acme", "acme")
	az := rbac.NewService(endpoint.NewRepository(d.shared.app), rbac.NewRepository(d.shared.app),
		rbac.Options{EndpointTTL: time.Millisecond, AccessTTL: time.Millisecond})
	c := serveAuth(t, d.mgr, az, grpcsrv.RouterTx(d.router))

	for name, tok := range map[string]string{"plain": tokPlain, "acme": tokAcme} {
		if !allowed(t, c, tok, "GET", "/api/v1/groups") {
			t.Fatalf("%s: gRPC must allow what REST allows", name)
		}
	}
	d.ded.stop()
	settle()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Authorize(ctx, &authv1.AuthorizeRequest{AccessToken: tokAcme, Method: "GET", Path: "/api/v1/groups"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("outage must be Unavailable, got %v", err)
	}
	if !allowed(t, c, tokPlain, "GET", "/api/v1/groups") {
		t.Fatal("the shared tenant must keep working")
	}
}

func TestDedicated_ReadyzListsTenantDatabases(t *testing.T) {
	d := setupDedicated(t)
	h := rest.NewRouterWith(rest.Options{Config: &config.Config{Env: "testing"}, Log: slog.New(slog.DiscardHandler),
		DB: d.shared.app, Router: d.router})
	res := call(t, h, http.MethodGet, "/readyz", "", "")
	if res.Status != 200 || !strings.Contains(res.Raw, `"acme":"ok"`) {
		t.Fatalf("up: %d %s", res.Status, res.Raw)
	}
	d.ded.stop()
	res = call(t, h, http.MethodGet, "/readyz", "", "")
	// one dedicated database down is reported, but does not take the instance out of rotation
	if res.Status != 200 || !strings.Contains(res.Raw, `"acme":"unavailable"`) {
		t.Fatalf("down: %d %s", res.Status, res.Raw)
	}
}
