// Package crud is the generic REST handler ("BaseHandler") for a resource that sits on core.Repository.
//
// A module declares what is specific to its resource (the input types, which columns may be filtered and
// sorted) and gets the standard endpoints, consistent responses and the same safety rules everywhere:
//
//	GET    /things                        list: pagination, filters, sort, created_at range
//	GET    /things/:id                    one record
//	GET    /things/show_by/:column/:value first record where column = value
//	GET    /things/all_by/:column/:value  list where column = value (same query options as the list)
//	POST   /things                        create
//	PATCH  /things/:id                    update the given fields
//	DELETE /things/:id                    soft delete
//
// Safety rules enforced here: input goes through explicit input types (never straight into the model, so a
// client cannot set id, tenant_id or timestamps); a client can only filter and sort on the columns listed in
// the Config; the tenant always comes from the request context (set by middleware.Authenticate), because the
// repository applies it. The handler does not authenticate or authorize: mount it on a group that already has
// the middleware.
package crud

import (
	"context"
	"slices"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// Store is what the handler needs from a repository. *core.Repository[T, P] satisfies it.
type Store[T any, P core.Entity[T]] interface {
	Create(ctx context.Context, e P) error
	GetByID(ctx context.Context, id string) (P, error)
	List(ctx context.Context, p core.ListParams) (core.Page[T], error)
	Update(ctx context.Context, id string, fields map[string]any) error
	Delete(ctx context.Context, id string) error
}

// Config describes one resource. C is the create input type and U the update input type; both are decoded
// from the JSON body and validated with their `binding:"..."` tags.
type Config[T any, P core.Entity[T], C, U any] struct {
	Store Store[T, P]

	// Filterable and Sortable are database column names. Only these can be used by a client.
	Filterable []string
	Sortable   []string

	// Build creates the entity from the create input. Nil means the resource has no POST endpoint.
	// Build must not set the ID or tenant_id: the repository does that.
	Build func(in C) P

	// Changes returns the columns to update from the update input (database column names to values).
	// Nil means the resource has no PATCH endpoint. Return only what the client sent.
	Changes func(in U) map[string]any

	// MaxBody is the body size limit in bytes (default request.DefaultMaxBody).
	MaxBody int64
}

// Handler serves one resource.
type Handler[T any, P core.Entity[T], C, U any] struct {
	cfg    Config[T, P, C, U]
	policy request.ListPolicy
}

// New creates the handler. It panics when the Config is unusable (nil Store), because that is a programming
// error found at startup, not at request time.
func New[T any, P core.Entity[T], C, U any](cfg Config[T, P, C, U]) *Handler[T, P, C, U] {
	if cfg.Store == nil {
		panic("crud: Config.Store is required")
	}
	return &Handler[T, P, C, U]{
		cfg:    cfg,
		policy: request.ListPolicy{Filterable: cfg.Filterable, Sortable: cfg.Sortable},
	}
}

// Register adds the endpoints to rg, which should already be the group of the resource
// (for example api.Group("/groups", authenticate)). Middleware passed here runs before every endpoint.
func (h *Handler[T, P, C, U]) Register(rg *gin.RouterGroup, mw ...gin.HandlerFunc) {
	g := rg.Group("", mw...)
	g.GET("", h.list)
	g.GET("/:id", h.get)
	g.GET("/show_by/:column/:value", h.showBy)
	g.GET("/all_by/:column/:value", h.allBy)
	if h.cfg.Build != nil {
		g.POST("", h.create)
	}
	if h.cfg.Changes != nil {
		g.PATCH("/:id", h.update)
	}
	g.DELETE("/:id", h.remove)
}

func (h *Handler[T, P, C, U]) list(c *gin.Context) {
	params, err := request.ParseList(c, h.policy)
	if err != nil {
		response.Fail(c, err)
		return
	}
	h.respondList(c, params)
}

func (h *Handler[T, P, C, U]) respondList(c *gin.Context, params core.ListParams) {
	page, err := h.cfg.Store.List(c.Request.Context(), params)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Paginated(c, page.Items, page.Total, page.Page, page.PerPage)
}

func (h *Handler[T, P, C, U]) get(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	e, err := h.cfg.Store.GetByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, e)
}

// byColumn parses /show_by and /all_by: the column must be on the whitelist.
func (h *Handler[T, P, C, U]) byColumn(c *gin.Context) (core.ListParams, bool) {
	col := c.Param("column")
	if !slices.Contains(h.cfg.Filterable, col) {
		response.Abort(c, response.CodeInvalidRequest, "Cannot filter by this column.")
		return core.ListParams{}, false
	}
	params, err := request.ParseList(c, h.policy)
	if err != nil {
		response.Fail(c, err)
		return params, false
	}
	params.Filters = append(params.Filters, core.Filter{Column: col, Value: c.Param("value")})
	return params, true
}

func (h *Handler[T, P, C, U]) showBy(c *gin.Context) {
	params, ok := h.byColumn(c)
	if !ok {
		return
	}
	params.Page, params.PerPage = 1, 1
	page, err := h.cfg.Store.List(c.Request.Context(), params)
	if err != nil {
		response.Fail(c, err)
		return
	}
	if len(page.Items) == 0 {
		response.Abort(c, response.CodeNotFound, "")
		return
	}
	response.OK(c, page.Items[0])
}

func (h *Handler[T, P, C, U]) allBy(c *gin.Context) {
	if params, ok := h.byColumn(c); ok {
		h.respondList(c, params)
	}
}

func (h *Handler[T, P, C, U]) create(c *gin.Context) {
	var in C
	if !request.BindJSON(c, &in, h.cfg.MaxBody) {
		return
	}
	e := h.cfg.Build(in)
	if err := h.cfg.Store.Create(c.Request.Context(), e); err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, e)
}

func (h *Handler[T, P, C, U]) update(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	var in U
	if !request.BindJSON(c, &in, h.cfg.MaxBody) {
		return
	}
	ctx := c.Request.Context()
	if err := h.cfg.Store.Update(ctx, id, h.cfg.Changes(in)); err != nil {
		response.Fail(c, err)
		return
	}
	e, err := h.cfg.Store.GetByID(ctx, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, e)
}

func (h *Handler[T, P, C, U]) remove(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.cfg.Store.Delete(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.Message(c, "Deleted.")
}
