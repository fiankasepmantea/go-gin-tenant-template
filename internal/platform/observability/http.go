package observability

import (
	"bytes"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Middleware counts every request and measures its latency. Mount it first, so it also sees what the other
// middleware refuses.
func Middleware(r *Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		r.InFlight(1)
		defer r.InFlight(-1)
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		r.Observe(c.Request.Method, route, c.Writer.Status(), time.Since(start))
	}
}

// Handler serves the metrics. With a non-empty token the request needs `Authorization: Bearer <token>`
// (compared in constant time); the endpoint answers 404 otherwise, so its existence is not advertised.
func Handler(r *Registry, token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token != "" {
			got, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
		}
		var buf bytes.Buffer
		r.Write(&buf)
		c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", buf.Bytes())
	}
}
