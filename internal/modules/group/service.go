package group

import (
	"context"
	"strings"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

// Store is what the service needs from the data layer. *Repository satisfies it, and a test can pass a fake.
type Store interface {
	Create(ctx context.Context, e *Group) error
	GetByID(ctx context.Context, id string) (*Group, error)
	List(ctx context.Context, p core.ListParams) (core.Page[Group], error)
	Update(ctx context.Context, id string, fields map[string]any) error
	Delete(ctx context.Context, id string) error
}

// Service holds the business rules of groups. The handler calls the service, the service calls the repository.
type Service struct {
	store Store
}

// NewService creates the service.
func NewService(store Store) *Service { return &Service{store: store} }

// Create stores a group. The name is trimmed: the unique index compares names without case, and a name with
// spaces around it would be a different group that looks the same.
func (s *Service) Create(ctx context.Context, g *Group) error {
	g.Name = strings.TrimSpace(g.Name)
	return s.store.Create(ctx, g)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Group, error) {
	return s.store.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, p core.ListParams) (core.Page[Group], error) {
	return s.store.List(ctx, p)
}

// Update changes the given columns (the same trimming as Create).
func (s *Service) Update(ctx context.Context, id string, fields map[string]any) error {
	if name, ok := fields["name"].(string); ok {
		fields["name"] = strings.TrimSpace(name)
	}
	return s.store.Update(ctx, id, fields)
}

func (s *Service) Delete(ctx context.Context, id string) error { return s.store.Delete(ctx, id) }
