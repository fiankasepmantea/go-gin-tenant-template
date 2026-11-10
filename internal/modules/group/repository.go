package group

import (
	"context"
	"errors"
	"strings"

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
