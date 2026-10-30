package tenant

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
)

// Repository: global table, so there is no automatic tenant filter.
type Repository struct {
	*core.Repository[Tenant, *Tenant]
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{Repository: core.NewRepository[Tenant](db), db: db}
}

// FindByCode is used at login to turn a tenant code (subdomain/header) into a tenant_id.
// Suspended tenants are still returned; the service decides whether to reject them.
func (r *Repository) FindByCode(ctx context.Context, code string) (*Tenant, error) {
	t := new(Tenant)
	err := database.From(ctx, r.db).Where("code = ?", code).Take(t).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return t, nil
}

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return core.ErrNotFound
	}
	return err
}
