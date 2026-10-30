package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

type txKey struct{}

// WithTx stores the active transaction in the context.
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// From returns the transaction in the context if there is one, or the plain db. Always bound to ctx.
// Repositories use this so that all queries within one request run in the same transaction.
func From(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx.WithContext(ctx)
	}
	return db.WithContext(ctx)
}

// ErrTenantRequired is returned when a tenant-level session is opened without a tenant in the context.
var ErrTenantRequired = errors.New("database: no tenant in context")

// InTenantTx runs fn inside a single transaction whose session variables are set for RLS:
//
//	app.current_tenant = tenant from the context (empty for platform level)
//	app.current_level  = level (auth.LevelTenant or auth.LevelPlatform)
//
// The variables are set with set_config(..., is_local=true), so they vanish when the transaction ends and
// do not leak to other requests through the connection pool. The level value must only come from a
// verified token, never from client input.
func InTenantTx(ctx context.Context, db *gorm.DB, level string, fn func(ctx context.Context) error) error {
	var tenantID string
	switch level {
	case auth.LevelTenant:
		id, ok := tenant.IDFrom(ctx)
		if !ok {
			return ErrTenantRequired
		}
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("database: tenant_id is not a UUID: %w", err)
		}
		tenantID = id
	case auth.LevelPlatform:
		// no tenant: a platform session sees across tenants according to policy
	default:
		return fmt.Errorf("database: unknown level %q", level)
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.current_tenant', ?, true)", tenantID).Error; err != nil {
			return fmt.Errorf("database: set tenant: %w", err)
		}
		if err := tx.Exec("SELECT set_config('app.current_level', ?, true)", level).Error; err != nil {
			return fmt.Errorf("database: set level: %w", err)
		}
		return fn(WithTx(ctx, tx))
	})
}
