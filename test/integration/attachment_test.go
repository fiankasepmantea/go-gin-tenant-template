//go:build integration

package integration

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/attachment"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/file"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/session"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/storage"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
)

// The SQL of docs/attachments.md, as an application would put it in its own migration.
const attachmentSQL = `
CREATE TABLE activities (
    activity_id uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants (tenant_id),
    title       text NOT NULL,
    CONSTRAINT activities_tenant_uq UNIQUE (tenant_id, activity_id)
);
CREATE TABLE activity_documents (
    activity_document_id uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL,
    activity_id uuid NOT NULL,
    file_id     uuid NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, activity_id) REFERENCES activities (tenant_id, activity_id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, file_id) REFERENCES files (tenant_id, file_id),
    UNIQUE (file_id)
);
CREATE INDEX activity_documents_activity_idx ON activity_documents (tenant_id, activity_id);
ALTER TABLE activities ENABLE ROW LEVEL SECURITY;
ALTER TABLE activities FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON activities USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
ALTER TABLE activity_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE activity_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON activity_documents USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
`

type attEnv struct {
	*authEnv
	r          http.Handler
	blobDir    string
	activities map[string]string // tenant code -> id of a activities of that tenant
}

func setupAttachment(t *testing.T) *attEnv {
	t.Helper()
	a := setupAuth(t)
	a.seed(t)
	if err := a.owner.Exec(attachmentSQL).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.owner.Exec("GRANT SELECT, INSERT, UPDATE, DELETE ON activities, activity_documents TO " + appRole).Error; err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, code := range []string{"tokoa", "tokob"} {
		var tid string
		if err := a.owner.Raw("SELECT tenant_id FROM tenants WHERE code = ?", code).Scan(&tid).Error; err != nil || tid == "" {
			t.Fatal(err)
		}
		kid := "00000000-0000-7000-8000-00000000000" + map[string]string{"tokoa": "1", "tokob": "2"}[code]
		if err := a.owner.Exec("INSERT INTO activities (activity_id, tenant_id, title) VALUES (?, ?, 'Patrol')", kid, tid).Error; err != nil {
			t.Fatal(err)
		}
		ids[code] = kid
	}

	blobDir := filepath.Join(t.TempDir(), "files")
	blobs, err := storage.NewLocal(blobDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })

	gin.SetMode(gin.TestMode)
	authn := middleware.Authenticate(a.mgr)
	chain := []gin.HandlerFunc{authn, middleware.TenantTx(a.app)}
	repo := file.NewRepository(a.app)
	sessions := session.NewHandler(a.svc, tenantctx.Chain(tenantctx.HeaderResolver{}), authn, nil)
	files := file.NewHandler(repo, blobs, file.Options{}, chain...)
	docs := attachment.NewHandler(attachment.Config{
		Name: "documents", Parent: "activities", Table: "activity_documents", IDColumn: "activity_document_id",
		ParentTable: "activities", ParentColumn: "activity_id",
	}, a.app, repo, blobs, chain...)
	r := rest.NewRouter(&config.Config{Env: "testing"}, slog.New(slog.DiscardHandler), a.app, sessions, files, docs)
	return &attEnv{authEnv: a, r: r, blobDir: blobDir, activities: ids}
}

func (e *attEnv) uploadPNG(t *testing.T, tok string) string {
	t.Helper()
	body, ct := form(t, "photo.png", pngBytes(500), "purpose", "activity_documents")
	res := call(t, e.r, http.MethodPost, "/api/v1/files", tok, body, "Content-Type", ct)
	return fileOf(t, res).ID
}

func TestAttachment_Flow(t *testing.T) {
	e := setupAttachment(t)
	tok := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	base := "/api/v1/activities/" + e.activities["tokoa"] + "/documents"

	if res := call(t, e.r, http.MethodGet, base, tok, ""); res.Status != 200 || !strings.Contains(res.Raw, `"data":[]`) {
		t.Fatalf("empty list: %d %s", res.Status, res.Raw)
	}

	fileID := e.uploadPNG(t, tok)
	res := call(t, e.r, http.MethodPost, base, tok, `{"file_id":"`+fileID+`"}`)
	if res.Status != 201 || !strings.Contains(res.Raw, "photo.png") || !strings.Contains(res.Raw, "/api/v1/files/"+fileID+"/content") {
		t.Fatalf("attach: %d %s", res.Status, res.Raw)
	}
	var item struct {
		ID string `json:"attachment_id"`
	}
	mustJSON(t, res.Body.Data, &item)

	if res := call(t, e.r, http.MethodGet, base, tok, ""); res.Status != 200 || !strings.Contains(res.Raw, item.ID) {
		t.Fatalf("list: %d %s", res.Status, res.Raw)
	}
	// the same file cannot be attached twice (UNIQUE (file_id) of the SQL)
	if res := call(t, e.r, http.MethodPost, base, tok, `{"file_id":"`+fileID+`"}`); res.Status != 409 {
		t.Fatalf("attach twice: %d %s", res.Status, res.Raw)
	}
	// an unknown file and a malformed id
	if res := call(t, e.r, http.MethodPost, base, tok, `{"file_id":"00000000-0000-7000-8000-000000000099"}`); res.Status != 422 {
		t.Fatalf("unknown file: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodPost, base, tok, `{"file_id":"x"}`); res.Status != 422 {
		t.Fatalf("bad file id: %d", res.Status)
	}

	// detach deletes the row, the file and the content
	if res := call(t, e.r, http.MethodDelete, base+"/"+item.ID, tok, ""); res.Status != 200 {
		t.Fatalf("detach: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodGet, "/api/v1/files/"+fileID, tok, ""); res.Status != 404 {
		t.Fatalf("file after detach: %d", res.Status)
	}
	if left, _ := filepath.Glob(filepath.Join(e.blobDir, "*", "*")); len(left) != 0 {
		t.Fatalf("content left: %v", left)
	}
	if res := call(t, e.r, http.MethodDelete, base+"/"+item.ID, tok, ""); res.Status != 404 {
		t.Fatalf("detach twice: %d", res.Status)
	}
}

func TestAttachment_TenantIsolation(t *testing.T) {
	e := setupAttachment(t)
	a := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	b := login(t, e.r, "/api/v1/auth/login", "tokob", "alice", "password-of-alice-b")
	baseA := "/api/v1/activities/" + e.activities["tokoa"] + "/documents"
	baseB := "/api/v1/activities/" + e.activities["tokob"] + "/documents"

	fileA := e.uploadPNG(t, a)
	// tenant B cannot reach the activities of A, nor attach the file of A to its own activities
	if res := call(t, e.r, http.MethodGet, baseA, b, ""); res.Status != 404 {
		t.Fatalf("list of another tenant: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, baseA, b, `{"file_id":"`+e.uploadPNG(t, b)+`"}`); res.Status != 404 {
		t.Fatalf("attach to a activities of another tenant: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, baseB, b, `{"file_id":"`+fileA+`"}`); res.Status != 422 {
		t.Fatalf("attach a file of another tenant: %d %s", res.Status, res.Raw)
	}
	// and the database refuses it too when the application layer is bypassed (composite foreign keys)
	var tidB string
	_ = e.owner.Raw("SELECT tenant_id FROM tenants WHERE code='tokob'").Scan(&tidB).Error
	err := e.owner.Exec(`INSERT INTO activity_documents (activity_document_id, tenant_id, activity_id, file_id)
		VALUES ('00000000-0000-7000-8000-0000000000aa', ?, ?, ?)`, tidB, e.activities["tokob"], fileA).Error
	if err == nil {
		t.Fatal("the database must refuse a file of another tenant")
	}
}

func TestAttachment_BadConfigPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a table name with SQL in it must be refused")
		}
	}()
	attachment.NewHandler(attachment.Config{Name: "x", Parent: "y", Table: "t; drop table users", IDColumn: "i",
		ParentTable: "p", ParentColumn: "c"}, nil, nil, nil)
}
