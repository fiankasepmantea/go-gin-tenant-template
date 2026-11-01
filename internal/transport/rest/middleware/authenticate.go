package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

const ctxClaims = "auth_claims"

// TokenParser verifies an access token. *auth.Manager implements it.
type TokenParser interface {
	Parse(token string) (*auth.Claims, error)
}

// Authenticate requires "Authorization: Bearer <access token>". On success the claims are stored in the Gin
// context (read them with ClaimsFrom) and, for tenant-level tokens, the tenant_id is put in the request context.
//
// The tenant always comes from the verified token, never from a header or the subdomain.
// An access token stays valid until it expires, even after logout; keep ACCESS_TOKEN_TTL short.
func Authenticate(p TokenParser) gin.HandlerFunc {
	return func(c *gin.Context) {
		scheme, token, ok := strings.Cut(c.GetHeader("Authorization"), " ")
		token = strings.TrimSpace(token)
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			unauthorized(c)
			return
		}
		claims, err := p.Parse(token)
		if err != nil {
			unauthorized(c)
			return
		}

		c.Set(ctxClaims, claims)
		if claims.Level == auth.LevelTenant {
			c.Request = c.Request.WithContext(tenant.WithID(c.Request.Context(), claims.TenantID))
		}
		c.Next()
	}
}

// ClaimsFrom returns the claims set by Authenticate.
func ClaimsFrom(c *gin.Context) (*auth.Claims, bool) {
	v, ok := c.Get(ctxClaims)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*auth.Claims)
	return claims, ok
}

func unauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", `Bearer realm="api"`)
	response.Error(c, http.StatusUnauthorized, "unauthorized", "Authentication is required.")
}
