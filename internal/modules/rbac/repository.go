package rbac

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// UserAccess is what authorization needs to know about a user.
type UserAccess struct {
	GroupID *string // nil: the user has no group and can only use public endpoints
	Active  bool
}

// Repository reads and writes grants. Every query uses the tenant of the context and runs inside the request's
// tenant transaction, so row level security applies as well.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

// UserAccess returns the group and the active flag of a user of the tenant in the context.
// core.ErrNotFound means there is no such (undeleted) user in this tenant.
func (r *Repository) UserAccess(ctx context.Context, userID string) (UserAccess, error) {
	tenantID, ok := tenant.IDFrom(ctx)
	if !ok {
		return UserAccess{}, core.ErrNoTenant
	}
	var row struct {
		GroupID  *string
		IsActive bool
	}
	err := database.From(ctx, r.db).Table("users").Select("group_id, is_active").
		Where("tenant_id = ? AND user_id = ? AND deleted_at IS NULL", tenantID, userID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return UserAccess{}, core.ErrNotFound
	}
	if err != nil {
		return UserAccess{}, fmt.Errorf("rbac: user access: %w", err)
	}
	return UserAccess{GroupID: row.GroupID, Active: row.IsActive}, nil
}

// GroupEndpointIDs returns the ids of the endpoints granted to a group of the tenant in the context.
// A deleted group has no grants, even if its grant rows still exist.
func (r *Repository) GroupEndpointIDs(ctx context.Context, groupID string) ([]string, error) {
	tenantID, ok := tenant.IDFrom(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	var ids []string
	err := database.From(ctx, r.db).Raw(`
		SELECT ge.endpoint_id::text
		FROM group_endpoints ge
		JOIN groups g ON g.tenant_id = ge.tenant_id AND g.group_id = ge.group_id AND g.deleted_at IS NULL
		JOIN endpoints e ON e.endpoint_id = ge.endpoint_id AND e.deleted_at IS NULL
		WHERE ge.tenant_id = ? AND ge.group_id = ? AND ge.deleted_at IS NULL
		ORDER BY ge.endpoint_id`, tenantID, groupID).Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("rbac: group endpoints: %w", err)
	}
	return ids, nil
}

// ReplaceGroupEndpoints makes the grants of a group exactly the given endpoints: grants that are not in the
// list are soft-deleted, missing ones are added. The caller has already checked that the group and the
// endpoints exist. It must run inside the request transaction, so a failure leaves the old grants untouched.
func (r *Repository) ReplaceGroupEndpoints(ctx context.Context, groupID string, endpointIDs []string) error {
	tenantID, ok := tenant.IDFrom(ctx)
	if !ok {
		return core.ErrNoTenant
	}
	conn := database.From(ctx, r.db)

	revoke := conn.Table("group_endpoints").
		Where("tenant_id = ? AND group_id = ? AND deleted_at IS NULL", tenantID, groupID)
	if len(endpointIDs) > 0 {
		revoke = revoke.Where("endpoint_id NOT IN ?", endpointIDs)
	}
	if err := revoke.Update("deleted_at", gorm.Expr("now()")).Error; err != nil {
		return fmt.Errorf("rbac: revoke grants: %w", err)
	}

	if len(endpointIDs) == 0 {
		return nil
	}
	rows := make([]GroupEndpoint, 0, len(endpointIDs))
	for _, id := range endpointIDs {
		rows = append(rows, GroupEndpoint{ID: idgen.New(), TenantID: tenantID, GroupID: groupID, EndpointID: id})
	}
	// DO NOTHING: a grant that is already active (partial unique index) stays as it is.
	err := conn.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 500).Error
	if err != nil {
		return fmt.Errorf("rbac: add grants: %w", err)
	}
	return nil
}
