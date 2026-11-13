// Package rest holds the HTTP router (Gin).
package rest

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/observability"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/version"
)

// Module is a business module that registers its own routes under /api/v1.
type Module interface {
	RegisterRoutes(rg *gin.RouterGroup)
}

// NewRouter creates a router with the base middleware, the health endpoints and the routes of the given modules.
func NewRouter(cfg *config.Config, log *slog.Logger, db *gorm.DB, modules ...Module) *gin.Engine {
	return NewRouterWith(Options{Config: cfg, Log: log, DB: db}, modules...)
}

// Options are the parts of the router that are optional.
type Options struct {
	Config  *config.Config
	Log     *slog.Logger
	DB      *gorm.DB
	Router  *database.Router        // dedicated tenant databases: shown by /readyz and in the metrics
	Metrics *observability.Registry // nil: no metrics
	Spec    gin.HandlerFunc         // GET /openapi.json and /docs when DOCS_ENABLED (docs/openapi.md); nil: none
}

// NewRouterWith is NewRouter with options.
func NewRouterWith(o Options, modules ...Module) *gin.Engine {
	cfg, log, db := o.Config, o.Log, o.DB
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.HandleMethodNotAllowed = true
	if o.Metrics != nil {
		r.Use(observability.Middleware(o.Metrics)) // first: it also counts what the other middleware refuses
	}
	r.Use(middleware.RequestID(), middleware.AccessLog(log), gin.CustomRecovery(func(c *gin.Context, _ any) {
		// gin already logged the panic and the stack; the client only gets the generic error.
		response.Abort(c, response.CodeInternal, "")
	}))

	// Unknown routes and wrong methods answer with the same JSON envelope as every other error.
	r.NoRoute(func(c *gin.Context) { response.Abort(c, response.CodeNotFound, "") })
	r.NoMethod(func(c *gin.Context) { response.Abort(c, response.CodeMethodNotAllowed, "") })

	// Liveness: the process is alive. Used by Docker/Kubernetes to decide on restarts.
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Readiness: ready to receive traffic. Only the shared database decides; the dedicated databases of tenants are
	// listed so an operator sees which one is down, but one of them being down must not take every pod out of
	// rotation (the other tenants are fine).
	r.GET("/readyz", func(c *gin.Context) {
		if err := pingDB(c.Request.Context(), db); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "version": version.Version})
			return
		}
		body := gin.H{"status": "ready", "version": version.Version}
		if o.Router != nil {
			if t := tenantDatabases(c.Request.Context(), o.Router); len(t) > 0 {
				body["tenant_databases"] = t
			}
		}
		c.JSON(http.StatusOK, body)
	})

	if o.Metrics != nil && cfg.Metrics.Enabled {
		r.GET("/metrics", observability.Handler(o.Metrics, cfg.Metrics.Token))
	}
	if o.Spec != nil && cfg.Docs.Enabled {
		r.GET("/openapi.json", o.Spec)
		r.GET("/docs", openapi.UIHandler())
	}

	api := r.Group("/api/v1")
	for _, m := range modules {
		m.RegisterRoutes(api)
	}

	return r
}

func pingDB(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return context.Canceled
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx)
}

// tenantDatabases pings every configured dedicated database, in parallel, and answers "ok" or "unavailable" for
// each reference. The error text is not shown: it can contain host names.
func tenantDatabases(ctx context.Context, r *database.Router) map[string]string {
	refs := r.Refs()
	out := make(map[string]string, len(refs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ref := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state := "ok"
			if db, err := r.Dedicated(ref); err != nil || pingDB(ctx, db) != nil {
				state = "unavailable"
			}
			mu.Lock()
			out[ref] = state
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
