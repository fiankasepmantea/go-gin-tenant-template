// Package middleware holds the Gin middleware shared across modules.
package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
)

// HeaderRequestID is the header used to correlate requests across services.
const HeaderRequestID = "X-Request-ID"

const ctxRequestID = "request_id"

// RequestID uses the client's X-Request-ID (if reasonable) or generates a new UUID v7,
// stores it in the Gin context, and returns it in the response header.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if id == "" || len(id) > 128 {
			id = idgen.New()
		}
		c.Set(ctxRequestID, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// RequestIDFrom retrieves the request id from the Gin context.
func RequestIDFrom(c *gin.Context) string {
	return c.GetString(ctxRequestID)
}
