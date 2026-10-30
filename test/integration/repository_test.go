//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/group"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
)

func inTenant(t *testing.T, e *env, ctx context.Context, fn func(ctx context.Context)) {
	t.Helper()
	err := database.InTenantTx(ctx, e.app, auth.LevelTenant, func(ctx context.Context) error {
		fn(ctx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGroup_TenantIsolation(t *testing.T) {
	e := setup(t)
	idA, ctxA := e.newTenant(t, "gedunga")
	_, ctxB := e.newTenant(t, "gedungb")
	repo := group.NewRepository(e.app)

	var gid string
	inTenant(t, e, ctxA, func(ctx context.Context) {
		g := &group.Group{Name: "admin"}
		if err := repo.Create(ctx, g); err != nil {
			t.Fatalf("create: %v", err)
		}
		gid = g.ID
		if g.TenantID != idA {
			t.Errorf("tenant_id = %q, want %q", g.TenantID, idA)
		}
	})

	inTenant(t, e, ctxB, func(ctx context.Context) {
		if _, err := repo.GetByID(ctx, gid); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("cross-tenant GetByID: %v, want ErrNotFound", err)
		}
		if p, err := repo.List(ctx, core.ListParams{}); err != nil || p.Total != 0 || len(p.Items) != 0 {
			t.Errorf("List tenant B: %+v err=%v, want empty", p, err)
		}
		if err := repo.Update(ctx, gid, map[string]any{"name": "hijacked"}); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("cross-tenant Update: %v", err)
		}
		if err := repo.Delete(ctx, gid); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("cross-tenant Delete: %v", err)
		}
	})

	inTenant(t, e, ctxA, func(ctx context.Context) {
		g, err := repo.GetByID(ctx, gid)
		if err != nil || g.Name != "admin" {
			t.Fatalf("GetByID own tenant: %+v err=%v", g, err)
		}
		if err := repo.Update(ctx, gid, map[string]any{"name": "super"}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if err := repo.Delete(ctx, gid); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := repo.GetByID(ctx, gid); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("after soft delete: %v", err)
		}
	})
}

func TestGroup_ClientTenantIDIgnored(t *testing.T) {
	e := setup(t)
	idA, ctxA := e.newTenant(t, "gedunga")
	idB, _ := e.newTenant(t, "gedungb")
	repo := group.NewRepository(e.app)

	inTenant(t, e, ctxA, func(ctx context.Context) {
		g := &group.Group{Name: "x", TenantID: idB} // the client tries to write to another tenant
		if err := repo.Create(ctx, g); err != nil {
			t.Fatal(err)
		}
		if g.TenantID != idA {
			t.Errorf("tenant_id = %q, must be forced to %q", g.TenantID, idA)
		}
	})
}

func TestRepository_FailClosed(t *testing.T) {
	e := setup(t)
	_, ctxA := e.newTenant(t, "gedunga")
	repo := group.NewRepository(e.app)

	// No tenant in the context: rejected at the application layer.
	if err := repo.Create(context.Background(), &group.Group{Name: "x"}); !errors.Is(err, core.ErrNoTenant) {
		t.Errorf("no tenant: %v, want ErrNoTenant", err)
	}
	if _, err := repo.List(context.Background(), core.ListParams{}); !errors.Is(err, core.ErrNoTenant) {
		t.Errorf("List without tenant: %v", err)
	}
	// A tenant is in the context but InTenantTx was not opened: RLS rejects it (empty session variable).
	if err := repo.Create(ctxA, &group.Group{Name: "x"}); err == nil {
		t.Error("insert without a tenant transaction should be rejected by RLS")
	}
}

// The owner connection bypasses RLS. This proves the application layer (WHERE tenant_id) works on its own.
func TestRepository_AppLayerWithoutRLS(t *testing.T) {
	e := setup(t)
	_, ctxA := e.newTenant(t, "gedunga")
	_, ctxB := e.newTenant(t, "gedungb")
	repo := group.NewRepository(e.owner)

	for _, c := range []struct {
		ctx  context.Context
		name string
	}{{ctxA, "a1"}, {ctxA, "a2"}, {ctxB, "b1"}} {
		if err := repo.Create(c.ctx, &group.Group{Name: c.name}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := repo.List(ctxA, core.ListParams{})
	if err != nil || p.Total != 2 || len(p.Items) != 2 {
		t.Fatalf("List A: total=%d items=%d err=%v, want 2", p.Total, len(p.Items), err)
	}
	if p.Items[0].Name != "a1" || p.Items[1].Name != "a2" {
		t.Errorf("order must follow creation time (UUID v7): %q, %q", p.Items[0].Name, p.Items[1].Name)
	}
}

func TestRepository_UpdateProtectedColumns(t *testing.T) {
	e := setup(t)
	_, ctxA := e.newTenant(t, "gedunga")
	repo := group.NewRepository(e.owner)
	g := &group.Group{Name: "x"}
	if err := repo.Create(ctxA, g); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"tenant_id", "group_id", "group_unique", "created_at", "deleted_at"} {
		if err := repo.Update(ctxA, g.ID, map[string]any{col: "x"}); !errors.Is(err, core.ErrProtectedField) {
			t.Errorf("column %s: %v, want ErrProtectedField", col, err)
		}
	}
	if err := repo.Update(ctxA, g.ID, map[string]any{}); !errors.Is(err, core.ErrNoFields) {
		t.Errorf("no columns: %v", err)
	}
}

func TestRepository_Pagination(t *testing.T) {
	e := setup(t)
	_, ctxA := e.newTenant(t, "gedunga")
	repo := group.NewRepository(e.owner)
	for _, n := range []string{"g1", "g2", "g3", "g4", "g5"} {
		if err := repo.Create(ctxA, &group.Group{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := repo.List(ctxA, core.ListParams{Page: 2, PerPage: 2})
	if err != nil || p.Total != 5 || len(p.Items) != 2 || p.Items[0].Name != "g3" {
		t.Errorf("page 2: %+v err=%v", p, err)
	}
}

func TestTenant_FindByCode(t *testing.T) {
	e := setup(t)
	id, _ := e.newTenant(t, "gedunga")
	repo := tenant.NewRepository(e.app)

	got, err := repo.FindByCode(context.Background(), "gedunga")
	if err != nil || got.ID != id || got.Status != tenant.StatusActive {
		t.Fatalf("FindByCode: %+v err=%v", got, err)
	}
	if _, err := repo.FindByCode(context.Background(), "nonexistent"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown code: %v", err)
	}
}
