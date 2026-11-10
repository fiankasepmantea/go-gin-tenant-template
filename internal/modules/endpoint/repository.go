package endpoint

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
)

// syncLockID is the PostgreSQL advisory lock that serializes synchronizations (two deploys at once).
const syncLockID int64 = 7_351_002

// Repository of the global endpoints table: there is no tenant filter.
type Repository struct {
	*core.Repository[Endpoint, *Endpoint]
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{Repository: core.NewRepository[Endpoint](db), db: db}
}

// All returns every endpoint that is not deleted, ordered by path and method.
func (r *Repository) All(ctx context.Context) ([]Endpoint, error) {
	var out []Endpoint
	err := database.From(ctx, r.db).Order("path ASC, method ASC").Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("endpoints: list: %w", err)
	}
	return out, nil
}

// SyncResult reports what Sync did.
type SyncResult struct {
	Total    int      // routes that need an endpoint
	Inserted int      // new rows
	Updated  int      // rows changed by an override
	Stale    []string // "METHOD path" of rows without a route (kept unless pruned)
	Pruned   int      // stale rows that were soft-deleted
}

// Sync makes the endpoints table match the routes. New routes get a row with no grants (so they are refused
// until someone grants them). Rows of routes that no longer exist are reported and, with prune, soft-deleted;
// they are kept by default because deleting one and later bringing the route back would lose its grants.
func (r *Repository) Sync(ctx context.Context, routes []Route, prune bool) (SyncResult, error) {
	routes = Wanted(routes)
	res := SyncResult{Total: len(routes)}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", syncLockID).Error; err != nil {
			return fmt.Errorf("lock: %w", err)
		}
		var existing []Endpoint
		if err := tx.Find(&existing).Error; err != nil {
			return fmt.Errorf("load: %w", err)
		}
		plan := BuildPlan(existing, routes)

		for _, e := range plan.Insert {
			// ON CONFLICT: a concurrent insert of the same route (another process without the lock) is fine.
			out := tx.Exec(`INSERT INTO endpoints (endpoint_id, method, path, name, scope, is_public)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (method, path) WHERE deleted_at IS NULL DO NOTHING`,
				idgen.New(), e.Method, e.Path, e.Name, e.Scope, e.IsPublic)
			if out.Error != nil {
				return fmt.Errorf("insert %s: %w", e.Key(), out.Error)
			}
			res.Inserted += int(out.RowsAffected)
		}
		for _, e := range plan.Update {
			if err := tx.Exec("UPDATE endpoints SET scope = ?, is_public = ? WHERE endpoint_id = ?",
				e.Scope, e.IsPublic, e.ID).Error; err != nil {
				return fmt.Errorf("update %s: %w", e.Key(), err)
			}
			res.Updated++
		}
		ids := make([]string, 0, len(plan.Stale))
		for _, e := range plan.Stale {
			res.Stale = append(res.Stale, e.Key())
			ids = append(ids, e.ID)
		}
		if prune && len(ids) > 0 {
			out := tx.Exec("UPDATE endpoints SET deleted_at = now() WHERE endpoint_id IN ? AND deleted_at IS NULL", ids)
			if out.Error != nil {
				return fmt.Errorf("prune: %w", out.Error)
			}
			res.Pruned = int(out.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, fmt.Errorf("endpoints: sync: %w", err)
	}
	return res, nil
}
