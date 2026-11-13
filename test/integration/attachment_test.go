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
CREATE TABLE kegiatan (
    kegiatan_id uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants (tenant_id),
    judul       text NOT NULL,
    CONSTRAINT kegiatan_tenant_uq UNIQUE (tenant_id, kegiatan_id)
);
CREATE TABLE dokumentasi_kegiatan (
    dokumentasi_kegiatan_id uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL,
    kegiatan_id uuid NOT NULL,
    file_id     uuid NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, kegiatan_id) REFERENCES kegiatan (tenant_id, kegiatan_id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, file_id) REFERENCES files (tenant_id, file_id),
    UNIQUE (file_id)
);
CREATE INDEX dokumentasi_kegiatan_kegiatan_idx ON dokumentasi_kegiatan (tenant_id, kegiatan_id);
ALTER TABLE kegiatan ENABLE ROW LEVEL SECURITY;
ALTER TABLE kegiatan FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kegiatan USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
ALTER TABLE dokumentasi_kegiatan ENABLE ROW LEVEL SECURITY;
ALTER TABLE dokumentasi_kegiatan FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dokumentasi_kegiatan USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
`

type attEnv struct {
	*authEnv
	r        http.Handler
	blobDir  string
	kegiatan map[string]string // tenant code -> id of a kegiatan of that tenant
}

func setupAttachment(t *testing.T) *attEnv {
	t.Helper()
	a := setupAuth(t)
	a.seed(t)
	if err := a.owner.Exec(attachmentSQL).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.owner.Exec("GRANT SELECT, INSERT, UPDATE, DELETE ON kegiatan, dokumentasi_kegiatan TO " + appRole).Error; err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, code := range []string{"tokoa", "tokob"} {
		var tid string
		if err := a.owner.Raw("SELECT tenant_id FROM tenants WHERE code = ?", code).Scan(&tid).Error; err != nil || tid == "" {
			t.Fatal(err)
		}
		kid := "00000000-0000-7000-8000-00000000000" + map[string]string{"tokoa": "1", "tokob": "2"}[code]
		if err := a.owner.Exec("INSERT INTO kegiatan (kegiatan_id, tenant_id, judul) VALUES (?, ?, 'Ronda')", kid, tid).Error; err != nil {
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
		Name: "dokumentasi", Parent: "kegiatan", Table: "dokumentasi_kegiatan", IDColumn: "dokumentasi_kegiatan_id",
		ParentTable: "kegiatan", ParentColumn: "kegiatan_id",
	}, a.app, repo, blobs, chain...)
	r := rest.NewRouter(&config.Config{Env: "testing"}, slog.New(slog.DiscardHandler), a.app, sessions, files, docs)
	return &attEnv{authEnv: a, r: r, blobDir: blobDir, kegiatan: ids}
}

func (e *attEnv) uploadPNG(t *testing.T, tok string) string {
	t.Helper()
	body, ct := form(t, "foto.png", pngBytes(500), "purpose", "dokumentasi_kegiatan")
	res := call(t, e.r, http.MethodPost, "/api/v1/files", tok, body, "Content-Type", ct)
	return fileOf(t, res).ID
}

func TestAttachment_Flow(t *testing.T) {
	e := setupAttachment(t)
	tok := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
	base := "/api/v1/kegiatan/" + e.kegiatan["tokoa"] + "/dokumentasi"

	if res := call(t, e.r, http.MethodGet, base, tok, ""); res.Status != 200 || !strings.Contains(res.Raw, `"data":[]`) {
		t.Fatalf("empty list: %d %s", res.Status, res.Raw)
	}

	fileID := e.uploadPNG(t, tok)
	res := call(t, e.r, http.MethodPost, base, tok, `{"file_id":"`+fileID+`"}`)
	if res.Status != 201 || !strings.Contains(res.Raw, "foto.png") || !strings.Contains(res.Raw, "/api/v1/files/"+fileID+"/content") {
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
	baseA := "/api/v1/kegiatan/" + e.kegiatan["tokoa"] + "/dokumentasi"
	baseB := "/api/v1/kegiatan/" + e.kegiatan["tokob"] + "/dokumentasi"

	fileA := e.uploadPNG(t, a)
	// tenant B cannot reach the kegiatan of A, nor attach the file of A to its own kegiatan
	if res := call(t, e.r, http.MethodGet, baseA, b, ""); res.Status != 404 {
		t.Fatalf("list of another tenant: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, baseA, b, `{"file_id":"`+e.uploadPNG(t, b)+`"}`); res.Status != 404 {
		t.Fatalf("attach to a kegiatan of another tenant: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, baseB, b, `{"file_id":"`+fileA+`"}`); res.Status != 422 {
		t.Fatalf("attach a file of another tenant: %d %s", res.Status, res.Raw)
	}
	// and the database refuses it too when the application layer is bypassed (composite foreign keys)
	var tidB string
	_ = e.owner.Raw("SELECT tenant_id FROM tenants WHERE code='tokob'").Scan(&tidB).Error
	err := e.owner.Exec(`INSERT INTO dokumentasi_kegiatan (dokumentasi_kegiatan_id, tenant_id, kegiatan_id, file_id)
		VALUES ('00000000-0000-7000-8000-0000000000aa', ?, ?, ?)`, tidB, e.kegiatan["tokob"], fileA).Error
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
