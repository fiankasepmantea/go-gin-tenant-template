//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
)

func inPlatform(t *testing.T, e *env, fn func(ctx context.Context) error) error {
	t.Helper()
	return database.InTenantTx(context.Background(), e.app, auth.LevelPlatform, fn)
}

func TestUser_PlatformAdminProtected(t *testing.T) {
	e := setup(t)
	_, ctxA := e.newTenant(t, "gedunga")
	repo := user.NewRepository(e.app)

	root := &user.User{Username: "root", Email: "root@x.id", Name: "Root", PasswordHash: "h", IsActive: true}
	if err := inPlatform(t, e, func(ctx context.Context) error { return repo.CreatePlatform(ctx, root) }); err != nil {
		t.Fatalf("create platform admin: %v", err)
	}
	if root.Level != auth.LevelPlatform || root.TenantID != nil {
		t.Errorf("wrong level/tenant: %+v", root)
	}

	// the same email (different case) is rejected by the partial unique index for platform rows
	dup := &user.User{Username: "root2", Email: "ROOT@x.id", Name: "R2", PasswordHash: "h"}
	if err := inPlatform(t, e, func(ctx context.Context) error { return repo.CreatePlatform(ctx, dup) }); err == nil {
		t.Error("duplicate platform email should be rejected")
	}

	// a platform session can find it
	err := inPlatform(t, e, func(ctx context.Context) error {
		u, err := repo.FindPlatformByUsername(ctx, "ROOT")
		if err != nil || u.ID != root.ID {
			t.Errorf("FindPlatformByUsername: %+v err=%v", u, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// a tenant session cannot see it, and cannot create a platform admin
	inTenant(t, e, ctxA, func(ctx context.Context) {
		if _, err := repo.FindPlatformByUsername(ctx, "root"); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("tenant sees platform admin: %v", err)
		}
	})
	// an error inside a transaction aborts it, so check the return value of InTenantTx instead
	h := &user.User{Username: "h", Email: "h@x.id", Name: "H", PasswordHash: "h"}
	err = database.InTenantTx(ctxA, e.app, auth.LevelTenant, func(ctx context.Context) error {
		return repo.CreatePlatform(ctx, h)
	})
	if err == nil {
		t.Error("tenant session created a platform admin, should be rejected")
	}
}

func TestUser_UniquePerTenant(t *testing.T) {
	e := setup(t)
	_, ctxA := e.newTenant(t, "gedunga")
	_, ctxB := e.newTenant(t, "gedungb")
	repo := user.NewRepository(e.app)
	mk := func(name string) *user.User {
		return &user.User{Username: "budi", Email: name + "@x.id", Name: "Budi", PasswordHash: "h", IsActive: true}
	}

	inTenant(t, e, ctxA, func(ctx context.Context) {
		if err := repo.Create(ctx, mk("a")); err != nil {
			t.Fatalf("create A: %v", err)
		}
	})
	inTenant(t, e, ctxB, func(ctx context.Context) {
		// same username in another tenant: allowed
		if err := repo.Create(ctx, mk("b")); err != nil {
			t.Fatalf("same username in another tenant must be allowed: %v", err)
		}
		u, err := repo.FindByUsername(ctx, "BUDI")
		if err != nil || u.Email != "b@x.id" {
			t.Errorf("FindByUsername must find only tenant B's user: %+v err=%v", u, err)
		}
	})
	err := database.InTenantTx(ctxA, e.app, auth.LevelTenant, func(ctx context.Context) error {
		return repo.Create(ctx, mk("a2")) // same username in the same tenant
	})
	if err == nil {
		t.Error("duplicate username within one tenant should be rejected")
	}
}
