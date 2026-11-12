package endpoint

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// Mirror makes the endpoints table of a dedicated tenant database equal to the control database: same ids, same
// method, path, scope and is_public, so group_endpoints rows point at the same endpoints everywhere. Rows that
// the control database no longer has are soft-deleted. It returns the number of rows written.
//
// The control database stays the source of truth (`tenantctl endpoint sync` writes there first).
func (r *Repository) Mirror(ctx context.Context, want []Endpoint) (int, error) {
	written := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", syncLockID).Error; err != nil {
			return fmt.Errorf("lock: %w", err)
		}
		ids := make([]string, 0, len(want))
		for _, e := range want {
			ids = append(ids, e.ID)
			out := tx.Exec(`INSERT INTO endpoints (endpoint_id, method, path, name, scope, is_public)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (endpoint_id) DO UPDATE
				SET method = EXCLUDED.method, path = EXCLUDED.path, name = EXCLUDED.name,
				    scope = EXCLUDED.scope, is_public = EXCLUDED.is_public, deleted_at = NULL, updated_at = now()
				WHERE endpoints.method <> EXCLUDED.method OR endpoints.path <> EXCLUDED.path OR endpoints.name <> EXCLUDED.name
				   OR endpoints.scope <> EXCLUDED.scope OR endpoints.is_public <> EXCLUDED.is_public OR endpoints.deleted_at IS NOT NULL`,
				e.ID, e.Method, e.Path, e.Name, e.Scope, e.IsPublic)
			if out.Error != nil {
				return fmt.Errorf("mirror %s %s: %w", e.Method, e.Path, out.Error)
			}
			written += int(out.RowsAffected)
		}
		q := tx.Model(&Endpoint{})
		if len(ids) > 0 {
			q = q.Where("endpoint_id NOT IN ?", ids)
		}
		if err := q.Delete(&Endpoint{}).Error; err != nil {
			return fmt.Errorf("mirror: remove stale: %w", err)
		}
		return nil
	})
	return written, err
}
