package cli

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/storage"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// Tables that are never exported: they hold only secrets (token hashes) that are of no use to the client.
var exportSkip = map[string]bool{"user_tokens": true, "password_resets": true}

// Columns removed from every exported row: secrets, and the internal storage key of a file.
const exportStrip = `- 'password_hash' - 'token_hash' - 'storage_key'`

// ExportManifest describes an export archive (manifest.json).
type ExportManifest struct {
	Format       int            `json:"format"`
	ExportedAt   time.Time      `json:"exported_at"`
	Tenant       map[string]any `json:"tenant"`
	Tables       map[string]int `json:"tables"`
	Files        int            `json:"files"`
	MissingFiles []string       `json:"missing_files,omitempty"`
}

// DeleteResult reports what DeleteTenant removed.
type DeleteResult struct {
	Rows         map[string]int64
	Files        int
	DedicatedRef string // set when the tenant had a dedicated database: the database itself is left to the operator
	FileErrors   []error
}

// createEntry adds a deflated entry with the current time (zip's Create leaves the date at 1980).
func createEntry(zw *zip.Writer, name string) (io.Writer, error) {
	return zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now().UTC()})
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// tenantTables lists the base tables of the current schema that have a tenant_id column, except "tenants" itself.
// Modules made by the generator are found automatically.
func tenantTables(db *gorm.DB) ([]string, error) {
	var names []string
	err := db.Raw(`SELECT c.table_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = current_schema() AND c.column_name = 'tenant_id' AND t.table_type = 'BASE TABLE'
		  AND c.table_name <> 'tenants'
		ORDER BY c.table_name`).Scan(&names).Error
	return names, err
}

// deleteOrder sorts tables so that a table that references another comes before it (children first).
func deleteOrder(db *gorm.DB, tables []string) ([]string, error) {
	var fks []struct{ Child, Parent string }
	err := db.Raw(`SELECT child.relname AS child, parent.relname AS parent
		FROM pg_constraint con
		JOIN pg_class child ON child.oid = con.conrelid
		JOIN pg_class parent ON parent.oid = con.confrelid
		JOIN pg_namespace n ON n.oid = child.relnamespace
		WHERE con.contype = 'f' AND n.nspname = current_schema()`).Scan(&fks).Error
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, t := range tables {
		in[t] = true
	}
	children := map[string]int{} // how many not yet deleted tables reference this one
	refs := map[string][]string{}
	for _, f := range fks {
		if f.Child == f.Parent || !in[f.Child] || !in[f.Parent] {
			continue // a self reference is fine: one DELETE statement checks the constraint at its end
		}
		children[f.Parent]++
		refs[f.Child] = append(refs[f.Child], f.Parent)
	}
	var ready, order []string
	for _, t := range tables {
		if children[t] == 0 {
			ready = append(ready, t)
		}
	}
	for len(ready) > 0 {
		sort.Strings(ready)
		t := ready[0]
		ready = ready[1:]
		order = append(order, t)
		for _, p := range refs[t] {
			if children[p]--; children[p] == 0 {
				ready = append(ready, p)
			}
		}
	}
	if len(order) != len(tables) {
		return nil, errors.New("tables of the tenant reference each other in a cycle: delete them by hand")
	}
	return order, nil
}

func (p *Provisioner) findTenant(ctx context.Context, tenantCode string) (*tenant.Tenant, error) {
	code, ok := tenantctx.NormalizeCode(tenantCode)
	if !ok {
		return nil, errors.New("invalid tenant code")
	}
	t, err := p.tenants.FindByCode(ctx, code)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("tenant %q does not exist", code)
	}
	return t, err
}

// ExportTenant writes everything the tenant owns to a zip archive at outPath: one JSON Lines file per table
// (data/<table>.jsonl), the uploaded files (files/<file_id>) and manifest.json. Secrets (password hashes and
// token hashes) are left out. It refuses to overwrite a file. store may be nil when the tenant has no files.
func (p *Provisioner) ExportTenant(ctx context.Context, tenantCode, outPath string, store storage.Store) (*ExportManifest, error) {
	t, err := p.findTenant(ctx, tenantCode)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is typed by the operator on the command line
	if err != nil {
		return nil, err
	}
	man := &ExportManifest{
		Format: 1, ExportedAt: time.Now().UTC(), Tables: map[string]int{},
		Tenant: map[string]any{"tenant_id": t.ID, "code": t.Code, "name": t.Name, "status": t.Status, "created_at": t.CreatedAt},
	}
	zw := zip.NewWriter(f)
	err = p.tx(tenantctx.WithID(ctx, t.ID), auth.LevelTenant, func(ctx context.Context) error {
		db := database.From(ctx, p.db)
		tables, err := tenantTables(db)
		if err != nil {
			return err
		}
		for _, table := range tables {
			if exportSkip[table] {
				continue
			}
			n, err := exportTable(db, zw, table, t.ID)
			if err != nil {
				return fmt.Errorf("export %s: %w", table, err)
			}
			man.Tables[table] = n
		}
		return exportFiles(ctx, db, zw, store, man)
	})
	if err == nil {
		var w io.Writer
		if w, err = createEntry(zw, "manifest.json"); err == nil {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			err = enc.Encode(man)
		}
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(outPath)
		return nil, err
	}
	return man, nil
}

func exportTable(db *gorm.DB, zw *zip.Writer, table, tenantID string) (int, error) {
	rows, err := db.Raw(`SELECT (to_jsonb(t) `+exportStrip+`)::text FROM `+quoteIdent(table)+` t WHERE tenant_id = ?`, tenantID).Rows()
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	w, err := createEntry(zw, "data/"+table+".jsonl")
	if err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return 0, err
		}
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return 0, err
		}
		n++
	}
	return n, rows.Err()
}

func exportFiles(ctx context.Context, db *gorm.DB, zw *zip.Writer, store storage.Store, man *ExportManifest) error {
	var files []struct {
		FileID     string `gorm:"column:file_id"`
		StorageKey string `gorm:"column:storage_key"`
	}
	if _, ok := man.Tables["files"]; !ok {
		return nil
	}
	if err := db.Raw(`SELECT file_id, storage_key FROM files WHERE deleted_at IS NULL ORDER BY file_id`).Scan(&files).Error; err != nil {
		return err
	}
	for _, f := range files {
		if store == nil {
			man.MissingFiles = append(man.MissingFiles, f.FileID)
			continue
		}
		rc, err := store.Open(ctx, f.StorageKey)
		if errors.Is(err, storage.ErrNotFound) {
			man.MissingFiles = append(man.MissingFiles, f.FileID)
			continue
		}
		if err != nil {
			return err
		}
		w, err := createEntry(zw, "files/"+f.FileID)
		if err == nil {
			_, err = io.Copy(w, rc)
		}
		_ = rc.Close()
		if err != nil {
			return err
		}
		man.Files++
	}
	return nil
}

// DeleteTenant permanently removes a tenant and everything it owns: every row of every table that has a tenant_id
// (found from the schema), the uploaded files, and the tenant itself, so that its code can be used again.
// Safety: the tenant must be suspended first, and confirm must repeat the tenant code. A dedicated database is not
// dropped; the operator does that. Export first (ExportTenant): this cannot be undone.
func (p *Provisioner) DeleteTenant(ctx context.Context, tenantCode, confirm string, store storage.Store) (*DeleteResult, error) {
	t, err := p.findTenant(ctx, tenantCode)
	if err != nil {
		return nil, err
	}
	if confirm != t.Code {
		return nil, fmt.Errorf("to delete tenant %q, repeat its code in --confirm", t.Code)
	}
	if t.Status != tenant.StatusSuspended {
		return nil, fmt.Errorf("tenant %q is %s: suspend it first (tenantctl tenant suspend --code %s)", t.Code, t.Status, t.Code)
	}

	res := &DeleteResult{Rows: map[string]int64{}}
	var keys []string
	err = p.tx(tenantctx.WithID(ctx, t.ID), auth.LevelTenant, func(ctx context.Context) error {
		db := database.From(ctx, p.db)
		tables, err := tenantTables(db)
		if err != nil {
			return err
		}
		order, err := deleteOrder(db, tables)
		if err != nil {
			return err
		}
		for _, table := range tables {
			if table == "files" {
				if err := db.Raw(`SELECT storage_key FROM files`).Scan(&keys).Error; err != nil {
					return err
				}
			}
		}
		for _, table := range order {
			r := db.Exec(`DELETE FROM `+quoteIdent(table)+` WHERE tenant_id = ?`, t.ID)
			if r.Error != nil {
				return fmt.Errorf("delete %s: %w", table, r.Error)
			}
			res.Rows[table] = r.RowsAffected
		}
		// the tenant row: in the shared database this is the tenant itself, in a dedicated one it is the copy
		return db.Exec(`DELETE FROM tenants WHERE tenant_id = ?`, t.ID).Error
	})
	if err != nil {
		return nil, err
	}
	if t.DBRef != nil {
		res.DedicatedRef = *t.DBRef
		if err := database.From(ctx, p.db).Exec(`DELETE FROM tenants WHERE tenant_id = ?`, t.ID).Error; err != nil {
			return res, fmt.Errorf("the data is deleted but the tenant row in the control database is not: %w", err)
		}
	}
	for _, k := range keys {
		if store == nil {
			res.FileErrors = append(res.FileErrors, fmt.Errorf("%s: no storage configured", k))
			continue
		}
		if err := store.Delete(ctx, k); err != nil {
			res.FileErrors = append(res.FileErrors, fmt.Errorf("%s: %w", k, err))
			continue
		}
		res.Files++
	}
	return res, nil
}
