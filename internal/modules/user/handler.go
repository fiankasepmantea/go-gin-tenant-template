package user

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

type createInput struct {
	Username string  `json:"username" binding:"required,min=3,max=64"`
	Email    string  `json:"email" binding:"required,email,max=254"`
	Name     string  `json:"name" binding:"required,max=100"`
	Password string  `json:"password" binding:"required"`
	GroupID  *string `json:"group_id" binding:"omitempty,uuid"`
	IsActive *bool   `json:"is_active"` // default true
}

type updateInput struct { // pointers: nil means "not sent"
	Email    *string `json:"email" binding:"omitempty,email,max=254"`
	Name     *string `json:"name" binding:"omitempty,min=1,max=100"`
	GroupID  *string `json:"group_id" binding:"omitempty,uuid|eq="` // "" removes the user from the group
	IsActive *bool   `json:"is_active"`
}

type passwordInput struct {
	Password string `json:"password" binding:"required"`
}

// Handler serves /users: the tenant administrator manages the users of the own tenant. It reads the request and
// writes the response; the rules are in the Service. Passwords never leave the server (User.PasswordHash has no
// JSON tag).
type Handler struct {
	svc  *Service
	mw   []gin.HandlerFunc
	base string
}

// NewHandler creates the handler. mw runs before every endpoint (Authenticate, TenantTx, Authorize).
func NewHandler(svc *Service, mw ...gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, mw: mw}
}

var userPolicy = request.ListPolicy{
	Filterable: []string{"username", "email", "name", "is_active", "group_id"},
	Sortable:   []string{"username", "email", "name", "created_at"},
}

// RegisterRoutes registers /users under rg (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	h.base = rg.BasePath() + "/users"
	g := rg.Group("/users", h.mw...)
	g.GET("", h.list)
	g.GET("/:id", h.get)
	g.POST("", h.create)
	g.PATCH("/:id", h.update)
	g.DELETE("/:id", h.remove)
	g.POST("/:id/password", h.setPassword)
}

// Document adds the endpoints to the OpenAPI document.
func (h *Handler) Document(d *openapi.Doc) {
	if h.base == "" {
		return
	}
	const tag = "users"
	var u User
	q := []openapi.Param{
		{Name: "page", Description: "page number, from 1"}, {Name: "per_page", Description: "items per page (at most 100)"},
		{Name: "sort", Description: "columns, a leading - means descending. Allowed: " + strings.Join(userPolicy.Sortable, ", ")},
		{Name: "from", Description: "created_at from"}, {Name: "to", Description: "created_at until, inclusive"},
	}
	for _, c := range userPolicy.Filterable {
		q = append(q, openapi.Param{Name: c, Description: "equality filter"})
	}
	d.Add(openapi.Op{Method: "GET", Path: h.base, Summary: "List users of the tenant", Tag: tag, Result: u, Kind: openapi.List, Query: q})
	d.Add(openapi.Op{Method: "GET", Path: h.base + "/:id", Summary: "Get a user", Tag: tag, Result: u})
	d.Add(openapi.Op{Method: "POST", Path: h.base, Summary: "Create a user in the tenant", Tag: tag, Body: createInput{}, Result: u, Status: 201})
	d.Add(openapi.Op{Method: "PATCH", Path: h.base + "/:id", Summary: "Change a user (send only what changes; group_id \"\" removes the group). Deactivating ends the sessions", Tag: tag, Body: updateInput{}, Result: u})
	d.Add(openapi.Op{Method: "DELETE", Path: h.base + "/:id", Summary: "Delete a user and end the sessions", Tag: tag, Kind: openapi.Message})
	d.Add(openapi.Op{Method: "POST", Path: h.base + "/:id/password", Summary: "Set a new password for a user and end the sessions", Tag: tag, Body: passwordInput{}, Kind: openapi.Message})
}

func (h *Handler) list(c *gin.Context) {
	params, err := request.ParseList(c, userPolicy)
	if err != nil {
		response.Fail(c, err)
		return
	}
	page, err := h.svc.List(c.Request.Context(), params)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.Paginated(c, page.Items, page.Total, page.Page, page.PerPage)
}

func (h *Handler) get(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	u, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, u)
}

func (h *Handler) create(c *gin.Context) {
	var in createInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	u, err := h.svc.Create(c.Request.Context(), CreateInput(in))
	if err != nil {
		h.fail(c, err)
		return
	}
	response.Created(c, u)
}

func (h *Handler) update(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	var in updateInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	u, err := h.svc.Update(c.Request.Context(), h.actor(c), id, Changes(in))
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, u)
}

func (h *Handler) remove(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), h.actor(c), id); err != nil {
		h.fail(c, err)
		return
	}
	response.Message(c, "Deleted.")
}

func (h *Handler) setPassword(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	var in passwordInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	if err := h.svc.SetPassword(c.Request.Context(), id, in.Password); err != nil {
		h.fail(c, err)
		return
	}
	response.Message(c, "Password changed.")
}

// actor is the id of the user who makes the request.
func (h *Handler) actor(c *gin.Context) string {
	if claims, ok := middleware.ClaimsFrom(c); ok {
		return claims.UserID
	}
	return ""
}

// fail turns the errors of the service into responses; everything else goes through response.Fail.
func (h *Handler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrOwnAccount):
		response.Abort(c, response.CodeForbidden, "You cannot deactivate or delete your own account.")
	case errors.Is(err, ErrWeakPassword):
		rule, msg := "min", "The password is too short."
		if errors.Is(err, password.ErrTooLong) {
			rule, msg = "max", "The password is too long."
		}
		response.Fail(c, &response.Error{
			Code: response.CodeValidationFailed, Message: msg,
			Details: []response.FieldError{{Field: "password", Rule: rule}},
		})
	default:
		response.Fail(c, err)
	}
}
