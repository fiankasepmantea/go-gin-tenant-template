package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/ttlcache"
)

// ErrUnavailable marks an error as "a dependency of this request cannot be reached": REST answers 503 and gRPC
// answers Unavailable.
var ErrUnavailable = errors.New("service unavailable")

// ErrTenantDatabaseUnavailable is returned when the dedicated database of a tenant cannot be used: it is not
// configured, cannot be opened, or the connection failed. Only that tenant is affected.
var ErrTenantDatabaseUnavailable = fmt.Errorf("%w: the database of this tenant", ErrUnavailable)

// RouterConfig configures NewRouter.
type RouterConfig struct {
	Control    *gorm.DB          // the shared database: tenants, platform admins, endpoints, and every tenant without db_ref
	URLs       map[string]string // reference -> connection URL of a dedicated database
	Pool       Pool              // pool size of each dedicated database
	Production bool
	LocateTTL  time.Duration // how long the db_ref of a tenant is remembered (default 30s)
	RetryAfter time.Duration // how long a failed open is not retried (default 5s)
}

// Router chooses the database of a tenant. A tenant is in the shared (control) database unless its row in
// `tenants` has a db_ref; then all its data lives in the dedicated database with that reference.
//
//	control database      tenants, platform admins (users without tenant), endpoints, tenants without db_ref
//	dedicated database    everything a tenant owns: users, groups, grants, sessions, and its business tables
//
// The row level security policies stay in force in both kinds, so the same code runs against either.
// Safe for concurrent use.
type Router struct {
	control    *gorm.DB
	urls       map[string]string
	pool       Pool
	production bool
	retryAfter time.Duration
	open       func(url string) (*gorm.DB, error) // replaced by tests

	refs *ttlcache.Cache[string, string] // tenant_id -> db_ref ("" = shared)

	mu      sync.Mutex
	pools   map[string]*gorm.DB
	failed  map[string]failure
	closing bool
}

type failure struct {
	at  time.Time
	err error
}

// NewRouter creates the router. With no URLs every tenant is served by the control database.
func NewRouter(c RouterConfig) *Router {
	if c.LocateTTL <= 0 {
		c.LocateTTL = 30 * time.Second
	}
	if c.RetryAfter <= 0 {
		c.RetryAfter = 5 * time.Second
	}
	if c.Pool.MaxOpen <= 0 {
		c.Pool.MaxOpen = 10
	}
	r := &Router{
		control: c.Control, urls: c.URLs, pool: c.Pool, production: c.Production, retryAfter: c.RetryAfter,
		refs:   ttlcache.New[string, string](c.LocateTTL, 10_000),
		pools:  make(map[string]*gorm.DB),
		failed: make(map[string]failure),
	}
	r.open = func(url string) (*gorm.DB, error) { return ConnectURL(url, r.pool, r.production) }
	return r
}

// Control returns the shared database.
func (r *Router) Control() *gorm.DB { return r.control }

// Refs lists the configured references of dedicated databases, sorted.
func (r *Router) Refs() []string {
	out := make([]string, 0, len(r.urls))
	for ref := range r.urls {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// Configured reports whether a dedicated database with this reference has a URL.
func (r *Router) Configured(ref string) bool { _, ok := r.urls[ref]; return ok }

// Dedicated returns the pool of a dedicated database, opening it on first use. A failed open is remembered
// for a few seconds, so a database that is down does not make every request wait for a connection timeout.
func (r *Router) Dedicated(ref string) (*gorm.DB, error) {
	url, ok := r.urls[ref]
	if !ok {
		return nil, fmt.Errorf("%w: %q has no TENANT_DB_URL_%s", ErrTenantDatabaseUnavailable, ref, strings.ToUpper(ref))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil, fmt.Errorf("%w: shutting down", ErrTenantDatabaseUnavailable)
	}
	if db, ok := r.pools[ref]; ok {
		return db, nil
	}
	if f, ok := r.failed[ref]; ok && time.Since(f.at) < r.retryAfter {
		return nil, f.err
	}
	db, err := r.open(url)
	if err != nil {
		err = fmt.Errorf("%w: %q: %w", ErrTenantDatabaseUnavailable, ref, err)
		r.failed[ref] = failure{at: time.Now(), err: err}
		return nil, err
	}
	delete(r.failed, ref)
	r.pools[ref] = db
	return db, nil
}

// refOf returns the db_ref of a tenant ("" = shared). A tenant that is not in the table counts as shared:
// the queries that follow find nothing, exactly as before this feature existed.
func (r *Router) refOf(ctx context.Context, tenantID string) (string, error) {
	if ref, ok := r.refs.Get(tenantID); ok {
		return ref, nil
	}
	var refs []struct{ DBRef *string }
	err := r.control.WithContext(ctx).Raw("SELECT db_ref FROM tenants WHERE tenant_id = ? AND deleted_at IS NULL", tenantID).Scan(&refs).Error
	if err != nil {
		return "", fmt.Errorf("database: locate tenant: %w", err)
	}
	ref := ""
	if len(refs) == 1 && refs[0].DBRef != nil {
		ref = *refs[0].DBRef
	}
	r.refs.Set(tenantID, ref)
	return ref, nil
}

// Forget drops what the router remembers about a tenant (after its db_ref was changed in this process).
func (r *Router) Forget(tenantID string) { r.refs.Delete(tenantID) }

// For returns the database of a tenant.
func (r *Router) For(ctx context.Context, tenantID string) (*gorm.DB, error) {
	if len(r.urls) == 0 {
		return r.control, nil // no dedicated databases configured: no lookup, no overhead
	}
	ref, err := r.refOf(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		return r.control, nil
	}
	return r.Dedicated(ref)
}

// InTenantTx is database.InTenantTx on the database of the tenant in ctx (tenant level) or on the control
// database (platform level). Its signature fits session.TxRunner, middleware.TxFunc and grpc.TxFunc.
func (r *Router) InTenantTx(ctx context.Context, level string, fn func(ctx context.Context) error) error {
	db := r.control
	var ref string
	if level == auth.LevelTenant {
		id, ok := tenant.IDFrom(ctx)
		if !ok {
			return ErrTenantRequired
		}
		if len(r.urls) > 0 {
			var err error
			if ref, err = r.refOf(ctx, id); err != nil {
				return err
			}
			if ref != "" {
				if db, err = r.Dedicated(ref); err != nil {
					return err
				}
			}
		}
	}
	err := InTenantTx(ctx, db, level, fn)
	if err != nil && ref != "" && isConnectionError(err) {
		return fmt.Errorf("%w: %q: %w", ErrTenantDatabaseUnavailable, ref, err)
	}
	return err
}

// Stats returns the pool statistics of a dedicated database; zero values while it was not opened yet.
func (r *Router) Stats(ref string) sql.DBStats {
	r.mu.Lock()
	db := r.pools[ref]
	r.mu.Unlock()
	if db == nil {
		return sql.DBStats{}
	}
	if sqlDB, err := db.DB(); err == nil {
		return sqlDB.Stats()
	}
	return sql.DBStats{}
}

// Close closes every dedicated pool. The control database is closed by whoever opened it.
func (r *Router) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closing = true
	for ref, db := range r.pools {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		delete(r.pools, ref)
	}
}

// isConnectionError reports whether err means "the database cannot be reached" (as opposed to a query that
// failed): refused or reset connections, a server that is shutting down, a failed connect.
func isConnectionError(err error) bool {
	var ce *pgconn.ConnectError
	var ne net.Error
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &ce), errors.As(err, &ne),
		errors.Is(err, driver.ErrBadConn), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.As(err, &pe):
		return strings.HasPrefix(pe.Code, "08") || strings.HasPrefix(pe.Code, "57P0") // connection exception, shutdown
	}
	return false
}
