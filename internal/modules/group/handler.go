package group

import (
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/crud"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
)

// createInput and updateInput are the only fields a client can set. Identity, tenant and timestamps are not here.
type createInput struct {
	Name        string `json:"name" binding:"required,max=100"`
	Description string `json:"description" binding:"max=500"`
}

type updateInput struct { // pointers: nil means "not sent"
	Name        *string `json:"name" binding:"omitempty,min=1,max=100"`
	Description *string `json:"description" binding:"omitempty,max=500"`
}

// Handler serves /groups with the standard endpoints of crud.Handler.
type Handler struct {
	h  *crud.Handler[Group, *Group, createInput, updateInput]
	mw []gin.HandlerFunc
}

// NewHandler creates the handler. mw runs before every endpoint; pass the authentication chain
// (Authenticate, TenantTx, Authorize) here. The handler itself decides nothing about who may call it.
func NewHandler(db *gorm.DB, mw ...gin.HandlerFunc) *Handler {
	h := crud.New(crud.Config[Group, *Group, createInput, updateInput]{
		Store:      NewRepository(db),
		Filterable: []string{"name"},
		Sortable:   []string{"name", "created_at"},
		Build: func(in createInput) *Group {
			return &Group{Name: strings.TrimSpace(in.Name), Description: in.Description}
		},
		Changes: func(in updateInput) map[string]any {
			m := map[string]any{}
			if in.Name != nil {
				m["name"] = strings.TrimSpace(*in.Name)
			}
			if in.Description != nil {
				m["description"] = *in.Description
			}
			return m
		},
	})
	return &Handler{h: h, mw: mw}
}

// Document adds the endpoints to the OpenAPI document.
func (h *Handler) Document(d *openapi.Doc) { h.h.Document(d) }

// RegisterRoutes registers /groups under rg (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	h.h.Register(rg.Group("/groups"), h.mw...)
}
