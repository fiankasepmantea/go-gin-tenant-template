// Package request reads and validates what a client sent: JSON bodies, path parameters and list queries.
// On bad input the helpers write the error response themselves and return false (or an error), so a handler
// only has to stop.
package request

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// DefaultMaxBody is the body size limit used when a handler does not choose one (1 MiB).
const DefaultMaxBody int64 = 1 << 20

// BindJSON reads the JSON body into dst, limited to max bytes (DefaultMaxBody if max <= 0), and checks the
// `binding:"..."` tags. On failure it writes the error response (400, 413 or 422) and returns false.
func BindJSON(c *gin.Context, dst any, max int64) bool {
	if max <= 0 {
		max = DefaultMaxBody
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
	if err := c.ShouldBindJSON(dst); err != nil {
		response.Fail(c, err)
		return false
	}
	return true
}

// UUIDParam returns the path parameter name when it is a UUID. Anything else cannot be an ID of this
// project, so the answer is 404 (not 400): the client learns nothing about the ID format.
func UUIDParam(c *gin.Context, name string) (string, bool) {
	v := c.Param(name)
	if _, err := uuid.Parse(v); err != nil {
		response.Abort(c, response.CodeNotFound, "")
		return "", false
	}
	return v, true
}
