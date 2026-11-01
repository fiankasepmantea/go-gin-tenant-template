// Package rest holds the HTTP router (Gin).
package rest

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
)

// Module is a business module that registers its own routes under /api/v1.
type Module interface {
	RegisterRoutes(rg *gin.RouterGroup)
}

// NewRouter creates a router with the base middleware, the health endpoints and the routes of the given modules.
func NewRouter(cfg *config.Config, log *slog.Logger, db *gorm.DB, modules ...Module) *gin.Engine {
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(middleware.RequestID(), middleware.AccessLog(log), gin.Recovery())

	// Liveness: the process is alive. Used by Docker/Kubernetes to decide on restarts.
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Readiness: ready to receive traffic (database reachable).
	r.GET("/readyz", func(c *gin.Context) {
		if err := pingDB(c.Request.Context(), db); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

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
