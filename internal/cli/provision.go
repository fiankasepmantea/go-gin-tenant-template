package cli

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/group"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/rbac"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/migrate"
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
	router  *database.Router // nil: every tenant is in db
	migDir  string           // migrations of a dedicated database must be complete before a tenant is placed there
}

// NewProvisioner creates a Provisioner. The Hasher must use the same parameters as the server.
func NewProvisioner(db *gorm.DB, hasher *password.Hasher) *Provisioner {
	return &Provisioner{
		db: db, hasher: hasher,
		tenants: tenant.NewRepository(db), users: user.NewRepository(db),
		groups: group.NewRepository(db), eps: endpoint.NewRepository(db), rbac: rbac.NewRepository(db),
	}
}

// NewProvisionerWithRouter is NewProvisioner for an installation that has dedicated tenant databases: the data
// of a tenant is written to the database of that tenant. migrationsDir is the directory of the up files.
func NewProvisionerWithRouter(r *database.Router, hasher *password.Hasher, migrationsDir string) *Provisioner {
	p := NewProvisioner(r.Control(), hasher)
	p.router, p.migDir = r, migrationsDir
	return p
}

// tx opens the transaction on the database of the tenant in ctx (or the control database for platform level).
func (p *Provisioner) tx(ctx context.Context, level string, fn func(ctx context.Context) error) error {
	if p.router != nil {
		return p.router.InTenantTx(ctx, level, fn)
	}
	return database.InTenantTx(ctx, p.db, level, fn)
}

// CreateTenant creates an active tenant in the shared database. The code becomes part of the login URL, so it is validated strictly.
func (p *Provisioner) CreateTenant(ctx context.Context, code, name string) (*tenant.Tenant, error) {
	return p.CreateTenantIn(ctx, code, name, "")
}

// CreateTenantIn creates a tenant. With a dbRef the tenant gets a dedicated database: the reference must have a
// TENANT_DB_URL_<REF>, and that database must already be migrated to the latest version
// (`tenantctl migrate up --ref <ref>`). Only new tenants can be placed there: there is no tool that moves the
// data of an existing tenant.
func (p *Provisioner) CreateTenantIn(ctx context.Context, code, name, dbRef string) (*tenant.Tenant, error) {
	code, ok := tenantctx.NormalizeCode(code)
	if !ok || len(code) < 2 {
		return nil, errors.New("tenant code must be 2-63 characters: lowercase letters, digits and hyphens, not starting or ending with a hyphen")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("tenant name is required")
	}

	t := &tenant.Tenant{Code: code, Name: name, Status: tenant.StatusActive}
	if dbRef == "" {
		if err := p.tenants.Create(ctx, t); err != nil {
			return nil, conflictOr(err, "tenant code "+code)
		}
		return t, nil
	}
	return p.createDedicated(ctx, t, dbRef)
}

// createDedicated creates the tenant in the control database (with its db_ref) and a copy of the row in its own
// database, where tenant_id of every table points at it. If the second step fails the first is undone.
func (p *Provisioner) createDedicated(ctx context.Context, t *tenant.Tenant, dbRef string) (*tenant.Tenant, error) {
	if p.router == nil {
		return nil, errors.New("this installation has no dedicated databases (set TENANT_DB_URL_<REF>)")
	}
	if !config.ValidTenantDBRef(dbRef) {
		return nil, fmt.Errorf("db ref %q is not valid: %s", dbRef, config.TenantDBRefPattern)
	}
	if !p.router.Configured(dbRef) {
		return nil, fmt.Errorf("db ref %q has no connection URL: set TENANT_DB_URL_%s", dbRef, strings.ToUpper(dbRef))
	}
	ded, err := p.router.Dedicated(dbRef)
	if err != nil {
		return nil, err
	}
	if err := p.requireMigrated(ctx, ded, dbRef); err != nil {
		return nil, err
	}
	t.DBRef = &dbRef
	if err := p.tenants.Create(ctx, t); err != nil {
		return nil, conflictOr(err, "tenant code "+t.Code)
	}
	undo := func(cause error) (*tenant.Tenant, error) {
		if derr := p.db.WithContext(ctx).Unscoped().Delete(&tenant.Tenant{}, "tenant_id = ?", t.ID).Error; derr != nil {
			cause = fmt.Errorf("%w (and the tenant row in the shared database could not be removed: %w; delete tenant %s by hand)", cause, derr, t.Code)
		}
		p.router.Forget(t.ID)
		return nil, cause
	}
	// the same row, with the same id, in the dedicated database (tables there reference tenants)
	if err := ded.WithContext(ctx).Create(t).Error; err != nil {
		return undo(fmt.Errorf("create the tenant in database %q: %w", dbRef, err))
	}
	if _, err := p.MirrorEndpoints(ctx); err != nil {
		return undo(err)
	}
	return t, nil
}

// requireMigrated refuses a dedicated database that is empty or behind the migrations of the code.
func (p *Provisioner) requireMigrated(ctx context.Context, db *gorm.DB, ref string) error {
	all, err := migrate.Load(p.migDir)
	if err != nil {
		return err
	}
	st, err := migrate.Current(ctx, db)
	if err != nil {
		return fmt.Errorf("database %q: %w (the role of TENANT_DB_URL_%s needs SELECT on schema_migrations)", ref, err, strings.ToUpper(ref))
	}
	if want := migrate.Latest(all); st.Version != want || st.Dirty {
		return fmt.Errorf("database %q is at migration %d (dirty=%t), the code needs %d: run `tenantctl migrate up --ref %s`", ref, st.Version, st.Dirty, want, ref)
	}
	return nil
}

// MirrorEndpoints copies the endpoints of the shared database to every dedicated database that a tenant uses.
// It continues after a failure and reports all of them together.
func (p *Provisioner) MirrorEndpoints(ctx context.Context) (int, error) {
	if p.router == nil {
		return 0, nil
	}
	var refs []string
	if err := p.db.WithContext(ctx).Raw("SELECT DISTINCT db_ref FROM tenants WHERE db_ref IS NOT NULL AND deleted_at IS NULL ORDER BY 1").Scan(&refs).Error; err != nil {
		return 0, fmt.Errorf("list dedicated databases: %w", err)
	}
	if len(refs) == 0 {
		return 0, nil
	}
	want, err := p.eps.All(ctx)
	if err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for _, ref := range refs {
		ded, err := p.router.Dedicated(ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		w, err := endpoint.NewRepository(ded).Mirror(ctx, want)
		if err != nil {
			errs = append(errs, fmt.Errorf("database %q: %w", ref, err))
			continue
		}
		n += w
	}
	return n, errors.Join(errs...)
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
	err = p.tx(ctx, auth.LevelTenant, func(ctx context.Context) error {
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
	err = p.tx(ctx, auth.LevelPlatform, func(ctx context.Context) error {
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
	err = p.tx(ctx, auth.LevelTenant, func(ctx context.Context) error {
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
	return p.tx(ctx, auth.LevelTenant, func(ctx context.Context) error {
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
	err = p.tx(ctx, auth.LevelTenant, func(ctx context.Context) error {
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
// Dedicated tenant databases get the same endpoints (same ids), see MirrorEndpoints; a failure there is
// returned after the shared database was updated, and running the command again repeats the copy.
func (p *Provisioner) SyncEndpoints(ctx context.Context, routes []endpoint.Route, prune bool) (endpoint.SyncResult, error) {
	res, err := p.eps.Sync(ctx, routes, prune)
	if err != nil {
		return res, err
	}
	if _, err := p.MirrorEndpoints(ctx); err != nil {
		return res, fmt.Errorf("the shared database is up to date, but not every dedicated database: %w", err)
	}
	return res, nil
}
