package session

import (
	"context"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
)

// NewTxRunner returns the production TxRunner: one PostgreSQL transaction per call, with the RLS variables set.
func NewTxRunner(db *gorm.DB) TxRunner {
	return func(ctx context.Context, level string, fn func(ctx context.Context) error) error {
		return database.InTenantTx(ctx, db, level, fn)
	}
}
