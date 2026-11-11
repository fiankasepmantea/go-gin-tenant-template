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

// PasswordReset is one request to reset a password. Only the SHA-256 hash of the token is stored.
type PasswordReset struct {
	ID        string         `gorm:"column:password_reset_id;primaryKey"`
	Unique    int64          `gorm:"column:password_reset_unique;->"`
	TenantID  *string        `gorm:"column:tenant_id"` // nil for platform admins
	UserID    string         `gorm:"column:user_id"`
	TokenHash []byte         `gorm:"column:token_hash"`
	IPAddress string         `gorm:"column:ip_address"`
	ExpiresAt time.Time      `gorm:"column:expires_at"`
	UsedAt    *time.Time     `gorm:"column:used_at"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (*PasswordReset) TableName() string { return "password_resets" }

// ResetRepository stores reset requests. Like Repository, it runs in the transaction of database.InTenantTx
// (RLS is the second layer) and filters on the tenant of the context itself (the first layer).
type ResetRepository struct {
	db *gorm.DB
}

func NewResetRepository(db *gorm.DB) *ResetRepository { return &ResetRepository{db: db} }

func (r *ResetRepository) scoped(ctx context.Context) *gorm.DB {
	conn := database.From(ctx, r.db)
	if id, ok := tenant.IDFrom(ctx); ok {
		return conn.Where("tenant_id = ?", id)
	}
	return conn.Where("tenant_id IS NULL")
}

func (r *ResetRepository) Create(ctx context.Context, p *PasswordReset) error {
	if err := database.From(ctx, r.db).Create(p).Error; err != nil {
		return fmt.Errorf("password_resets: create: %w", err)
	}
	return nil
}

func (r *ResetRepository) FindByHash(ctx context.Context, hash []byte) (*PasswordReset, error) {
	p := new(PasswordReset)
	if err := r.scoped(ctx).Where("token_hash = ?", hash).Take(p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, core.ErrNotFound
		}
		return nil, fmt.Errorf("password_resets: find: %w", err)
	}
	return p, nil
}

// Consume marks a request as used. It reports whether THIS call did it, and only does it while the request is
// unused and not expired, so two concurrent resets with one token cannot both succeed.
func (r *ResetRepository) Consume(ctx context.Context, id string, at time.Time) (bool, error) {
	res := r.scoped(ctx).Model(&PasswordReset{}).
		Where("password_reset_id = ? AND used_at IS NULL AND expires_at > ?", id, at).
		Update("used_at", at)
	if res.Error != nil {
		return false, fmt.Errorf("password_resets: consume: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// InvalidateForUser marks every unused request of the user as used (a new request replaces the older ones, and a
// successful reset or password change cancels the rest).
func (r *ResetRepository) InvalidateForUser(ctx context.Context, userID string, at time.Time) error {
	err := r.scoped(ctx).Model(&PasswordReset{}).
		Where("user_id = ? AND used_at IS NULL", userID).
		Update("used_at", at).Error
	if err != nil {
		return fmt.Errorf("password_resets: invalidate: %w", err)
	}
	return nil
}
