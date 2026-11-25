package group

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// Repository reuses all core.Repository behavior (CRUD + automatic tenant filter).
type Repository struct {
	*core.Repository[Group, *Group]
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{core.NewRepository[Group](db), db}
}

// CountUsers counts the users of the tenant in the context that belong to the group. Deleted users do not count.
func (r *Repository) CountUsers(ctx context.Context, groupID string) (int64, error) {
	tid, ok := tenant.IDFrom(ctx)
	if !ok {
		return 0, core.ErrNoTenant
	}
	var n int64
	err := database.From(ctx, r.db).Table("users").
		Where("tenant_id = ? AND group_id = ? AND deleted_at IS NULL", tid, groupID).
		Count(&n).Error
	return n, err
}

// FindByName looks a group up by name in the tenant of the context (case-insensitive, like the unique index).
func (r *Repository) FindByName(ctx context.Context, name string) (*Group, error) {
	conn, err := r.Conn(ctx)
	if err != nil {
		return nil, err
	}
	g := new(Group)
	if err := conn.Where("lower(name) = lower(?)", strings.TrimSpace(name)).Take(g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, core.ErrNotFound
		}
		return nil, err
	}
	return g, nil
}
