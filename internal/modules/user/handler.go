package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// Store is what the handler needs from the user repository. *Repository satisfies it.
type Store interface {
	Create(ctx context.Context, e *User) error
	GetByID(ctx context.Context, id string) (*User, error)
	List(ctx context.Context, p core.ListParams) (core.Page[User], error)
	Update(ctx context.Context, id string, fields map[string]any) error
	Delete(ctx context.Context, id string) error
}

// Sessions ends the sessions of a user. *session.Repository satisfies it.
type Sessions interface {
	RevokeAllForUser(ctx context.Context, userID string, at time.Time) error
}

// Hasher hashes new passwords. *password.Hasher satisfies it.
type Hasher interface {
	Hash(plain string) (string, error)
}

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

// Handler serves /users: the tenant administrator manages the users of the own tenant.
// Passwords never leave the server (User.PasswordHash has no JSON tag), and a user cannot be created or
// changed across tenants: the repository applies the tenant of the token and PostgreSQL enforces it again.
type Handler struct {
	store    Store
	sessions Sessions
	hasher   Hasher
	now      func() time.Time
	mw       []gin.HandlerFunc
	base     string
}

// NewHandler creates the handler. mw runs before every endpoint (Authenticate, TenantTx, Authorize).
func NewHandler(store Store, sessions Sessions, hasher Hasher, mw ...gin.HandlerFunc) *Handler {
	return &Handler{store: store, sessions: sessions, hasher: hasher, now: time.Now, mw: mw}
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
	page, err := h.store.List(c.Request.Context(), params)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Paginated(c, page.Items, page.Total, page.Page, page.PerPage)
}

func (h *Handler) get(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	u, err := h.store.GetByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, u)
}

func (h *Handler) create(c *gin.Context) {
	var in createInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	if !h.checkPassword(c, in.Password, "password") {
		return
	}
	hash, err := h.hasher.Hash(in.Password)
	if err != nil {
		response.Fail(c, fmt.Errorf("users: hash password: %w", err))
		return
	}
	u := &User{
		Username:     strings.TrimSpace(in.Username),
		Email:        strings.TrimSpace(in.Email),
		Name:         strings.TrimSpace(in.Name),
		PasswordHash: hash,
		GroupID:      in.GroupID,
		IsActive:     in.IsActive == nil || *in.IsActive,
	}
	if err := h.store.Create(c.Request.Context(), u); err != nil {
		response.Fail(c, err)
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
	if in.IsActive != nil && !*in.IsActive && h.isSelf(c, id) {
		response.Abort(c, response.CodeForbidden, "You cannot deactivate your own account.")
		return
	}
	fields := map[string]any{}
	if in.Email != nil {
		fields["email"] = strings.TrimSpace(*in.Email)
	}
	if in.Name != nil {
		fields["name"] = strings.TrimSpace(*in.Name)
	}
	if in.GroupID != nil {
		if *in.GroupID == "" {
			fields["group_id"] = nil
		} else {
			fields["group_id"] = *in.GroupID
		}
	}
	if in.IsActive != nil {
		fields["is_active"] = *in.IsActive
	}
	ctx := c.Request.Context()
	if err := h.store.Update(ctx, id, fields); err != nil {
		response.Fail(c, err)
		return
	}
	if in.IsActive != nil && !*in.IsActive {
		if err := h.sessions.RevokeAllForUser(ctx, id, h.now()); err != nil {
			response.Fail(c, err)
			return
		}
	}
	u, err := h.store.GetByID(ctx, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, u)
}

func (h *Handler) remove(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	if h.isSelf(c, id) {
		response.Abort(c, response.CodeForbidden, "You cannot delete your own account.")
		return
	}
	ctx := c.Request.Context()
	if err := h.store.Delete(ctx, id); err != nil {
		response.Fail(c, err)
		return
	}
	if err := h.sessions.RevokeAllForUser(ctx, id, h.now()); err != nil {
		response.Fail(c, err)
		return
	}
	response.Message(c, "Deleted.")
}

// setPassword lets the administrator set a new password. The sessions of that user end, so the old password
// (and anyone holding a token for it) stops working at once.
func (h *Handler) setPassword(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	var in passwordInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	if !h.checkPassword(c, in.Password, "password") {
		return
	}
	hash, err := h.hasher.Hash(in.Password)
	if err != nil {
		response.Fail(c, fmt.Errorf("users: hash password: %w", err))
		return
	}
	ctx := c.Request.Context()
	if err := h.store.Update(ctx, id, map[string]any{"password_hash": hash}); err != nil {
		response.Fail(c, err)
		return
	}
	if err := h.sessions.RevokeAllForUser(ctx, id, h.now()); err != nil {
		response.Fail(c, err)
		return
	}
	response.Message(c, "Password changed.")
}

func (h *Handler) checkPassword(c *gin.Context, plain, field string) bool {
	err := password.Validate(plain)
	if err == nil {
		return true
	}
	rule := "min"
	msg := "The password is too short."
	if errors.Is(err, password.ErrTooLong) {
		rule, msg = "max", "The password is too long."
	}
	response.Fail(c, &response.Error{
		Code: response.CodeValidationFailed, Message: msg,
		Details: []response.FieldError{{Field: field, Rule: rule}},
	})
	return false
}

func (h *Handler) isSelf(c *gin.Context, id string) bool {
	claims, ok := middleware.ClaimsFrom(c)
	return ok && claims.UserID == id
}
