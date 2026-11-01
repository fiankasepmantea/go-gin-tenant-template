package user

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
)

// Repository for tenant-scoped users. The built-in methods (Create, GetByID, List, ...) are always limited to
// the tenant in the context. Platform admins have their own methods below, which only work in a platform session.
type Repository struct {
	*core.Repository[User, *User]
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{Repository: core.NewRepository[User](db), db: db}
}

// FindByUsername looks up a user in the tenant from the context (case-insensitive).
func (r *Repository) FindByUsername(ctx context.Context, username string) (*User, error) {
	conn, err := r.Conn(ctx)
	if err != nil {
		return nil, err
	}
	u := new(User)
	if err := conn.Where("lower(username) = lower(?)", username).Take(u).Error; err != nil {
		return nil, notFound(err)
	}
	return u, nil
}

// FindPlatformByUsername looks up a platform admin. Must be called inside a platform-level database.InTenantTx;
// in any other session RLS hides platform rows, so the result is ErrNotFound.
func (r *Repository) FindPlatformByUsername(ctx context.Context, username string) (*User, error) {
	u := new(User)
	err := database.From(ctx, r.db).
		Where("tenant_id IS NULL AND lower(username) = lower(?)", username).Take(u).Error
	if err != nil {
		return nil, notFound(err)
	}
	return u, nil
}

// FindPlatformByID looks up a platform admin by id. Same rules as FindPlatformByUsername.
func (r *Repository) FindPlatformByID(ctx context.Context, id string) (*User, error) {
	u := new(User)
	err := database.From(ctx, r.db).
		Where("tenant_id IS NULL AND user_id = ?", id).Take(u).Error
	if err != nil {
		return nil, notFound(err)
	}
	return u, nil
}

// SetPasswordHash replaces the password hash of a user (used to upgrade a hash to stronger parameters).
// A tenant user is updated inside the tenant from the context; a platform admin needs a platform-level session.
func (r *Repository) SetPasswordHash(ctx context.Context, u *User, hash string) error {
	if u.Level == auth.LevelPlatform {
		res := database.From(ctx, r.db).Model(&User{}).
			Where("tenant_id IS NULL AND user_id = ?", u.ID).Update("password_hash", hash)
		if res.Error != nil {
			return fmt.Errorf("users: set password hash: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return core.ErrNotFound
		}
		return nil
	}
	return r.Update(ctx, u.ID, map[string]any{"password_hash": hash})
}

// CreatePlatform creates a platform admin. It only succeeds in a platform-level session (guarded by RLS and a database CHECK).
func (r *Repository) CreatePlatform(ctx context.Context, u *User) error {
	u.ID = idgen.New()
	u.TenantID, u.GroupID = nil, nil
	u.Level = auth.LevelPlatform
	if err := database.From(ctx, r.db).Create(u).Error; err != nil {
		return fmt.Errorf("users: create platform: %w", err)
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return core.ErrNotFound
	}
	return err
}
