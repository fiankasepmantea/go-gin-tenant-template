// Package core holds the generic building blocks shared by all modules.
package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// Repository is a generic repository with automatic tenant isolation (application layer).
// The second layer is RLS in PostgreSQL; both are deliberately applied together.
//
// Modules use it through embedding and add their own custom queries:
//
//	type Repository struct{ *core.Repository[Group, *Group] }
type Repository[T any, P Entity[T]] struct {
	db *gorm.DB
}

// NewRepository creates a repository for model T. P is inferred automatically: core.NewRepository[Group](db).
func NewRepository[T any, P Entity[T]](db *gorm.DB) *Repository[T, P] {
	return &Repository[T, P]{db: db}
}

func isTenantOwned[T any, P Entity[T]]() bool {
	_, ok := any(P(new(T))).(TenantOwned)
	return ok
}

// Conn returns the connection/transaction for this request, already scoped to the table and tenant.
// Modules use it to write custom queries without skipping the tenant filter.
func (r *Repository[T, P]) Conn(ctx context.Context) (*gorm.DB, error) {
	e := P(new(T))
	conn := database.From(ctx, r.db).Table(e.TableName())
	if isTenantOwned[T, P]() {
		id, ok := tenant.IDFrom(ctx)
		if !ok {
			return nil, ErrNoTenant
		}
		conn = conn.Where(e.TableName()+".tenant_id = ?", id)
	}
	return conn, nil
}

// Create stores a new record. The ID is generated automatically (UUID v7) if empty, and tenant_id is filled from the context.
func (r *Repository[T, P]) Create(ctx context.Context, e P) error {
	if e.GetID() == "" {
		e.SetID(idgen.New())
	}
	if owned, ok := any(e).(TenantOwned); ok {
		id, ok := tenant.IDFrom(ctx)
		if !ok {
			return ErrNoTenant
		}
		owned.SetTenantID(id) // always overwrites: values from the client are not trusted
	}
	if err := database.From(ctx, r.db).Create(e).Error; err != nil {
		return fmt.Errorf("%s: create: %w", e.TableName(), err)
	}
	return nil
}

// GetByID fetches a single record. A record belonging to another tenant returns ErrNotFound.
func (r *Repository[T, P]) GetByID(ctx context.Context, id string) (P, error) {
	conn, err := r.Conn(ctx)
	if err != nil {
		return nil, err
	}
	e := P(new(T))
	if err := conn.Where(e.PrimaryKey()+" = ?", id).Take(e).Error; err != nil {
		return nil, mapErr(e.TableName(), "get", err)
	}
	return e, nil
}

// ListParams controls pagination.
type ListParams struct {
	Page    int // starts at 1
	PerPage int // default 20, maximum 100
}

func (p ListParams) normalize() (limit, offset int) {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PerPage < 1 {
		p.PerPage = 20
	}
	if p.PerPage > 100 {
		p.PerPage = 100
	}
	return p.PerPage, (p.Page - 1) * p.PerPage
}

// Page is the result of List.
type Page[T any] struct {
	Items   []T
	Total   int64
	Page    int
	PerPage int
}

// List fetches records ordered from oldest to newest. UUID v7 is time-ordered, so primary key order = creation order.
func (r *Repository[T, P]) List(ctx context.Context, p ListParams) (Page[T], error) {
	limit, offset := p.normalize()
	out := Page[T]{Items: []T{}, Page: offset/limit + 1, PerPage: limit}

	conn, err := r.Conn(ctx)
	if err != nil {
		return out, err
	}
	e := P(new(T))
	if err := conn.Model(e).Count(&out.Total).Error; err != nil {
		return out, mapErr(e.TableName(), "count", err)
	}
	if err := conn.Order(e.PrimaryKey() + " ASC").Limit(limit).Offset(offset).Find(&out.Items).Error; err != nil {
		return out, mapErr(e.TableName(), "list", err)
	}
	return out, nil
}

// protected lists the columns that must not be changed through Update.
var protected = []string{"tenant_id", "created_at", "deleted_at"}

// Update changes specific columns (database column names). The primary key, *_unique, tenant_id,
// created_at, and deleted_at columns are rejected. updated_at is set by a database trigger.
func (r *Repository[T, P]) Update(ctx context.Context, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return ErrNoFields
	}
	e := P(new(T))
	for k := range fields {
		if k == e.PrimaryKey() || slices.Contains(protected, k) || k == uniqueColumn(e.PrimaryKey()) {
			return fmt.Errorf("%w: %s", ErrProtectedField, k)
		}
	}
	conn, err := r.Conn(ctx)
	if err != nil {
		return err
	}
	res := conn.Where(e.PrimaryKey()+" = ?", id).Updates(fields)
	if res.Error != nil {
		return mapErr(e.TableName(), "update", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete soft-deletes (sets deleted_at). The model must have a gorm.DeletedAt field.
func (r *Repository[T, P]) Delete(ctx context.Context, id string) error {
	conn, err := r.Conn(ctx)
	if err != nil {
		return err
	}
	e := P(new(T))
	res := conn.Where(e.PrimaryKey()+" = ?", id).Delete(e)
	if res.Error != nil {
		return mapErr(e.TableName(), "delete", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// uniqueColumn: by convention the identity column is "<t>_unique" for primary key "<t>_id".
func uniqueColumn(pk string) string {
	if base, ok := strings.CutSuffix(pk, "_id"); ok {
		return base + "_unique"
	}
	return ""
}

func mapErr(table, op string, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %s: %w", table, op, err)
}
