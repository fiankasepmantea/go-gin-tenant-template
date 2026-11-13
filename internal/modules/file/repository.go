package file

import (
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

// Repository of files; the built-in methods are limited to the tenant in the context.
type Repository struct {
	*core.Repository[File, *File]
}

// NewRepository creates the repository.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{Repository: core.NewRepository[File](db)}
}
