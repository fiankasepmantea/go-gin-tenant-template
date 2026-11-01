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
}

// NewProvisioner creates a Provisioner. The Hasher must use the same parameters as the server.
func NewProvisioner(db *gorm.DB, hasher *password.Hasher) *Provisioner {
	return &Provisioner{db: db, hasher: hasher, tenants: tenant.NewRepository(db), users: user.NewRepository(db)}
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
