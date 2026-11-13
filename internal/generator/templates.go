package generator

import "strings"

// The templates use ¦ where a Go source file has a backtick (struct tags): a raw string cannot contain one.
func tick(s string) string { return strings.ReplaceAll(s, "¦", "`") }

const tmplUp = `-- {{.Type}}: table {{.Table}}, one set of rows per tenant. Written by "tenantctl make:module"; edit before applying if needed.
CREATE TABLE {{.Table}} (
    {{.IDColumn}} uuid PRIMARY KEY,
    {{.UniqueColumn}} bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    tenant_id uuid NOT NULL REFERENCES tenants (tenant_id),
{{- range .Fields}}
    {{.Name}} {{.SQLType}}{{if .SQLConstraint}} {{.SQLConstraint}}{{end}},
{{- end}}
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    -- lets other tables reference a {{.Name}} with a composite FK that cannot cross tenants
    CONSTRAINT {{.Table}}_tenant_{{.Name}}_uq UNIQUE (tenant_id, {{.IDColumn}})
);
CREATE INDEX {{.Table}}_tenant_idx ON {{.Table}} (tenant_id);
{{- range .UniqueSet}}
CREATE UNIQUE INDEX {{$.Table}}_tenant_{{.Name}}_uq ON {{$.Table}} (tenant_id, {{if eq .Type "string"}}lower({{.Name}}){{else}}{{.Name}}{{end}}) WHERE deleted_at IS NULL;
{{- end}}

CREATE TRIGGER {{.Table}}_updated_at BEFORE UPDATE ON {{.Table}}
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE {{.Table}} ENABLE ROW LEVEL SECURITY;
ALTER TABLE {{.Table}} FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON {{.Table}}
    USING (tenant_id = app_tenant())
    WITH CHECK (tenant_id = app_tenant());
`

const tmplDown = `DROP TABLE IF EXISTS {{.Table}};
`

var tmplModel = tick(`// Package {{.Name}} is the "{{.Name}}" business module. Written by "tenantctl make:module".
package {{.Name}}

import (
	"time"

	"gorm.io/gorm"
)

type {{.Type}} struct {
	ID        string         ¦gorm:"column:{{.IDColumn}};primaryKey" json:"{{.IDColumn}}"¦
	Unique    int64          ¦gorm:"column:{{.UniqueColumn}};->" json:"{{.UniqueColumn}}"¦
	TenantID  string         ¦gorm:"column:tenant_id" json:"tenant_id"¦
{{- range .Fields}}
	{{.GoName}} {{.ModelType}} ¦gorm:"column:{{.Name}}" json:"{{.Name}}"¦
{{- end}}
	CreatedAt time.Time      ¦gorm:"column:created_at" json:"created_at"¦
	UpdatedAt time.Time      ¦gorm:"column:updated_at" json:"updated_at"¦
	DeletedAt gorm.DeletedAt ¦gorm:"column:deleted_at;index" json:"-"¦
}

func (*{{.Type}}) TableName() string       { return {{quote .Table}} }
func (*{{.Type}}) PrimaryKey() string      { return {{quote .IDColumn}} }
func (m *{{.Type}}) GetID() string         { return m.ID }
func (m *{{.Type}}) SetID(id string)       { m.ID = id }
func (m *{{.Type}}) SetTenantID(id string) { m.TenantID = id }
`)

const tmplRepository = `package {{.Name}}

import (
	"gorm.io/gorm"

	"{{.Path}}/internal/core"
)

// Repository reuses all core.Repository behavior (CRUD + automatic tenant filter).
// Add the queries of this module here.
type Repository struct {
	*core.Repository[{{.Type}}, *{{.Type}}]
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{core.NewRepository[{{.Type}}](db)}
}
`

var tmplHandler = tick(`package {{.Name}}

import (
{{- if .NeedStrings}}
	"strings"
{{- end}}
{{- if .NeedTime}}
	"time"
{{- end}}

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"{{.Path}}/internal/transport/rest/crud"
	"{{.Path}}/internal/transport/rest/openapi"
)

// createInput and updateInput are the only fields a client can set. Identity, tenant and timestamps are not here.
type createInput struct {
{{- range .Fields}}
	{{.GoName}} {{.CreateType}} ¦json:"{{.Name}}"{{if .CreateTag}} binding:"{{.CreateTag}}"{{end}}¦
{{- end}}
}

type updateInput struct { // pointers: nil means "not sent"
{{- range .Fields}}
	{{.GoName}} {{.UpdateType}} ¦json:"{{.Name}}"{{if .UpdateTag}} binding:"{{.UpdateTag}}"{{end}}¦
{{- end}}
}

// Handler serves /{{.Table}} with the standard endpoints of crud.Handler.
type Handler struct {
	h  *crud.Handler[{{.Type}}, *{{.Type}}, createInput, updateInput]
	mw []gin.HandlerFunc
}

// NewHandler creates the handler. mw runs before every endpoint; pass the authentication chain
// (Authenticate, TenantTx, Authorize) here. The handler itself decides nothing about who may call it.
func NewHandler(db *gorm.DB, mw ...gin.HandlerFunc) *Handler {
	h := crud.New(crud.Config[{{.Type}}, *{{.Type}}, createInput, updateInput]{
		Store:      NewRepository(db),
		Filterable: []string{ {{- range $i, $c := .FilterCols}}{{if $i}}, {{end}}{{quote $c}}{{end -}} },
		Sortable:   []string{ {{- range $i, $c := .SortCols}}{{if $i}}, {{end}}{{quote $c}}{{end -}} },
		Build: func(in createInput) *{{.Type}} {
			return &{{.Type}}{
{{- range .Fields}}
				{{.GoName}}: {{.BuildExpr}},
{{- end}}
			}
		},
		Changes: func(in updateInput) map[string]any {
			m := map[string]any{}
{{- range .Fields}}
			if in.{{.GoName}} != nil {
				m[{{quote .Name}}] = {{.ChangeExpr}}
			}
{{- end}}
			return m
		},
	})
	return &Handler{h: h, mw: mw}
}

// Document adds the endpoints to the OpenAPI document.
func (h *Handler) Document(d *openapi.Doc) { h.h.Document(d) }

// RegisterRoutes registers /{{.Table}} under rg (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	h.h.Register(rg.Group("/{{.Table}}"), h.mw...)
}
`)

const tmplHandlerTest = `package {{.Name}}

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

func TestRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewHandler(nil).RegisterRoutes(r.Group("/api/v1"))

	got := map[string]bool{}
	for _, ri := range r.Routes() {
		got[ri.Method+" "+ri.Path] = true
	}
	want := []string{
		"GET /api/v1/{{.Table}}",
		"GET /api/v1/{{.Table}}/:id",
		"GET /api/v1/{{.Table}}/show_by/:column/:value",
		"GET /api/v1/{{.Table}}/all_by/:column/:value",
		"POST /api/v1/{{.Table}}",
		"PATCH /api/v1/{{.Table}}/:id",
		"DELETE /api/v1/{{.Table}}/:id",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("route %q is not registered", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d routes registered, want %d: %v", len(got), len(want), got)
	}
}

func TestUpdateInputAcceptsAnEmptyBody(t *testing.T) {
	if err := binding.Validator.ValidateStruct(&updateInput{}); err != nil {
		t.Fatalf("an update that sends nothing is rejected by crud (400), not by validation: %v", err)
	}
}
{{if .HasRequired}}
func TestCreateInputRequiresTheRequiredFields(t *testing.T) {
	if err := binding.Validator.ValidateStruct(&createInput{}); err == nil {
		t.Fatal("an empty create request must fail validation")
	}
}
{{end}}`

var tmplIntegration = tick(`//go:build integration

package integration

import (
	"context"
	"encoding/json"
{{- if .HasStrings}}
	"fmt"
{{- end}}
	"net/http"
	"strings"
	"testing"
)

// Test{{.Type}}Module checks the generated {{.Name}} module on a real PostgreSQL: RBAC (default deny, then a
// grant), the CRUD endpoints, validation and the isolation between two tenants.
func Test{{.Type}}Module(t *testing.T) {
	e := setupRBAC(t)
	ctx := context.Background()
	base := "/api/v1/{{.Table}}"
	tokA := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	tokB := login(t, e.r, "/api/v1/auth/login", "tokob", "alice", "password-of-alice-b")
	{{if .HasStrings}}body := func(n int) string {
		return fmt.Sprintf(¦{ {{- range $i, $f := .Fields}}{{if $i}},{{end}}"{{$f.Name}}":{{$f.Sample}}{{end -}} }¦, n)
	}{{else}}body := func(_ int) string {
		return ¦{ {{- range $i, $f := .Fields}}{{if $i}},{{end}}"{{$f.Name}}":{{$f.Sample}}{{end -}} }¦
	}{{end}}

	// default deny: a new route is closed until "tenantctl endpoint sync" and a grant open it
	if st, code := e.code(t, http.MethodGet, base, tokA, ""); st != 403 || code != "forbidden" {
		t.Fatalf("before sync: %d %s", st, code)
	}
	e.sync(t)
	if st, _ := e.code(t, http.MethodGet, base, tokA, ""); st != 403 {
		t.Fatalf("synced but not granted: %d", st)
	}
	if _, err := e.prov.CreateGroup(ctx, "tokoa", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	if st, _ := e.code(t, http.MethodGet, base, tokB, ""); st != 403 {
		t.Fatalf("tenant B has no grant: %d", st)
	}

	// create
	res := call(t, e.r, http.MethodPost, base, tokA, body(1))
	if res.Status != 201 {
		t.Fatalf("create: %d %s", res.Status, res.Raw)
	}
	var created struct {
		ID string ¦json:"{{.IDColumn}}"¦
	}
	if err := json.Unmarshal(res.Body.Data, &created); err != nil || created.ID == "" {
		t.Fatalf("created record has no id: %v %s", err, res.Raw)
	}
	item := base + "/" + created.ID
{{if .UniqueSet}}
	// unique per tenant
	if st, code := e.code(t, http.MethodPost, base, tokA, body(1)); st != 409 || code != "conflict" {
		t.Errorf("duplicate: %d %s", st, code)
	}
{{end}}
	// list and get
	res = call(t, e.r, http.MethodGet, base, tokA, "")
	var pg struct {
		TotalData int64 ¦json:"total_data"¦
	}
	if err := json.Unmarshal(res.Body.Data, &pg); err != nil || res.Status != 200 || pg.TotalData != 1 {
		t.Errorf("list: %d %s", res.Status, res.Raw)
	}
	if st, _ := e.code(t, http.MethodGet, item, tokA, ""); st != 200 {
		t.Errorf("get: %d", st)
	}

	// update: only what is sent changes
	res = call(t, e.r, http.MethodPatch, item, tokA, ¦{"{{.First.Name}}":{{.First.Alt}}}¦)
	if res.Status != 200 || !strings.Contains(res.Raw, ¦"{{.First.Name}}":{{.First.Alt}}¦) {
		t.Errorf("update: %d %s", res.Status, res.Raw)
	}
	if st, _ := e.code(t, http.MethodPatch, item, tokA, "{}"); st != 400 {
		t.Errorf("empty update: %d", st)
	}
{{if .HasRequired}}
	// validation
	if st, code := e.code(t, http.MethodPost, base, tokA, "{}"); st != 422 || code != "validation_failed" {
		t.Errorf("empty create: %d %s", st, code)
	}
{{end}}
	// tenant B (granted the same endpoints) sees nothing of tenant A
	if _, err := e.prov.CreateGroup(ctx, "tokob", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokob", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokob", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	res = call(t, e.r, http.MethodGet, base, tokB, "")
	if err := json.Unmarshal(res.Body.Data, &pg); err != nil || res.Status != 200 || pg.TotalData != 0 {
		t.Errorf("tenant B list: %d %s", res.Status, res.Raw)
	}
	for method, b := range map[string]string{http.MethodGet: "", http.MethodPatch: ¦{"{{.First.Name}}":{{.First.Alt}}}¦, http.MethodDelete: ""} {
		if st, code := e.code(t, method, item, tokB, b); st != 404 || code != "not_found" {
			t.Errorf("tenant B %s on A's record: %d %s", method, st, code)
		}
	}

	// delete is soft; the record is gone for the API
	if st, _ := e.code(t, http.MethodDelete, item, tokA, ""); st != 200 {
		t.Errorf("delete: %d", st)
	}
	if st, _ := e.code(t, http.MethodGet, item, tokA, ""); st != 404 {
		t.Errorf("get after delete: %d", st)
	}
	if n := e.count(t, "SELECT count(*) FROM {{.Table}} WHERE {{.IDColumn}} = ? AND deleted_at IS NOT NULL", created.ID); n != 1 {
		t.Error("delete must be a soft delete")
	}
}
`)
