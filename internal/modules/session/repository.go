package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// Repository stores sessions. It runs inside the transaction from database.InTenantTx, so RLS is the
// second layer; the explicit tenant filter below is the first.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

// scoped limits a query to the session's own tenant, or to platform rows when the context has no tenant.
func (r *Repository) scoped(ctx context.Context) *gorm.DB {
	conn := database.From(ctx, r.db)
	if id, ok := tenant.IDFrom(ctx); ok {
		return conn.Where("tenant_id = ?", id)
	}
	return conn.Where("tenant_id IS NULL")
}

// Create stores a new session row.
func (r *Repository) Create(ctx context.Context, t *UserToken) error {
	if err := database.From(ctx, r.db).Create(t).Error; err != nil {
		return fmt.Errorf("user_tokens: create: %w", err)
	}
	return nil
}

// FindByHash returns the session for a refresh token hash, or core.ErrNotFound.
func (r *Repository) FindByHash(ctx context.Context, hash []byte) (*UserToken, error) {
	t := new(UserToken)
	if err := r.scoped(ctx).Where("token_hash = ?", hash).Take(t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, core.ErrNotFound
		}
		return nil, fmt.Errorf("user_tokens: find: %w", err)
	}
	return t, nil
}

// Revoke marks a session as revoked. It reports whether THIS call did it: false means it was already
// revoked (or does not exist), which is how two concurrent refreshes with the same token are told apart.
func (r *Repository) Revoke(ctx context.Context, id string, at time.Time) (bool, error) {
	res := r.scoped(ctx).Model(&UserToken{}).
		Where("user_token_id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", at)
	if res.Error != nil {
		return false, fmt.Errorf("user_tokens: revoke: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// RevokeAllForUser revokes every active session of a user (used when refresh token reuse is detected).
func (r *Repository) RevokeAllForUser(ctx context.Context, userID string, at time.Time) error {
	err := r.scoped(ctx).Model(&UserToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", at).Error
	if err != nil {
		return fmt.Errorf("user_tokens: revoke all: %w", err)
	}
	return nil
}
