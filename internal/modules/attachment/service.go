package attachment

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/file"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// ErrFileNotFound: the file to attach does not exist in the tenant (it must be uploaded first).
var ErrFileNotFound = errors.New("attachment: file not found")

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

type row struct {
	ID          string    `gorm:"column:attachment_id"`
	ParentID    string    `gorm:"column:parent_id"`
	FileID      string    `gorm:"column:file_id"`
	Name        string    `gorm:"column:name"`
	ContentType string    `gorm:"column:content_type"`
	Size        int64     `gorm:"column:size"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// Files is what the service needs from the file module. *file.Service satisfies it.
type Files interface {
	GetByID(ctx context.Context, id string) (*file.File, error)
	Delete(ctx context.Context, id string) error
}

// Service holds the rules of attaching files to the records of one table: the record and the file must exist
// in the tenant, and detaching removes the file with it. The handler calls the service.
type Service struct {
	cfg   Config
	db    *gorm.DB
	files Files
}

// NewService creates the service. It panics on an unusable Config, a programming error found at startup.
func NewService(cfg Config, db *gorm.DB, files Files) *Service {
	if err := cfg.validate(); err != nil {
		panic(err)
	}
	return &Service{cfg: cfg, db: db, files: files}
}

// Config returns the configuration the service was made with (defaults filled in).
func (s *Service) Config() Config { return s.cfg }

func (s *Service) conn(ctx context.Context) (*gorm.DB, string, error) {
	id, ok := tenant.IDFrom(ctx)
	if !ok {
		return nil, "", core.ErrNoTenant
	}
	return database.From(ctx, s.db), id, nil
}

// parentExists answers ErrNotFound for a record that is missing or belongs to another tenant (RLS hides it).
func (s *Service) parentExists(conn *gorm.DB, tenantID, id string) error {
	var n int64
	err := conn.Table(s.cfg.ParentTable).
		Where("tenant_id = ? AND "+s.cfg.ParentPK+" = ?", tenantID, id).Limit(1).Count(&n).Error
	if err != nil {
		return core.MapError(s.cfg.ParentTable, "get", err)
	}
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}

func (s *Service) selectItems(conn *gorm.DB, tenantID string) *gorm.DB {
	t := s.cfg.Table
	return conn.Table(t).
		Select(t+"."+s.cfg.IDColumn+" AS attachment_id, "+t+"."+s.cfg.ParentColumn+" AS parent_id, "+
			t+".file_id, f.name, f.content_type, f.size, "+t+".created_at").
		Joins("JOIN files f ON f.file_id = "+t+".file_id AND f.deleted_at IS NULL").
		Where(t+".tenant_id = ?", tenantID)
}

func toItem(r row) Item {
	return Item{ID: r.ID, ParentID: r.ParentID, FileID: r.FileID, Name: r.Name, ContentType: r.ContentType,
		Size: r.Size, URL: "/api/v1/files/" + r.FileID + "/content", CreatedAt: r.CreatedAt}
}

// List returns the files attached to a record, oldest first.
func (s *Service) List(ctx context.Context, parentID string) ([]Item, error) {
	conn, tid, err := s.conn(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.parentExists(conn, tid, parentID); err != nil {
		return nil, err
	}
	var rows []row
	err = s.selectItems(conn, tid).Where(s.cfg.Table+"."+s.cfg.ParentColumn+" = ?", parentID).
		Order(s.cfg.Table + ".created_at, " + s.cfg.Table + "." + s.cfg.IDColumn).Limit(s.cfg.Limit).Scan(&rows).Error
	if err != nil {
		return nil, core.MapError(s.cfg.Table, "list", err)
	}
	items := make([]Item, 0, len(rows))
	for _, r := range rows {
		items = append(items, toItem(r))
	}
	return items, nil
}

// Attach attaches an uploaded file to a record.
func (s *Service) Attach(ctx context.Context, parentID, fileID string) (*Item, error) {
	conn, tid, err := s.conn(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.parentExists(conn, tid, parentID); err != nil {
		return nil, err
	}
	if _, err := s.files.GetByID(ctx, fileID); errors.Is(err, core.ErrNotFound) {
		return nil, ErrFileNotFound
	} else if err != nil {
		return nil, err
	}

	id := idgen.New()
	err = conn.Table(s.cfg.Table).Create(map[string]any{
		s.cfg.IDColumn: id, "tenant_id": tid, s.cfg.ParentColumn: parentID, "file_id": fileID,
	}).Error
	if err != nil {
		return nil, core.MapError(s.cfg.Table, "create", err)
	}
	var r row
	if err := s.selectItems(conn, tid).Where(s.cfg.Table+"."+s.cfg.IDColumn+" = ?", id).Take(&r).Error; err != nil {
		return nil, core.MapError(s.cfg.Table, "get", err)
	}
	item := toItem(r)
	return &item, nil
}

// Detach removes an attachment and deletes its file: the row first (it points to the file), then the file with
// its content. An attachment of another record or tenant is ErrNotFound.
func (s *Service) Detach(ctx context.Context, parentID, attachmentID string) error {
	conn, tid, err := s.conn(ctx)
	if err != nil {
		return err
	}
	var fileID string
	err = conn.Table(s.cfg.Table).Select("file_id").
		Where("tenant_id = ? AND "+s.cfg.IDColumn+" = ? AND "+s.cfg.ParentColumn+" = ?", tid, attachmentID, parentID).
		Limit(1).Scan(&fileID).Error
	if err != nil {
		return core.MapError(s.cfg.Table, "get", err)
	}
	if fileID == "" {
		return core.ErrNotFound
	}
	del := "DELETE FROM " + s.cfg.Table + " WHERE tenant_id = ? AND " + s.cfg.IDColumn + " = ?"
	if err := conn.Exec(del, tid, attachmentID).Error; err != nil {
		return core.MapError(s.cfg.Table, "delete", err)
	}
	if err := s.files.Delete(ctx, fileID); err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	return nil
}
