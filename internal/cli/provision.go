package cli

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/group"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/rbac"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// ErrAlreadyExists is returned when the tenant code, username or email is already taken.
var ErrAlreadyExists = errors.New("already exists")

// Provisioner creates the first records of a new installation: a tenant, its users and platform admins.
// There is no public sign-up, so this is how the very first account comes into existence.
type Provisioner struct {
	db      *gorm.DB
	hasher  *password.Hasher
	tenants *tenant.Repository
	users   *user.Repository
	groups  *group.Repository
	eps     *endpoint.Repository
	rbac    *rbac.Repository
}

// NewProvisioner creates a Provisioner. The Hasher must use the same parameters as the server.
func NewProvisioner(db *gorm.DB, hasher *password.Hasher) *Provisioner {
	return &Provisioner{
		db: db, hasher: hasher,
		tenants: tenant.NewRepository(db), users: user.NewRepository(db),
		groups: group.NewRepository(db), eps: endpoint.NewRepository(db), rbac: rbac.NewRepository(db),
	}
}

// CreateTenant creates an active tenant. The code becomes part of the login URL, so it is validated strictly.
func (p *Provisioner) CreateTenant(ctx context.Context, code, name string) (*tenant.Tenant, error) {
	code, ok := tenantctx.NormalizeCode(code)
	if !ok || len(code) < 2 {
		return nil, errors.New("tenant code must be 2-63 characters: lowercase letters, digits and hyphens, not starting or ending with a hyphen")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("tenant name is required")
	}

	t := &tenant.Tenant{Code: code, Name: name, Status: tenant.StatusActive}
	if err := p.tenants.Create(ctx, t); err != nil {
		return nil, conflictOr(err, "tenant code "+code)
	}
	return t, nil
}

// CreateTenantUser creates an active user inside the tenant with the given code.
func (p *Provisioner) CreateTenantUser(ctx context.Context, tenantCode, username, email, name, plainPassword string) (*user.User, error) {
	u, err := p.newUser(username, email, name, plainPassword)
	if err != nil {
		return nil, err
	}
	code, ok := tenantctx.NormalizeCode(tenantCode)
	if !ok {
		return nil, errors.New("invalid tenant code")
	}
	t, err := p.tenants.FindByCode(ctx, code)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("tenant %q does not exist", code)
	}
	if err != nil {
		return nil, err
	}

	ctx = tenantctx.WithID(ctx, t.ID)
	err = database.InTenantTx(ctx, p.db, auth.LevelTenant, func(ctx context.Context) error {
		return p.users.Create(ctx, u) // fills tenant_id and level from the context
	})
	if err != nil {
		return nil, conflictOr(err, "username or email in this tenant")
	}
	return u, nil
}

// CreatePlatformAdmin creates a platform admin (not bound to any tenant).
func (p *Provisioner) CreatePlatformAdmin(ctx context.Context, username, email, name, plainPassword string) (*user.User, error) {
	u, err := p.newUser(username, email, name, plainPassword)
	if err != nil {
		return nil, err
	}
	err = database.InTenantTx(ctx, p.db, auth.LevelPlatform, func(ctx context.Context) error {
		return p.users.CreatePlatform(ctx, u)
	})
	if err != nil {
		return nil, conflictOr(err, "platform username or email")
	}
	return u, nil
}

func (p *Provisioner) newUser(username, email, name, plainPassword string) (*user.User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 255 {
		return nil, errors.New("username must be 3-255 characters")
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || addr.Name != "" {
		return nil, errors.New("email address is not valid")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := password.Validate(plainPassword); err != nil {
		return nil, err
	}
	hash, err := p.hasher.Hash(plainPassword)
	if err != nil {
		return nil, err
	}
	return &user.User{Username: username, Email: addr.Address, Name: name, PasswordHash: hash, IsActive: true}, nil
}

// conflictOr turns a PostgreSQL unique violation (23505) into ErrAlreadyExists.
func conflictOr(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%s: %w", what, ErrAlreadyExists)
	}
	return err
}

// tenantByCode finds the tenant and returns a context that carries it.
func (p *Provisioner) tenantByCode(ctx context.Context, tenantCode string) (context.Context, error) {
	code, ok := tenantctx.NormalizeCode(tenantCode)
	if !ok {
		return nil, errors.New("invalid tenant code")
	}
	t, err := p.tenants.FindByCode(ctx, code)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("tenant %q does not exist", code)
	}
	if err != nil {
		return nil, err
	}
	return tenantctx.WithID(ctx, t.ID), nil
}

// CreateGroup creates a group (a role) in the tenant. A new group has no permissions: see GrantEndpoints.
func (p *Provisioner) CreateGroup(ctx context.Context, tenantCode, name, description string) (*group.Group, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, errors.New("group name must be 1-100 characters")
	}
	ctx, err := p.tenantByCode(ctx, tenantCode)
	if err != nil {
		return nil, err
	}
	g := &group.Group{Name: name, Description: description}
	err = database.InTenantTx(ctx, p.db, auth.LevelTenant, func(ctx context.Context) error {
		return p.groups.Create(ctx, g)
	})
	if err != nil {
		return nil, conflictOr(err, "group "+name+" in this tenant")
	}
	return g, nil
}

// SetUserGroup puts a user of the tenant into a group of the same tenant.
func (p *Provisioner) SetUserGroup(ctx context.Context, tenantCode, username, groupName string) error {
	ctx, err := p.tenantByCode(ctx, tenantCode)
	if err != nil {
		return err
	}
	return database.InTenantTx(ctx, p.db, auth.LevelTenant, func(ctx context.Context) error {
		g, err := p.groups.FindByName(ctx, groupName)
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("group %q does not exist in this tenant", groupName)
		}
		if err != nil {
			return err
		}
		u, err := p.users.FindByUsername(ctx, username)
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("user %q does not exist in this tenant", username)
		}
		if err != nil {
			return err
		}
		return p.users.Update(ctx, u.ID, map[string]any{"group_id": g.ID})
	})
}

// GrantEndpoints adds endpoints to a group, keeping the grants it already has. With all, every endpoint that
// can be granted is added (tenant scope, not public); otherwise specs are "METHOD /pattern" strings, for
// example "GET /api/v1/groups/:id". It returns the number of endpoints the group has afterwards.
func (p *Provisioner) GrantEndpoints(ctx context.Context, tenantCode, groupName string, all bool, specs []string) (int, error) {
	if !all && len(specs) == 0 {
		return 0, errors.New("give --all or at least one --endpoint")
	}
	ctx, err := p.tenantByCode(ctx, tenantCode)
	if err != nil {
		return 0, err
	}
	var total int
	err = database.InTenantTx(ctx, p.db, auth.LevelTenant, func(ctx context.Context) error {
		g, err := p.groups.FindByName(ctx, groupName)
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("group %q does not exist in this tenant", groupName)
		}
		if err != nil {
			return err
		}
		list, err := p.eps.All(ctx)
		if err != nil {
			return err
		}
		byKey := make(map[string]string) // "METHOD pattern" -> id, only what can be granted
		for _, e := range list {
			if e.Scope == endpoint.ScopeTenant && !e.IsPublic {
				byKey[e.Key()] = e.ID
			}
		}
		want := make(map[string]bool)
		if all {
			for _, id := range byKey {
				want[id] = true
			}
		}
		for _, spec := range specs {
			id, ok := byKey[strings.Join(strings.Fields(spec), " ")]
			if !ok {
				return fmt.Errorf("endpoint %q is not known or cannot be granted (run `tenantctl endpoint sync`; use the route pattern)", spec)
			}
			want[id] = true
		}
		current, err := p.rbac.GroupEndpointIDs(ctx, g.ID)
		if err != nil {
			return err
		}
		for _, id := range current {
			want[id] = true
		}
		ids := make([]string, 0, len(want))
		for id := range want {
			ids = append(ids, id)
		}
		total = len(ids)
		return p.rbac.ReplaceGroupEndpoints(ctx, g.ID, ids)
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// SyncEndpoints makes the endpoints table match the given routes (see endpoint.Repository.Sync).
func (p *Provisioner) SyncEndpoints(ctx context.Context, routes []endpoint.Route, prune bool) (endpoint.SyncResult, error) {
	return p.eps.Sync(ctx, routes, prune)
}
