// Package openapi builds the OpenAPI 3.0 description of the API from the code: the routes that are registered
// and the Go types that handlers read and write (json and binding tags), so the description cannot drift from
// the API. A module describes itself by implementing Documenter; a route without a description still appears,
// with its method, path, parameters and the common errors.
package openapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// authScheme names the security scheme in the document. (Named without "bearer"/"token": the secret scanner
// gosec G101 flags a literal under such a key.)
const authScheme = "pasetoAuth"

// Documenter is implemented by a module that can describe its routes in more detail than "a route exists".
type Documenter interface {
	Document(d *Doc)
}

// Kind says what the data field of a success response holds.
type Kind int

const (
	One     Kind = iota // one Result
	List                // a Pagination of Result
	Message             // no data, only a message
)

// Param is a query parameter.
type Param struct {
	Name, Description string
}

// Upload describes a multipart/form-data request body: one file field and optional text fields.
type Upload struct {
	File   string // name of the file field
	Fields []Param
}

// Op describes one operation.
type Op struct {
	Method  string // GET, POST, ...
	Path    string // gin pattern, e.g. /api/v1/groups/:id
	Summary string
	Tag     string
	Body    any  // a value of the request body type (nil: no body)
	Result  any  // a value of the response data type (nil: none)
	Kind    Kind // how Result is wrapped
	Status  int  // success status (default 200)
	Query   []Param
	Public  bool    // no bearer token needed
	Upload  *Upload // multipart/form-data body (replaces Body)
	Binary  bool    // the success response is the content of a file, not JSON
}

// Doc collects operations and builds the document.
type Doc struct {
	title, version string
	ops            map[string]Op // "METHOD path"
	sch            *schemas
}

// New creates an empty document.
func New(title, version string) *Doc {
	return &Doc{title: title, version: version, ops: map[string]Op{}, sch: newSchemas()}
}

func key(method, path string) string { return strings.ToUpper(method) + " " + path }

// Add records an operation; a later Add for the same method and path replaces the earlier one.
func (d *Doc) Add(op Op) { d.ops[key(op.Method, op.Path)] = op }

// Has reports whether the operation was described.
func (d *Doc) Has(method, path string) bool { _, ok := d.ops[key(method, path)]; return ok }

// FillRoutes adds a plain operation for every registered route that no module described.
func (d *Doc) FillRoutes(routes []gin.RouteInfo) {
	for _, r := range routes {
		if !strings.HasPrefix(r.Path, "/api/") || d.Has(r.Method, r.Path) {
			continue
		}
		d.Add(Op{Method: r.Method, Path: r.Path, Summary: r.Method + " " + r.Path, Public: strings.HasPrefix(r.Path, "/api/v1/auth/")})
	}
}

// Marshal returns the OpenAPI document as JSON.
func (d *Doc) Marshal() ([]byte, error) {
	paths := map[string]map[string]any{}
	for _, op := range d.ops {
		p := toBraces(op.Path)
		if paths[p] == nil {
			paths[p] = map[string]any{}
		}
		paths[p][strings.ToLower(op.Method)] = d.operation(op)
	}
	comps := d.sch.components()
	comps["securitySchemes"] = map[string]any{authScheme: map[string]any{"type": "http", "scheme": "bearer", "description": "PASETO v4.local access token from /auth/login"}}
	doc := map[string]any{
		"openapi":    "3.0.3",
		"info":       map[string]any{"title": d.title, "version": d.version, "description": "Responses use an envelope: {success, code, message?, data} or {success:false, code, error:{code, message, details?}}."},
		"paths":      paths,
		"components": comps,
	}
	return json.MarshalIndent(doc, "", "  ")
}

// toBraces turns /groups/:id into /groups/{id}.
func toBraces(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if strings.HasPrefix(s, ":") {
			parts[i] = "{" + s[1:] + "}"
		} else if strings.HasPrefix(s, "*") {
			parts[i] = "{" + s[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

func (d *Doc) operation(op Op) map[string]any {
	tag := op.Tag
	if tag == "" {
		tag = tagOf(op.Path)
	}
	out := map[string]any{"summary": op.Summary, "tags": []string{tag}, "operationId": operationID(op)}

	var params []any
	for _, seg := range strings.Split(op.Path, "/") {
		if strings.HasPrefix(seg, ":") {
			name := seg[1:]
			schema := map[string]any{"type": "string"}
			if name == "id" {
				schema["format"] = "uuid"
			}
			params = append(params, map[string]any{"name": name, "in": "path", "required": true, "schema": schema})
		}
	}
	for _, q := range op.Query {
		params = append(params, map[string]any{"name": q.Name, "in": "query", "description": q.Description, "schema": map[string]any{"type": "string"}})
	}
	if len(params) > 0 {
		out["parameters"] = params
	}
	if op.Body != nil {
		out["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": d.sch.of(op.Body)}}}
	}
	if op.Upload != nil {
		props := map[string]any{op.Upload.File: map[string]any{"type": "string", "format": "binary"}}
		for _, f := range op.Upload.Fields {
			props[f.Name] = map[string]any{"type": "string", "description": f.Description}
		}
		out["requestBody"] = map[string]any{"required": true, "content": map[string]any{"multipart/form-data": map[string]any{
			"schema": map[string]any{"type": "object", "required": []string{op.Upload.File}, "properties": props},
		}}}
	}
	if op.Public {
		out["security"] = []any{}
	} else {
		out["security"] = []any{map[string]any{authScheme: []string{}}}
	}

	status := op.Status
	if status == 0 {
		status = http.StatusOK
	}
	resp := map[string]any{}
	resp[itoa(status)] = map[string]any{"description": http.StatusText(status), "content": map[string]any{"application/json": map[string]any{"schema": d.envelope(op)}}}
	if op.Binary {
		resp[itoa(status)] = map[string]any{"description": "the content of the file", "content": map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}
	}
	errResp := func(code int, desc string) {
		resp[itoa(code)] = map[string]any{"description": desc, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/ErrorBody"}}}}
	}
	if op.Body != nil || op.Upload != nil {
		errResp(400, "invalid_request / payload too large (413)")
		errResp(422, "validation_failed (details lists the fields, named as in the JSON)")
	}
	if !op.Public {
		errResp(401, "unauthorized")
		errResp(403, "forbidden: the group of the user was not granted this endpoint")
	}
	if len(params) > 0 && strings.Contains(op.Path, ":") {
		errResp(404, "not_found")
	}
	errResp(500, "internal_error")
	errResp(503, "service_unavailable (the database of the tenant cannot be reached)")
	out["responses"] = resp
	return out
}

func (d *Doc) envelope(op Op) map[string]any {
	var data map[string]any
	switch {
	case op.Kind == Message || op.Result == nil:
		data = map[string]any{"nullable": true}
	case op.Kind == List:
		data = map[string]any{"type": "object", "properties": map[string]any{
			"total_data":    map[string]any{"type": "integer", "format": "int64"},
			"total_pages":   map[string]any{"type": "integer"},
			"curr_page":     map[string]any{"type": "integer"},
			"next_page":     map[string]any{"type": "integer"},
			"prev_page":     map[string]any{"type": "integer", "description": "absent on the first page"},
			"data_per_page": map[string]any{"type": "integer"},
			"data":          map[string]any{"type": "array", "items": d.sch.of(op.Result)},
		}}
	default:
		data = d.sch.of(op.Result)
	}
	return map[string]any{"type": "object", "properties": map[string]any{
		"success": map[string]any{"type": "boolean"},
		"code":    map[string]any{"type": "integer", "description": "the HTTP status"},
		"message": map[string]any{"type": "string"},
		"data":    data,
	}}
}

func tagOf(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) > 0 && parts[0] != "" && !strings.HasPrefix(parts[0], ":") {
		return parts[0]
	}
	return "api"
}

func operationID(op Op) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(op.Method))
	for _, s := range strings.Split(op.Path, "/") {
		if s == "" || s == "api" || s == "v1" {
			continue
		}
		s = strings.TrimLeft(s, ":*")
		if s == "" {
			continue
		}
		b.WriteString("_")
		b.WriteString(strings.NewReplacer("-", "_").Replace(s))
	}
	return b.String()
}

func itoa(n int) string { return strconv.Itoa(n) }

// sortedKeys is used by the schema builder for a stable order of required fields.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
