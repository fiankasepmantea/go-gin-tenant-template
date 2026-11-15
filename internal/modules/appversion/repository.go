package appversion

import (
	"context"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

// Repository of app versions. The table is global, so core.Repository applies no tenant filter.
type Repository struct {
	*core.Repository[AppVersion, *AppVersion]
}

// NewRepository creates the repository.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{core.NewRepository[AppVersion](db)}
}

// ByPlatform returns every version of a platform that was not deleted. Which one is the latest is decided by
// comparing the numbers (Version.Compare), which SQL on text columns cannot do.
func (r *Repository) ByPlatform(ctx context.Context, platform string) ([]AppVersion, error) {
	conn, err := r.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var rows []AppVersion
	if err := conn.Where("platform = ?", platform).Find(&rows).Error; err != nil {
		return nil, core.MapError("app_versions", "by platform", err)
	}
	return rows, nil
}
