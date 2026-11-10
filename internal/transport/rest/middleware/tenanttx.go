package middleware

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// errRollback makes the transaction roll back without being reported as a failure.
var errRollback = errors.New("middleware: rollback failed request")

// TenantTx runs the rest of the handler chain inside ONE database transaction whose session variables are set
// for row level security (see database.InTenantTx). Without it a tenant-scoped query sees no rows at all,
// because RLS fails closed.
//
// Mount it after Authenticate (it needs the verified claims) and before anything that queries tenant data,
// including the authorization check:
//
//	g := api.Group("/groups", middleware.Authenticate(mgr), middleware.TenantTx(db))
//
// The transaction commits when the request ends with a status below 400 and no error was attached to the
// context; otherwise it rolls back. Because the response is written before the commit, a failure of the
// commit itself (rare: lost connection, deferred constraint) can no longer change the response; it is
// attached to the context and logged.
func TenantTx(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := ClaimsFrom(c)
		if !ok {
			unauthorized(c) // mounted without Authenticate: refuse instead of running without a tenant
			return
		}
		err := database.InTenantTx(c.Request.Context(), db, claims.Level, func(ctx context.Context) error {
			c.Request = c.Request.WithContext(ctx)
			c.Next()
			if c.Writer.Status() >= 400 || len(c.Errors) > 0 {
				return errRollback
			}
			return nil
		})
		if err == nil || errors.Is(err, errRollback) {
			return
		}
		if c.Writer.Written() {
			_ = c.Error(err) // too late to change the response; the access log records it
			return
		}
		response.Fail(c, err)
	}
}
