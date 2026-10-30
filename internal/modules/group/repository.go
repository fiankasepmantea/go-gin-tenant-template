package group

import (
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

// Repository reuses all core.Repository behavior (CRUD + automatic tenant filter).
type Repository struct {
	*core.Repository[Group, *Group]
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{core.NewRepository[Group](db)}
}
