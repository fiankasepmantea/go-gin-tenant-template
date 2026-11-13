// Package attachment attaches uploaded files (module file) to the records of any other module: photos of an
// activity, documents of a case, the signature of a user. It is a reusable handler, configured per table:
//
//	attachment.NewHandler(attachment.Config{
//	    Name: "documents", Parent: "activities",
//	    Table: "activity_documents", IDColumn: "activity_document_id",
//	    ParentTable: "activities", ParentColumn: "activity_id",
//	}, db, files, blobs, chain...)
//
// serves
//
//	GET    /activities/:id/documents                  files attached to the record
//	POST   /activities/:id/documents                  {"file_id": "..."} attach an uploaded file
//	DELETE /activities/:id/documents/:attachment_id   detach and delete the file
//
// The table is made by a migration of the application (docs/attachments.md has the SQL). The composite foreign
// keys of that SQL, with row level security, are what keeps a tenant away from the records and files of another.
package attachment

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/file"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/storage"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

var identRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Config names the tables and the routes. Names of tables and columns are checked to be plain identifiers,
// because they are put into SQL; they come from code, never from a request.
type Config struct {
	Name         string // last path segment: "documents"
	Parent       string // path segment of the record: "activities" (routes: /activities/:id/documents)
	Table        string // the attachment table
	IDColumn     string // primary key of the attachment table (uuid)
	ParentColumn string // column of the attachment table that points to the record
	ParentTable  string // table of the record
	ParentPK     string // primary key of the record; default ParentColumn
	Limit        int    // most attachments listed per record; default 200
}

func (c *Config) validate() error {
	if c.ParentPK == "" {
		c.ParentPK = c.ParentColumn
	}
	if c.Limit <= 0 {
		c.Limit = 200
	}
	for name, v := range map[string]string{
		"Name": c.Name, "Parent": c.Parent, "Table": c.Table, "IDColumn": c.IDColumn,
		"ParentColumn": c.ParentColumn, "ParentTable": c.ParentTable, "ParentPK": c.ParentPK,
	} {
		if !identRe.MatchString(v) {
			return fmt.Errorf("attachment: Config.%s %q must be lower snake case", name, v)
		}
	}
	return nil
}

// Item is one attachment with the metadata of its file.
type Item struct {
	ID          string    `json:"attachment_id"`
	ParentID    string    `json:"parent_id"`
	FileID      string    `json:"file_id"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	URL         string    `json:"url"` // GET this (with the bearer token) for the content
	CreatedAt   time.Time `json:"created_at"`
}

type attachInput struct {
	FileID string `json:"file_id" binding:"required,uuid"`
}

type row struct {
	ID          string    `gorm:"column:attachment_id"`
	ParentID    string    `gorm:"column:parent_id"`
	FileID      string    `gorm:"column:file_id"`
	Name        string    `gorm:"column:name"`
	ContentType string    `gorm:"column:content_type"`
	Size        int64     `gorm:"column:size"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// Handler serves the three routes of one attachment table.
type Handler struct {
	cfg   Config
	db    *gorm.DB
	files file.Store
	blobs storage.Store
	mw    []gin.HandlerFunc
	base  string
}

// NewHandler creates the handler. It panics on an unusable Config, a programming error found at startup.
func NewHandler(cfg Config, db *gorm.DB, files file.Store, blobs storage.Store, mw ...gin.HandlerFunc) *Handler {
	if err := cfg.validate(); err != nil {
		panic(err)
	}
	return &Handler{cfg: cfg, db: db, files: files, blobs: blobs, mw: mw}
}

// RegisterRoutes registers the routes under rg (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	h.base = rg.BasePath() + "/" + h.cfg.Parent + "/:id/" + h.cfg.Name
	g := rg.Group("/"+h.cfg.Parent+"/:id/"+h.cfg.Name, h.mw...)
	g.GET("", h.list)
	g.POST("", h.attach)
	g.DELETE("/:attachment_id", h.detach)
}

// Document adds the routes to the OpenAPI document.
func (h *Handler) Document(d *openapi.Doc) {
	if h.base == "" {
		return
	}
	tag := h.cfg.Name
	d.Add(openapi.Op{Method: "GET", Path: h.base, Summary: "List the " + h.cfg.Name + " of a " + h.cfg.Parent, Tag: tag, Result: Item{}, Kind: openapi.List})
	d.Add(openapi.Op{Method: "POST", Path: h.base, Summary: "Attach an uploaded file (POST /files first) to a " + h.cfg.Parent, Tag: tag, Body: attachInput{}, Result: Item{}, Status: 201})
	d.Add(openapi.Op{Method: "DELETE", Path: h.base + "/:attachment_id", Summary: "Detach and delete the file", Tag: tag, Kind: openapi.Message})
}

func (h *Handler) conn(ctx context.Context) (*gorm.DB, string, error) {
	id, ok := tenant.IDFrom(ctx)
	if !ok {
		return nil, "", core.ErrNoTenant
	}
	return database.From(ctx, h.db), id, nil
}

// parentExists answers 404 for a record that is missing or belongs to another tenant (RLS hides it).
func (h *Handler) parentExists(conn *gorm.DB, tenantID, id string) error {
	var n int64
	err := conn.Table(h.cfg.ParentTable).
		Where("tenant_id = ? AND "+h.cfg.ParentPK+" = ?", tenantID, id).Limit(1).Count(&n).Error
	if err != nil {
		return core.MapError(h.cfg.ParentTable, "get", err)
	}
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}

func (h *Handler) selectItems(conn *gorm.DB, tenantID string) *gorm.DB {
	t := h.cfg.Table
	return conn.Table(t).
		Select(t+"."+h.cfg.IDColumn+" AS attachment_id, "+t+"."+h.cfg.ParentColumn+" AS parent_id, "+
			t+".file_id, f.name, f.content_type, f.size, "+t+".created_at").
		Joins("JOIN files f ON f.file_id = "+t+".file_id AND f.deleted_at IS NULL").
		Where(t+".tenant_id = ?", tenantID)
}

func toItem(r row) Item {
	return Item{ID: r.ID, ParentID: r.ParentID, FileID: r.FileID, Name: r.Name, ContentType: r.ContentType,
		Size: r.Size, URL: "/api/v1/files/" + r.FileID + "/content", CreatedAt: r.CreatedAt}
}

func (h *Handler) list(c *gin.Context) {
	parentID, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	conn, tid, err := h.conn(ctx)
	if err != nil {
		response.Fail(c, err)
		return
	}
	if err := h.parentExists(conn, tid, parentID); err != nil {
		response.Fail(c, err)
		return
	}
	var rows []row
	err = h.selectItems(conn, tid).Where(h.cfg.Table+"."+h.cfg.ParentColumn+" = ?", parentID).
		Order(h.cfg.Table + ".created_at, " + h.cfg.Table + "." + h.cfg.IDColumn).Limit(h.cfg.Limit).Scan(&rows).Error
	if err != nil {
		response.Fail(c, core.MapError(h.cfg.Table, "list", err))
		return
	}
	items := make([]Item, 0, len(rows))
	for _, r := range rows {
		items = append(items, toItem(r))
	}
	response.OK(c, items)
}

func (h *Handler) attach(c *gin.Context) {
	parentID, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	var in attachInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	ctx := c.Request.Context()
	conn, tid, err := h.conn(ctx)
	if err != nil {
		response.Fail(c, err)
		return
	}
	if err := h.parentExists(conn, tid, parentID); err != nil {
		response.Fail(c, err)
		return
	}
	if _, err := h.files.GetByID(ctx, in.FileID); errors.Is(err, core.ErrNotFound) {
		response.Fail(c, &response.Error{Code: response.CodeValidationFailed, Message: "The file does not exist. Upload it first.",
			Details: []response.FieldError{{Field: "file_id", Rule: "exists"}}})
		return
	} else if err != nil {
		response.Fail(c, err)
		return
	}

	id := idgen.New()
	err = conn.Table(h.cfg.Table).Create(map[string]any{
		h.cfg.IDColumn: id, "tenant_id": tid, h.cfg.ParentColumn: parentID, "file_id": in.FileID,
	}).Error
	if err != nil {
		response.Fail(c, core.MapError(h.cfg.Table, "create", err))
		return
	}
	var r row
	if err := h.selectItems(conn, tid).Where(h.cfg.Table+"."+h.cfg.IDColumn+" = ?", id).Take(&r).Error; err != nil {
		response.Fail(c, core.MapError(h.cfg.Table, "get", err))
		return
	}
	response.Created(c, toItem(r))
}

func (h *Handler) detach(c *gin.Context) {
	parentID, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	attID, ok := request.UUIDParam(c, "attachment_id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	conn, tid, err := h.conn(ctx)
	if err != nil {
		response.Fail(c, err)
		return
	}
	var fileID string
	err = conn.Table(h.cfg.Table).Select("file_id").
		Where("tenant_id = ? AND "+h.cfg.IDColumn+" = ? AND "+h.cfg.ParentColumn+" = ?", tid, attID, parentID).
		Limit(1).Scan(&fileID).Error
	if err != nil {
		response.Fail(c, core.MapError(h.cfg.Table, "get", err))
		return
	}
	if fileID == "" {
		response.Abort(c, response.CodeNotFound, "")
		return
	}
	// the row first (it points to the file), then the file: its row and its content
	del := "DELETE FROM " + h.cfg.Table + " WHERE tenant_id = ? AND " + h.cfg.IDColumn + " = ?"
	if err := conn.Exec(del, tid, attID).Error; err != nil {
		response.Fail(c, core.MapError(h.cfg.Table, "delete", err))
		return
	}
	f, err := h.files.GetByID(ctx, fileID)
	if err == nil {
		if err = h.files.Delete(ctx, fileID); err == nil {
			_ = h.blobs.Delete(context.WithoutCancel(ctx), f.StorageKey) // an orphan at worst; see docs/files.md
		}
	}
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		response.Fail(c, err)
		return
	}
	response.Message(c, "Deleted.")
}
