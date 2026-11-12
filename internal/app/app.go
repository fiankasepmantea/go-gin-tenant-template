// Package app wires the HTTP modules together. The server uses it with real dependencies; the CLI uses it with
// none, only to learn which routes exist (`tenantctl endpoint sync`). Keeping the wiring in one place means the
// list of routes that is synchronized into the endpoints table is, by construction, the list the server serves.
package app

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/group"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/rbac"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/session"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
)

// Deps are the things the modules need. For listing routes every field may be left empty.
type Deps struct {
	Log      *slog.Logger
	DB       *gorm.DB
	Router   *database.Router // nil: every tenant is in DB
	Tokens   *auth.Manager
	Sessions *session.Service
	Account  *session.Account // nil: no password endpoints
	Resolver tenantctx.Resolver
	RBAC     rbac.Options
}

// Modules creates every module of the API. Routes of tenant data share one middleware chain, in this order:
//
//	Authenticate  verifies the token and puts the tenant of the token in the context
//	TenantTx      opens the database transaction with the row level security variables
//	Authorize     refuses everything the caller's group was not granted (default deny)
//
// To add a module: `tenantctl make:module <name>` does it; by hand, create the handler with `chain...` as the
// middleware and append it below.
func Modules(d Deps) []rest.Module {
	authn := middleware.Authenticate(d.Tokens)
	rbacSvc := rbac.NewService(endpoint.NewRepository(d.DB), rbac.NewRepository(d.DB), d.RBAC)
	chain := []gin.HandlerFunc{authn, tenantTx(d), middleware.Authorize(rbacSvc, d.Log)}

	// /auth/*: public login endpoints; they authenticate themselves and are not decided by RBAC.
	auth := session.NewHandler(d.Sessions, d.Resolver, authn, d.Log)
	if d.Account != nil {
		auth.WithAccount(d.Account)
	}

	return []rest.Module{
		auth,
		group.NewHandler(d.DB, chain...),
		rbac.NewHandler(rbacSvc, group.NewRepository(d.DB), rbac.NewRepository(d.DB), chain...),
		// tenantctl:modules (`tenantctl make:module` adds new modules above this line; keep it)
	}
}

// tenantTx opens the transaction on the database of the tenant: through the router when there is one.
func tenantTx(d Deps) gin.HandlerFunc {
	if d.Router != nil {
		return middleware.TenantTxWith(d.Router.InTenantTx)
	}
	return middleware.TenantTx(d.DB)
}

// Routes lists the method and route pattern of every route the modules register under /api/v1.
// It needs no database and no keys.
func Routes(modules []rest.Module) []endpoint.Route {
	prev := gin.Mode()
	gin.SetMode(gin.ReleaseMode) // no route table printed to stdout
	defer gin.SetMode(prev)

	r := gin.New()
	api := r.Group("/api/v1")
	for _, m := range modules {
		m.RegisterRoutes(api)
	}
	infos := r.Routes()
	out := make([]endpoint.Route, 0, len(infos))
	for _, ri := range infos {
		out = append(out, endpoint.Route{Method: ri.Method, Path: ri.Path})
	}
	return out
}
