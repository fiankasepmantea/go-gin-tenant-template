//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/group"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/session"
	tenantmod "github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/crud"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
)

type groupCreate struct {
	Name        string `json:"name" binding:"required,max=100"`
	Description string `json:"description" binding:"max=500"`
}

type groupUpdate struct {
	Name        *string `json:"name" binding:"omitempty,min=1,max=100"`
	Description *string `json:"description" binding:"omitempty,max=500"`
}

// groupRoutes mounts the example resource of docs/http-api.md on the real middleware chain.
type groupRoutes struct {
	register func(rg *gin.RouterGroup)
}

func (g groupRoutes) RegisterRoutes(rg *gin.RouterGroup) { g.register(rg) }

func setupCRUD(t *testing.T) (*authEnv, http.Handler) {
	t.Helper()
	a := setupAuth(t)
	a.seed(t)

	h := crud.New(crud.Config[group.Group, *group.Group, groupCreate, groupUpdate]{
		Store: group.NewRepository(a.app),
		// group_unique is only listed to prove that a database type error is a 400, not a 500
		Filterable: []string{"name", "description", "group_unique"},
		Sortable:   []string{"name", "created_at"},
		Build: func(in groupCreate) *group.Group {
			return &group.Group{Name: in.Name, Description: in.Description}
		},
		Changes: func(in groupUpdate) map[string]any {
			m := map[string]any{}
			if in.Name != nil {
				m["name"] = *in.Name
			}
			if in.Description != nil {
				m["description"] = *in.Description
			}
			return m
		},
	})

	hasher, err := password.New(password.Params{MemoryKiB: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := session.NewService(session.Deps{
		Tx: session.NewTxRunner(a.app), Tenants: tenantmod.NewRepository(a.app), Users: user.NewRepository(a.app),
		Tokens: session.NewRepository(a.app), Hasher: hasher, Issuer: a.mgr,
		AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	sessions := session.NewHandler(svc, tenantctx.Chain(tenantctx.HeaderResolver{}), middleware.Authenticate(a.mgr), nil)
	resource := groupRoutes{func(rg *gin.RouterGroup) {
		h.Register(rg.Group("/groups", middleware.Authenticate(a.mgr), middleware.TenantTx(a.app)))
	}}
	router := rest.NewRouter(&config.Config{Env: "testing"}, slog.New(slog.DiscardHandler), a.app, sessions, resource)
	return a, router
}

type apiResp struct {
	Status int
	Body   struct {
		Success bool            `json:"success"`
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	Raw string
}

func call(t *testing.T, r http.Handler, method, target, token, body string, headers ...string) apiResp {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := apiResp{Status: w.Code, Raw: w.Body.String()}
	if err := json.Unmarshal(w.Body.Bytes(), &out.Body); err != nil {
		t.Fatalf("%s %s: response is not JSON: %v: %s", method, target, err, w.Body)
	}
	return out
}

func login(t *testing.T, r http.Handler, path, tenantCode, username, pw string) string {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + pw + `"}`
	var res apiResp
	if tenantCode == "" {
		res = call(t, r, http.MethodPost, path, "", body)
	} else {
		res = call(t, r, http.MethodPost, path, "", body, "X-Tenant-Code", tenantCode)
	}
	// the login endpoint answers with the token fields at the top level
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(res.Raw), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("login %s/%s failed: %d %s", tenantCode, username, res.Status, res.Raw)
	}
	return tok.AccessToken
}

type pageData struct {
	TotalData   int64         `json:"total_data"`
	TotalPages  int           `json:"total_pages"`
	CurrPage    int           `json:"curr_page"`
	NextPage    int           `json:"next_page"`
	PrevPage    *int          `json:"prev_page"`
	DataPerPage int           `json:"data_per_page"`
	Data        []group.Group `json:"data"`
}

func page(t *testing.T, res apiResp) pageData {
	t.Helper()
	var p pageData
	if err := json.Unmarshal(res.Body.Data, &p); err != nil {
		t.Fatalf("not a page: %v: %s", err, res.Raw)
	}
	return p
}

func names(p pageData) []string {
	out := make([]string, 0, len(p.Data))
	for _, g := range p.Data {
		out = append(out, g.Name)
	}
	return out
}

func TestCRUD_FullStack(t *testing.T) {
	a, r := setupCRUD(t)
	tokA := login(t, r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	tokB := login(t, r, "/api/v1/auth/login", "tokob", "alice", "password-of-alice-b")

	var tenantAID string
	if err := a.owner.Raw("SELECT tenant_id FROM tenants WHERE code = 'tokoa'").Scan(&tenantAID).Error; err != nil || tenantAID == "" {
		t.Fatalf("tenant A id: %v", err)
	}

	// --- create: the tenant comes from the token; id and tenant_id in the body are ignored
	res := call(t, r, http.MethodPost, "/api/v1/groups", tokA,
		`{"name":"Admin","description":"all rights","tenant_id":"00000000-0000-7000-8000-000000000000","group_id":"00000000-0000-7000-8000-000000000001"}`)
	if res.Status != 201 || !res.Body.Success || res.Body.Code != 201 {
		t.Fatalf("create: %d %s", res.Status, res.Raw)
	}
	var created group.Group
	_ = json.Unmarshal(res.Body.Data, &created)
	if created.TenantID != tenantAID {
		t.Errorf("tenant_id = %s, want %s (from the token)", created.TenantID, tenantAID)
	}
	if created.ID == "00000000-0000-7000-8000-000000000001" || created.ID == "" {
		t.Errorf("the client must not choose the id: %q", created.ID)
	}
	if n := a.count(t, "SELECT count(*) FROM groups WHERE tenant_id = ?", tenantAID); n != 1 {
		t.Errorf("rows for tenant A = %d, want 1", n)
	}

	// --- duplicate name (case-insensitive unique index) -> 409
	if res = call(t, r, http.MethodPost, "/api/v1/groups", tokA, `{"name":"ADMIN"}`); res.Status != 409 || res.Body.Error.Code != "conflict" {
		t.Errorf("duplicate: %d %s", res.Status, res.Raw)
	}
	// the failed request must not have left anything behind, and the next request still works
	if n := a.count(t, "SELECT count(*) FROM groups WHERE tenant_id = ?", tenantAID); n != 1 {
		t.Errorf("rows after the rejected duplicate = %d, want 1", n)
	}

	// --- validation
	if res = call(t, r, http.MethodPost, "/api/v1/groups", tokA, `{"description":"no name"}`); res.Status != 422 || res.Body.Error.Code != "validation_failed" {
		t.Errorf("missing name: %d %s", res.Status, res.Raw)
	}

	// --- isolation: tenant B sees nothing of tenant A
	if res = call(t, r, http.MethodGet, "/api/v1/groups", tokB, ""); res.Status != 200 || page(t, res).TotalData != 0 {
		t.Errorf("tenant B list: %d %s", res.Status, res.Raw)
	}
	for method, body := range map[string]string{http.MethodGet: "", http.MethodPatch: `{"name":"hijacked"}`, http.MethodDelete: ""} {
		if res = call(t, r, method, "/api/v1/groups/"+created.ID, tokB, body); res.Status != 404 || res.Body.Error.Code != "not_found" {
			t.Errorf("tenant B %s on A's record: %d %s", method, res.Status, res.Raw)
		}
	}
	if res = call(t, r, http.MethodGet, "/api/v1/groups/show_by/name/Admin", tokB, ""); res.Status != 404 {
		t.Errorf("tenant B show_by: %d", res.Status)
	}
	if n := a.count(t, "SELECT count(*) FROM groups WHERE group_id = ? AND name = 'Admin' AND deleted_at IS NULL", created.ID); n != 1 {
		t.Error("tenant A's record was changed by tenant B")
	}

	// --- get / update / show_by
	if res = call(t, r, http.MethodGet, "/api/v1/groups/"+created.ID, tokA, ""); res.Status != 200 {
		t.Errorf("get: %d %s", res.Status, res.Raw)
	}
	res = call(t, r, http.MethodPatch, "/api/v1/groups/"+created.ID, tokA, `{"description":"changed","tenant_id":"x"}`)
	var updated group.Group
	_ = json.Unmarshal(res.Body.Data, &updated)
	if res.Status != 200 || updated.Description != "changed" || updated.Name != "Admin" || updated.TenantID != tenantAID {
		t.Errorf("update: %d %s", res.Status, res.Raw)
	}
	if res = call(t, r, http.MethodPatch, "/api/v1/groups/"+created.ID, tokA, `{}`); res.Status != 400 {
		t.Errorf("empty update: %d %s", res.Status, res.Raw)
	}
	if res = call(t, r, http.MethodGet, "/api/v1/groups/show_by/name/Admin", tokA, ""); res.Status != 200 {
		t.Errorf("show_by: %d %s", res.Status, res.Raw)
	}

	// --- delete (soft) then it is gone
	if res = call(t, r, http.MethodDelete, "/api/v1/groups/"+created.ID, tokA, ""); res.Status != 200 || !res.Body.Success {
		t.Errorf("delete: %d %s", res.Status, res.Raw)
	}
	if res = call(t, r, http.MethodGet, "/api/v1/groups/"+created.ID, tokA, ""); res.Status != 404 {
		t.Errorf("get after delete: %d", res.Status)
	}
	if n := a.count(t, "SELECT count(*) FROM groups WHERE group_id = ? AND deleted_at IS NOT NULL", created.ID); n != 1 {
		t.Error("delete must be a soft delete")
	}
}

func TestCRUD_ListOptions(t *testing.T) {
	_, r := setupCRUD(t)
	tok := login(t, r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")

	for _, n := range []string{"charlie", "alpha", "echo", "bravo", "delta"} {
		if res := call(t, r, http.MethodPost, "/api/v1/groups", tok, `{"name":"`+n+`","description":"d-`+n+`"}`); res.Status != 201 {
			t.Fatalf("seed %s: %d %s", n, res.Status, res.Raw)
		}
	}

	// default order = creation order (UUID v7)
	p := page(t, call(t, r, http.MethodGet, "/api/v1/groups", tok, ""))
	if got := strings.Join(names(p), ","); got != "charlie,alpha,echo,bravo,delta" || p.TotalData != 5 {
		t.Errorf("default order = %s (total %d)", got, p.TotalData)
	}

	// sort + pagination: page 2 of 2 per page, sorted by name
	p = page(t, call(t, r, http.MethodGet, "/api/v1/groups?sort=name&per_page=2&page=2", tok, ""))
	if got := strings.Join(names(p), ","); got != "charlie,delta" {
		t.Errorf("page 2 = %s", got)
	}
	if p.TotalData != 5 || p.TotalPages != 3 || p.CurrPage != 2 || p.NextPage != 3 || p.PrevPage == nil || *p.PrevPage != 1 || p.DataPerPage != 2 {
		t.Errorf("pagination = %+v", p)
	}
	p = page(t, call(t, r, http.MethodGet, "/api/v1/groups?sort=name&per_page=2&page=3", tok, ""))
	if got := strings.Join(names(p), ","); got != "echo" || p.NextPage != 3 {
		t.Errorf("last page = %s next=%d", got, p.NextPage)
	}

	// descending
	p = page(t, call(t, r, http.MethodGet, "/api/v1/groups?sort=-name&per_page=2", tok, ""))
	if got := strings.Join(names(p), ","); got != "echo,delta" {
		t.Errorf("descending = %s", got)
	}

	// filter, and a filter combined with pagination totals
	p = page(t, call(t, r, http.MethodGet, "/api/v1/groups?name=bravo", tok, ""))
	if p.TotalData != 1 || len(p.Data) != 1 || p.Data[0].Name != "bravo" {
		t.Errorf("filter = %+v", p)
	}
	if p = page(t, call(t, r, http.MethodGet, "/api/v1/groups?name=nobody", tok, "")); p.TotalData != 0 || len(p.Data) != 0 {
		t.Errorf("filter without a match = %+v", p)
	}

	// all_by
	p = page(t, call(t, r, http.MethodGet, "/api/v1/groups/all_by/description/d-echo", tok, ""))
	if p.TotalData != 1 || p.Data[0].Name != "echo" {
		t.Errorf("all_by = %+v", p)
	}

	// created_at range: today is included, yesterday-only is not (UTC dates)
	today := time.Now().UTC().Format(time.DateOnly)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if p = page(t, call(t, r, http.MethodGet, "/api/v1/groups?from="+today+"&to="+today, tok, "")); p.TotalData != 5 {
		t.Errorf("range today = %d, want 5", p.TotalData)
	}
	if p = page(t, call(t, r, http.MethodGet, "/api/v1/groups?to="+yesterday, tok, "")); p.TotalData != 0 {
		t.Errorf("range until yesterday = %d, want 0", p.TotalData)
	}

	// rejected inputs never reach the database
	for _, q := range []string{"?tenant_id=x", "?sort=tenant_id", "?page=0", "?nam=a", "?name=a;b", "?sort=name;drop%20table%20groups"} {
		if res := call(t, r, http.MethodGet, "/api/v1/groups"+q, tok, ""); res.Status != 400 || res.Body.Error.Code != "invalid_request" {
			t.Errorf("%s: %d %s", q, res.Status, res.Raw)
		}
	}
	// a value the database rejects for the column type is a 400, not a 500
	if res := call(t, r, http.MethodGet, "/api/v1/groups?group_unique=abc", tok, ""); res.Status != 400 || res.Body.Error.Code != "invalid_request" {
		t.Errorf("type error from the database: %d %s", res.Status, res.Raw)
	}
	// SQL metacharacters in a value are just a value
	if res := call(t, r, http.MethodGet, "/api/v1/groups?name=x'%20OR%201=1--", tok, ""); res.Status != 200 || page(t, res).TotalData != 0 {
		t.Errorf("injection attempt in a value: %d %s", res.Status, res.Raw)
	}
}

func TestCRUD_Authentication(t *testing.T) {
	_, r := setupCRUD(t)

	// no token, garbage token
	for _, tok := range []string{"", "garbage"} {
		if res := call(t, r, http.MethodGet, "/api/v1/groups", tok, ""); res.Status != 401 || res.Body.Error.Code != "unauthorized" {
			t.Errorf("token %q: %d %s", tok, res.Status, res.Raw)
		}
	}

	// a platform admin has no tenant: a tenant resource is not available to it (and nothing leaks)
	root := login(t, r, "/api/v1/auth/platform/login", "", "root", "password-of-the-root")
	if res := call(t, r, http.MethodGet, "/api/v1/groups", root, ""); res.Status != 403 || res.Body.Error.Code != "forbidden" {
		t.Errorf("platform token on a tenant resource: %d %s", res.Status, res.Raw)
	}
	if res := call(t, r, http.MethodPost, "/api/v1/groups", root, `{"name":"x"}`); res.Status != 403 {
		t.Errorf("platform token create: %d %s", res.Status, res.Raw)
	}
}

func TestCRUD_UnknownRoutesUseTheEnvelope(t *testing.T) {
	_, r := setupCRUD(t)
	if res := call(t, r, http.MethodGet, "/api/v1/nope", "", ""); res.Status != 404 || res.Body.Error.Code != "not_found" || res.Body.Success {
		t.Errorf("unknown route: %d %s", res.Status, res.Raw)
	}
	if res := call(t, r, http.MethodPut, "/api/v1/groups", "", ""); res.Status != 405 || res.Body.Error.Code != "method_not_allowed" {
		t.Errorf("wrong method: %d %s", res.Status, res.Raw)
	}
}
