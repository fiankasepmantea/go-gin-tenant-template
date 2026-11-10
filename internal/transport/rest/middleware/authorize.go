package middleware

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// Authorizer decides whether a caller may use an endpoint. *rbac.Service implements it.
type Authorizer interface {
	Authorize(ctx context.Context, claims *auth.Claims, method, route string) (auth.Decision, error)
}

// Authorize refuses every request the Authorizer does not explicitly allow.
//
// Mount it after Authenticate and TenantTx (the decision reads the database inside the tenant transaction):
//
//	g := api.Group("/groups", middleware.Authenticate(mgr), middleware.TenantTx(db), middleware.Authorize(az, log))
//
// The endpoint is identified by the HTTP method and the matched route PATTERN (c.FullPath(), e.g.
// "/api/v1/groups/:id"), never by the raw URL, so path parameters cannot be used to slip past a rule.
//
// Responses: 403 forbidden when the caller is not allowed; 401 unauthorized when the user behind a valid token
// no longer exists or is inactive; 500 when the decision could not be made (the request is NOT let through).
// The reason of a refusal is written to the log only.
func Authorize(az Authorizer, log *slog.Logger) gin.HandlerFunc {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return func(c *gin.Context) {
		claims, ok := ClaimsFrom(c)
		if !ok {
			unauthorized(c) // mounted without Authenticate: refuse instead of guessing
			return
		}
		route := c.FullPath()
		if route == "" {
			response.Abort(c, response.CodeForbidden, "")
			return
		}

		d, err := az.Authorize(c.Request.Context(), claims, c.Request.Method, route)
		if err != nil {
			response.Fail(c, err) // fail closed
			return
		}
		switch d.Verdict {
		case auth.VerdictAllowed:
			c.Next()
		case auth.VerdictUnauthenticated:
			logDenied(c, log, claims, route, d)
			unauthorized(c)
		default:
			logDenied(c, log, claims, route, d)
			response.Abort(c, response.CodeForbidden, "")
		}
	}
}

func logDenied(c *gin.Context, log *slog.Logger, claims *auth.Claims, route string, d auth.Decision) {
	log.LogAttrs(c.Request.Context(), slog.LevelWarn, "access_denied",
		slog.String("request_id", RequestIDFrom(c)),
		slog.String("user_id", claims.UserID),
		slog.String("tenant_id", claims.TenantID),
		slog.String("level", claims.Level),
		slog.String("method", c.Request.Method),
		slog.String("route", route),
		slog.String("reason", d.Reason),
	)
}
