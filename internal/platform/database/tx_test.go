package database

import (
	"context"
	"errors"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// Validation happens before touching the database, so a nil db is safe here.
func TestInTenantTx_Validation(t *testing.T) {
	called := func(context.Context) error { t.Error("fn must not be called"); return errors.New("unexpected call") }

	tests := []struct {
		name  string
		ctx   context.Context
		level string
		want  error
	}{
		{"tenant without a tenant in context", context.Background(), auth.LevelTenant, ErrTenantRequired},
		{"tenant_id is not a uuid", tenant.WithID(context.Background(), "'; drop table users;--"), auth.LevelTenant, nil},
		{"unknown level", context.Background(), "root", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := InTenantTx(tc.ctx, nil, tc.level, called)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
