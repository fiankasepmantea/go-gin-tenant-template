// Package attachment attaches uploaded files (module file) to the records of any other module: photos of an
// activity, documents of a case, the signature of a user. It is a reusable service and handler, configured per
// table:
//
//	attachment.NewHandler(attachment.NewService(attachment.Config{
//	    Name: "documents", Parent: "activities",
//	    Table: "activity_documents", IDColumn: "activity_document_id",
//	    ParentTable: "activities", ParentColumn: "activity_id",
//	}, db, fileService), chain...)
//
// serves
//
//	GET    /activities/:id/documents                  files attached to the record
//	POST   /activities/:id/documents                  {"file_id": "..."} attach an uploaded file
//	DELETE /activities/:id/documents/:attachment_id   detach and delete the file
//
// The table is made by a migration of the application (docs/attachments.md has the SQL). The composite foreign
// keys of that SQL, with row level security, are what keeps a tenant away from the records and files of another.
package attachment

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

type attachInput struct {
	FileID string `json:"file_id" binding:"required,uuid"`
}

// Handler serves the three routes of one attachment table. It reads the request and writes the response; the
// rules are in the Service.
type Handler struct {
	svc  *Service
	mw   []gin.HandlerFunc
	base string
}

// NewHandler creates the handler. mw runs before every endpoint (Authenticate, TenantTx, Authorize).
func NewHandler(svc *Service, mw ...gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, mw: mw}
}

// RegisterRoutes registers the routes under rg (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	cfg := h.svc.Config()
	h.base = rg.BasePath() + "/" + cfg.Parent + "/:id/" + cfg.Name
	g := rg.Group("/"+cfg.Parent+"/:id/"+cfg.Name, h.mw...)
	g.GET("", h.list)
	g.POST("", h.attach)
	g.DELETE("/:attachment_id", h.detach)
}

// Document adds the routes to the OpenAPI document.
func (h *Handler) Document(d *openapi.Doc) {
	if h.base == "" {
		return
	}
	cfg := h.svc.Config()
	tag := cfg.Name
	d.Add(openapi.Op{Method: "GET", Path: h.base, Summary: "List the " + cfg.Name + " of a " + cfg.Parent, Tag: tag, Result: Item{}, Kind: openapi.List})
	d.Add(openapi.Op{Method: "POST", Path: h.base, Summary: "Attach an uploaded file (POST /files first) to a " + cfg.Parent, Tag: tag, Body: attachInput{}, Result: Item{}, Status: 201})
	d.Add(openapi.Op{Method: "DELETE", Path: h.base + "/:attachment_id", Summary: "Detach and delete the file", Tag: tag, Kind: openapi.Message})
}

func (h *Handler) list(c *gin.Context) {
	parentID, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	items, err := h.svc.List(c.Request.Context(), parentID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, items)
}

func (h *Handler) attach(c *gin.Context) {
	parentID, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	var in attachInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	item, err := h.svc.Attach(c.Request.Context(), parentID, in.FileID)
	if errors.Is(err, ErrFileNotFound) {
		response.Fail(c, &response.Error{Code: response.CodeValidationFailed, Message: "The file does not exist. Upload it first.",
			Details: []response.FieldError{{Field: "file_id", Rule: "exists"}}})
		return
	}
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, item)
}

func (h *Handler) detach(c *gin.Context) {
	parentID, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	attID, ok := request.UUIDParam(c, "attachment_id")
	if !ok {
		return
	}
	if err := h.svc.Detach(c.Request.Context(), parentID, attID); err != nil {
		response.Fail(c, err)
		return
	}
	response.Message(c, "Deleted.")
}
