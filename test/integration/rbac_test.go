//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/app"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/rbac"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
)

// rbacEnv serves the real module list (app.Modules): Authenticate -> TenantTx -> Authorize.
type rbacEnv struct {
	*authEnv
	r      http.Handler
	routes []endpoint.Route
}

func setupRBAC(t *testing.T) *rbacEnv {
	t.Helper()
	a := setupAuth(t)
	a.seed(t)
	gin.SetMode(gin.TestMode)
	modules := app.Modules(app.Deps{
		Log: slog.New(slog.DiscardHandler), DB: a.app, Tokens: a.mgr, Sessions: a.svc,
		Resolver: tenantctx.Chain(tenantctx.HeaderResolver{}),
		// tiny lifetimes: changes made by the CLI (another process in production) show up quickly
		RBAC: rbac.Options{EndpointTTL: time.Millisecond, AccessTTL: time.Millisecond},
	})
	r := rest.NewRouter(&config.Config{Env: "testing"}, slog.New(slog.DiscardHandler), a.app, modules...)
	return &rbacEnv{authEnv: a, r: r, routes: app.Routes(modules)}
}

func (e *rbacEnv) sync(t *testing.T) {
	t.Helper()
	if _, err := e.prov.SyncEndpoints(context.Background(), e.routes, false); err != nil {
		t.Fatal(err)
	}
}

// settle waits longer than the cache lifetimes of setupRBAC.
func settle() { time.Sleep(10 * time.Millisecond) }

func (e *rbacEnv) code(t *testing.T, method, target, token, body string) (int, string) {
	t.Helper()
	res := call(t, e.r, method, target, token, body)
	return res.Status, res.Body.Error.Code
}

func TestRBAC_DefaultDenyAndGrants(t *testing.T) {
	e := setupRBAC(t)
	ctx := context.Background()
	tok := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")

	// nothing synchronized yet: every non-/auth route is refused
	if st, code := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 403 || code != "forbidden" {
		t.Fatalf("unregistered endpoint: %d %s", st, code)
	}

	e.sync(t)
	// registered, but the user has no group
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 403 {
		t.Fatalf("user without group: %d", st)
	}

	if _, err := e.prov.CreateGroup(ctx, "tokoa", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "alice", "viewer"); err != nil {
		t.Fatal(err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 403 {
		t.Fatalf("group without grants: %d", st)
	}

	n, err := e.prov.GrantEndpoints(ctx, "tokoa", "viewer", false, []string{"GET /api/v1/groups"})
	if err != nil || n != 1 {
		t.Fatalf("grant: %d %v", n, err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 200 {
		t.Fatalf("granted read: %d", st)
	}
	// the grant is for GET only
	if st, _ := e.code(t, http.MethodPost, "/api/v1/groups", tok, `{"name":"x"}`); st != 403 {
		t.Fatalf("other method: %d", st)
	}
	// granting again adds, never removes
	n, err = e.prov.GrantEndpoints(ctx, "tokoa", "viewer", false, []string{"POST /api/v1/groups"})
	if err != nil || n != 2 {
		t.Fatalf("second grant: %d %v", n, err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 200 {
		t.Fatalf("first grant must stay: %d", st)
	}
	if st, _ := e.code(t, http.MethodPost, "/api/v1/groups", tok, `{"name":"x"}`); st != 201 {
		t.Fatalf("second grant: %d", st)
	}
	// unknown endpoint spec is an error, not a silent no-op
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "viewer", false, []string{"GET /api/v1/nope"}); err == nil {
		t.Fatal("unknown endpoint must be an error")
	}
}

func TestRBAC_TenantIsolationOfGrants(t *testing.T) {
	e := setupRBAC(t)
	ctx := context.Background()
	e.sync(t)
	for _, c := range []string{"tokoa", "tokob"} {
		if _, err := e.prov.CreateGroup(ctx, c, "admin", ""); err != nil {
			t.Fatal(err)
		}
		if err := e.prov.SetUserGroup(ctx, c, "alice", "admin"); err != nil {
			t.Fatal(err)
		}
	}
	// only tenant A's admin group gets access
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	tokA := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	tokB := login(t, e.r, "/api/v1/auth/login", "tokob", "alice", "password-of-alice-b")
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tokA, ""); st != 200 {
		t.Fatalf("tenant A: %d", st)
	}
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tokB, ""); st != 403 {
		t.Fatalf("tenant B must not inherit the grants of A: %d", st)
	}
}

func TestRBAC_UserStateChangesApply(t *testing.T) {
	e := setupRBAC(t)
	ctx := context.Background()
	e.sync(t)
	if _, err := e.prov.CreateGroup(ctx, "tokoa", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	tok := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 200 {
		t.Fatalf("baseline: %d", st)
	}

	// deleting the group removes its power
	if err := e.owner.Exec("UPDATE groups SET deleted_at = now() WHERE name = 'admin'").Error; err != nil {
		t.Fatal(err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 403 {
		t.Fatalf("deleted group: %d", st)
	}
	if err := e.owner.Exec("UPDATE groups SET deleted_at = NULL WHERE name = 'admin'").Error; err != nil {
		t.Fatal(err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 200 {
		t.Fatalf("restored group: %d", st)
	}

	// an inactive user is refused even with a valid token
	if err := e.owner.Exec("UPDATE users SET is_active = false WHERE username = 'alice' AND tenant_id IS NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	settle()
	if st, code := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 401 {
		t.Fatalf("inactive user: %d %s", st, code)
	}
	if err := e.owner.Exec("UPDATE users SET is_active = true WHERE username = 'alice'").Error; err != nil {
		t.Fatal(err)
	}
	// a deleted user
	if err := e.owner.Exec("UPDATE users SET deleted_at = now() WHERE username = 'alice' AND tenant_id IS NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 401 {
		t.Fatalf("deleted user: %d", st)
	}
}

func TestRBAC_PublicAndPlatformScope(t *testing.T) {
	e := setupRBAC(t)
	ctx := context.Background()
	e.sync(t)
	if _, err := e.prov.CreateGroup(ctx, "tokoa", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	tok := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")

	// a public endpoint needs no grant (but still a valid login)
	if err := e.owner.Exec("UPDATE endpoints SET is_public = true WHERE method = 'GET' AND path = '/api/v1/groups'").Error; err != nil {
		t.Fatal(err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", tok, ""); st != 200 {
		t.Fatalf("public endpoint: %d", st)
	}
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", "", ""); st != 401 {
		t.Fatalf("public endpoint without a token: %d", st)
	}
	// public endpoints cannot be granted through the CLI: there is nothing to grant
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "admin", false, []string{"GET /api/v1/groups"}); err == nil {
		t.Fatal("a public endpoint is not grantable")
	}

	// a platform admin has no access to tenant-scope endpoints
	pt := login(t, e.r, "/api/v1/auth/platform/login", "", "root", "password-of-the-root")
	if st, _ := e.code(t, http.MethodGet, "/api/v1/groups", pt, ""); st == 200 {
		t.Fatalf("platform token on a tenant endpoint: %d", st)
	}
	// a tenant user has no access to platform-scope endpoints
	if err := e.owner.Exec("UPDATE endpoints SET scope = 'platform', is_public = false WHERE method = 'GET' AND path = '/api/v1/rbac/endpoints'").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, "/api/v1/rbac/endpoints", tok, ""); st != 403 {
		t.Fatalf("tenant user on a platform endpoint: %d", st)
	}
}

func TestRBAC_PermissionsAPI(t *testing.T) {
	e := setupRBAC(t)
	ctx := context.Background()
	e.sync(t)
	for _, c := range []string{"tokoa", "tokob"} {
		if _, err := e.prov.CreateGroup(ctx, c, "admin", ""); err != nil {
			t.Fatal(err)
		}
		if err := e.prov.SetUserGroup(ctx, c, "alice", "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	tok := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	settle()

	// the catalog of grantable endpoints
	res := call(t, e.r, http.MethodGet, "/api/v1/rbac/endpoints", tok, "")
	if res.Status != 200 {
		t.Fatalf("catalog: %d %s", res.Status, res.Raw)
	}
	var cat []struct {
		ID     string `json:"endpoint_id"`
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(res.Body.Data, &cat); err != nil || len(cat) == 0 {
		t.Fatalf("catalog body: %v %s", err, res.Raw)
	}
	for _, c := range cat {
		if strings.HasPrefix(c.Path, "/api/v1/auth/") {
			t.Fatalf("auth routes must not be listed: %s", c.Path)
		}
	}
	idOf := func(method, path string) string {
		for _, c := range cat {
			if c.Method == method && c.Path == path {
				return c.ID
			}
		}
		t.Fatalf("%s %s not in the catalog", method, path)
		return ""
	}

	// a group of tenant A
	g := call(t, e.r, http.MethodPost, "/api/v1/groups", tok, `{"name":"staff"}`)
	if g.Status != 201 {
		t.Fatalf("create group: %d %s", g.Status, g.Raw)
	}
	var created struct {
		ID string `json:"group_id"`
	}
	if err := json.Unmarshal(g.Body.Data, &created); err != nil || created.ID == "" {
		t.Fatalf("group body: %v %s", err, g.Raw)
	}
	url := "/api/v1/groups/" + created.ID + "/permissions"

	// replace, read back
	body := `{"endpoint_ids":["` + idOf("GET", "/api/v1/groups") + `"]}`
	if r := call(t, e.r, http.MethodPut, url, tok, body); r.Status != 200 {
		t.Fatalf("put: %d %s", r.Status, r.Raw)
	}
	r := call(t, e.r, http.MethodGet, url, tok, "")
	if r.Status != 200 || !contains(r.Raw, idOf("GET", "/api/v1/groups")) {
		t.Fatalf("get: %d %s", r.Status, r.Raw)
	}
	// an unknown endpoint id is a validation error and changes nothing
	if r := call(t, e.r, http.MethodPut, url, tok, `{"endpoint_ids":["018f0000-0000-7000-8000-000000000000"]}`); r.Status != 422 {
		t.Fatalf("unknown id: %d %s", r.Status, r.Raw)
	}
	if r := call(t, e.r, http.MethodPut, url, tok, `{"endpoint_ids":["not-a-uuid"]}`); r.Status != 422 {
		t.Fatalf("bad id: %d", r.Status)
	}
	if r := call(t, e.r, http.MethodPut, url, tok, `{}`); r.Status != 422 {
		t.Fatalf("missing field: %d", r.Status)
	}
	if r := call(t, e.r, http.MethodGet, url, tok, ""); !contains(r.Raw, idOf("GET", "/api/v1/groups")) {
		t.Fatalf("a refused PUT must not change anything: %s", r.Raw)
	}
	// a group of another tenant looks like it does not exist
	var otherID string
	if err := e.owner.Raw("SELECT group_id FROM groups WHERE name = 'admin' AND tenant_id = (SELECT tenant_id FROM tenants WHERE code='tokob')").Scan(&otherID).Error; err != nil {
		t.Fatal(err)
	}
	if r := call(t, e.r, http.MethodPut, "/api/v1/groups/"+otherID+"/permissions", tok, body); r.Status != 404 {
		t.Fatalf("other tenant: %d", r.Status)
	}
	if r := call(t, e.r, http.MethodGet, "/api/v1/groups/not-a-uuid/permissions", tok, ""); r.Status != 404 {
		t.Fatalf("bad group id: %d", r.Status)
	}
	// [] revokes everything
	if r := call(t, e.r, http.MethodPut, url, tok, `{"endpoint_ids":[]}`); r.Status != 200 {
		t.Fatalf("revoke: %d %s", r.Status, r.Raw)
	}
	var left int64
	if err := e.owner.Raw("SELECT count(*) FROM group_endpoints WHERE group_id = ? AND deleted_at IS NULL", created.ID).Scan(&left).Error; err != nil || left != 0 {
		t.Fatalf("grants left: %d %v", left, err)
	}
	// the crud routes next to /groups/:id/permissions still work
	if r := call(t, e.r, http.MethodGet, "/api/v1/groups/show_by/name/staff", tok, ""); r.Status != 200 {
		t.Fatalf("show_by next to :id/permissions: %d %s", r.Status, r.Raw)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestEndpointSync(t *testing.T) {
	e := setupRBAC(t)
	ctx := context.Background()

	res, err := e.prov.SyncEndpoints(ctx, e.routes, false)
	if err != nil || res.Inserted == 0 || res.Inserted != res.Total {
		t.Fatalf("first sync: %+v %v", res, err)
	}
	var auths int64
	if err := e.owner.Raw("SELECT count(*) FROM endpoints WHERE path LIKE '/api/v1/auth/%'").Scan(&auths).Error; err != nil || auths != 0 {
		t.Fatalf("auth routes are not synchronized: %d %v", auths, err)
	}
	again, err := e.prov.SyncEndpoints(ctx, e.routes, false)
	if err != nil || again.Inserted != 0 || again.Updated != 0 {
		t.Fatalf("second sync must change nothing: %+v %v", again, err)
	}

	// a route that disappeared is reported, kept, and removed only with prune
	if err := e.owner.Exec(`INSERT INTO endpoints (endpoint_id, method, path, name, scope)
		VALUES (gen_random_uuid(), 'GET', '/api/v1/old', 'old', 'tenant')`).Error; err != nil {
		t.Fatal(err)
	}
	kept, err := e.prov.SyncEndpoints(ctx, e.routes, false)
	if err != nil || len(kept.Stale) != 1 || kept.Pruned != 0 {
		t.Fatalf("stale: %+v %v", kept, err)
	}
	pruned, err := e.prov.SyncEndpoints(ctx, e.routes, true)
	if err != nil || pruned.Pruned != 1 {
		t.Fatalf("prune: %+v %v", pruned, err)
	}
	var live int64
	if err := e.owner.Raw("SELECT count(*) FROM endpoints WHERE path = '/api/v1/old' AND deleted_at IS NULL").Scan(&live).Error; err != nil || live != 0 {
		t.Fatalf("pruned row still live: %d %v", live, err)
	}
	// settings an operator changed by hand survive a sync
	if err := e.owner.Exec("UPDATE endpoints SET is_public = true WHERE method='GET' AND path='/api/v1/groups'").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.SyncEndpoints(ctx, e.routes, false); err != nil {
		t.Fatal(err)
	}
	var pub bool
	if err := e.owner.Raw("SELECT is_public FROM endpoints WHERE method='GET' AND path='/api/v1/groups'").Scan(&pub).Error; err != nil || !pub {
		t.Fatalf("manual is_public was overwritten: %v %v", pub, err)
	}
}
