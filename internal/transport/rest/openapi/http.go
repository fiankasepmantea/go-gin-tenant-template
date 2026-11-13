package openapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// swaggerUIVersion is the pinned version of swagger-ui-dist that the docs page loads from unpkg.
const swaggerUIVersion = "5.17.14"

// SpecHandler serves the document at GET /openapi.json. The document is built once, at start-up.
func SpecHandler(d *Doc) (gin.HandlerFunc, error) {
	body, err := d.Marshal()
	if err != nil {
		return nil, err
	}
	return func(c *gin.Context) { c.Data(http.StatusOK, "application/json; charset=utf-8", body) }, nil
}

// UIHandler serves GET /docs: a page that shows /openapi.json with Swagger UI. The page loads the UI from unpkg,
// so only the person reading it needs internet access, not the server; the JSON stays usable by any tool.
func UIHandler() gin.HandlerFunc {
	page := []byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>API docs</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css">
</head><body><div id="ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js"></script>
<script>window.ui = SwaggerUIBundle({url: "/openapi.json", dom_id: "#ui", persistAuthorization: true});</script>
</body></html>`)
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/html; charset=utf-8", page)
	}
}
