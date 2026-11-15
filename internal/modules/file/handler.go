package file

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

const (
	multipartOverhead = 1 << 20 // room for the form framing and the text fields on top of the file
	memoryLimit       = 8 << 20 // larger parts of the form go to a temporary file
)

var filePolicy = request.ListPolicy{
	Filterable: []string{"purpose", "content_type", "uploaded_by", "name"},
	Sortable:   []string{"created_at", "name", "size"},
}

// Handler serves /files. It reads the request and writes the response; the rules are in the Service.
type Handler struct {
	svc  *Service
	mw   []gin.HandlerFunc
	base string
}

// NewHandler creates the handler. mw runs before every endpoint (Authenticate, TenantTx, Authorize).
func NewHandler(svc *Service, mw ...gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, mw: mw}
}

// RegisterRoutes registers /files under rg (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	h.base = rg.BasePath() + "/files"
	g := rg.Group("/files", h.mw...)
	g.POST("", h.upload)
	g.GET("", h.list)
	g.GET("/:id", h.get)
	g.GET("/:id/content", h.content)
	g.DELETE("/:id", h.remove)
}

// Document adds the endpoints to the OpenAPI document.
func (h *Handler) Document(d *openapi.Doc) {
	if h.base == "" {
		return
	}
	const tag = "files"
	var f File
	q := []openapi.Param{
		{Name: "page", Description: "page number, from 1"}, {Name: "per_page", Description: "items per page (at most 100)"},
		{Name: "sort", Description: "columns, a leading - means descending. Allowed: " + strings.Join(filePolicy.Sortable, ", ")},
		{Name: "from", Description: "created_at from"}, {Name: "to", Description: "created_at until, inclusive"},
	}
	for _, c := range filePolicy.Filterable {
		q = append(q, openapi.Param{Name: c, Description: "equality filter"})
	}
	d.Add(openapi.Op{Method: "POST", Path: h.base, Tag: tag, Result: f, Status: 201,
		Summary: "Upload a file (multipart/form-data). Allowed: " + strings.Join(h.svc.Types(), ", ") + "; at most " + strconv.FormatInt(h.svc.MaxBytes()>>20, 10) + " MiB",
		Upload:  &openapi.Upload{File: "file", Fields: []openapi.Param{{Name: "purpose", Description: "what the file is for, e.g. avatar (a-z, 0-9, - and _)"}}}})
	d.Add(openapi.Op{Method: "GET", Path: h.base, Summary: "List files of the tenant", Tag: tag, Result: f, Kind: openapi.List, Query: q})
	d.Add(openapi.Op{Method: "GET", Path: h.base + "/:id", Summary: "Get the metadata of a file", Tag: tag, Result: f})
	d.Add(openapi.Op{Method: "GET", Path: h.base + "/:id/content", Summary: "Download a file (add ?download=1 to force a download instead of showing it)", Tag: tag, Binary: true})
	d.Add(openapi.Op{Method: "DELETE", Path: h.base + "/:id", Summary: "Delete a file", Tag: tag, Kind: openapi.Message})
}

func invalid(field, rule, msg string) *response.Error {
	return &response.Error{Code: response.CodeValidationFailed, Message: msg,
		Details: []response.FieldError{{Field: field, Rule: rule}}}
}

func (h *Handler) tooLarge(c *gin.Context) {
	response.Abort(c, response.CodePayloadTooLarge, "The file is larger than "+strconv.FormatInt(h.svc.MaxBytes()>>20, 10)+" MiB.")
}

func (h *Handler) upload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.svc.MaxBytes()+multipartOverhead)
	if err := c.Request.ParseMultipartForm(memoryLimit); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			response.Fail(c, err)
			return
		}
		response.Abort(c, response.CodeInvalidRequest, "Send the file as multipart/form-data.")
		return
	}
	defer func() { _ = c.Request.MultipartForm.RemoveAll() }()

	fh, err := c.FormFile("file")
	if err != nil {
		response.Fail(c, invalid("file", "required", "The file field is required."))
		return
	}
	if fh.Size > h.svc.MaxBytes() {
		h.tooLarge(c)
		return
	}
	src, err := fh.Open()
	if err != nil {
		response.Fail(c, err)
		return
	}
	defer func() { _ = src.Close() }()

	in := UploadInput{Name: fh.Filename, Purpose: c.PostForm("purpose"), Content: src}
	if claims, ok := middleware.ClaimsFrom(c); ok {
		in.UploadedBy = claims.UserID
	}
	f, err := h.svc.Upload(c.Request.Context(), in)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.Created(c, f)
}

func (h *Handler) list(c *gin.Context) {
	params, err := request.ParseList(c, filePolicy)
	if err != nil {
		response.Fail(c, err)
		return
	}
	page, err := h.svc.List(c.Request.Context(), params)
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
	f, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, f)
}

// content streams the file. The type sent is the one detected at upload, with nosniff and a sandboxing CSP,
// so a browser never runs an uploaded file as a page.
func (h *Handler) content(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	f, rc, err := h.svc.Open(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	defer func() { _ = rc.Close() }()

	disp := "inline"
	if c.Query("download") == "1" {
		disp = "attachment"
	}
	hd := c.Writer.Header()
	hd.Set("Content-Type", f.ContentType)
	hd.Set("Content-Length", strconv.FormatInt(f.Size, 10))
	hd.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": f.Name}))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("Cache-Control", "private, no-cache")
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, rc); err != nil {
		_ = c.Error(err) // the status is sent already: the access log records the interrupted download
	}
}

func (h *Handler) remove(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.Message(c, "Deleted.")
}

// fail turns the errors of the service into responses; everything else goes through response.Fail.
func (h *Handler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrEmpty):
		response.Fail(c, invalid("file", "empty", "The file is empty."))
	case errors.Is(err, ErrTooLarge):
		h.tooLarge(c)
	case errors.Is(err, ErrTypeNotAllowed):
		response.Fail(c, invalid("file", "type", "This kind of file is not allowed. Allowed: "+strings.Join(h.svc.Types(), ", ")+"."))
	case errors.Is(err, ErrInvalidPurpose):
		response.Fail(c, invalid("purpose", "format", "The purpose may use a-z, 0-9, - and _ (at most 50 characters)."))
	default:
		response.Fail(c, err)
	}
}
