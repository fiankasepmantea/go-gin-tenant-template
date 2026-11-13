package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/storage"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// DefaultMaxBytes is the upload limit when none is configured.
const DefaultMaxBytes = 10 << 20

// DefaultTypes are the content types that can be uploaded. The type is detected from the bytes of the file,
// what the client says is ignored. SVG is left out on purpose: it can carry scripts.
var DefaultTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}

const (
	multipartOverhead = 1 << 20 // room for the form framing and the text fields on top of the file
	memoryLimit       = 8 << 20 // larger parts of the form go to a temporary file
	maxNameRunes      = 255
)

var purposeRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,49}$`)

// Store is what the handler needs from the repository. *Repository satisfies it.
type Store interface {
	Create(ctx context.Context, e *File) error
	GetByID(ctx context.Context, id string) (*File, error)
	List(ctx context.Context, p core.ListParams) (core.Page[File], error)
	Delete(ctx context.Context, id string) error
}

// Options configure the handler.
type Options struct {
	MaxBytes int64    // largest file; DefaultMaxBytes when zero
	Types    []string // allowed content types; DefaultTypes when empty
	Log      *slog.Logger
}

// Handler serves /files.
type Handler struct {
	repo    Store
	blobs   storage.Store
	max     int64
	allowed map[string]bool
	log     *slog.Logger
	mw      []gin.HandlerFunc
	base    string
}

// NewHandler creates the handler. mw runs before every endpoint (Authenticate, TenantTx, Authorize).
func NewHandler(repo Store, blobs storage.Store, opt Options, mw ...gin.HandlerFunc) *Handler {
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = DefaultMaxBytes
	}
	if len(opt.Types) == 0 {
		opt.Types = DefaultTypes
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	allowed := make(map[string]bool, len(opt.Types))
	for _, t := range opt.Types {
		allowed[t] = true
	}
	return &Handler{repo: repo, blobs: blobs, max: opt.MaxBytes, allowed: allowed, log: opt.Log, mw: mw}
}

var filePolicy = request.ListPolicy{
	Filterable: []string{"purpose", "content_type", "uploaded_by", "name"},
	Sortable:   []string{"created_at", "name", "size"},
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
		Summary: "Upload a file (multipart/form-data). Allowed: " + strings.Join(h.types(), ", ") + "; at most " + strconv.FormatInt(h.max>>20, 10) + " MiB",
		Upload:  &openapi.Upload{File: "file", Fields: []openapi.Param{{Name: "purpose", Description: "what the file is for, e.g. avatar (a-z, 0-9, - and _)"}}}})
	d.Add(openapi.Op{Method: "GET", Path: h.base, Summary: "List files of the tenant", Tag: tag, Result: f, Kind: openapi.List, Query: q})
	d.Add(openapi.Op{Method: "GET", Path: h.base + "/:id", Summary: "Get the metadata of a file", Tag: tag, Result: f})
	d.Add(openapi.Op{Method: "GET", Path: h.base + "/:id/content", Summary: "Download a file (add ?download=1 to force a download instead of showing it)", Tag: tag, Binary: true})
	d.Add(openapi.Op{Method: "DELETE", Path: h.base + "/:id", Summary: "Delete a file", Tag: tag, Kind: openapi.Message})
}

func (h *Handler) types() []string {
	out := make([]string, 0, len(h.allowed))
	for _, t := range DefaultTypes {
		if h.allowed[t] {
			out = append(out, t)
		}
	}
	for t := range h.allowed {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func invalid(field, rule, msg string) *response.Error {
	return &response.Error{Code: response.CodeValidationFailed, Message: msg,
		Details: []response.FieldError{{Field: field, Rule: rule}}}
}

func (h *Handler) upload(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := tenant.IDFrom(ctx)
	if !ok {
		response.Fail(c, core.ErrNoTenant)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.max+multipartOverhead)
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
	purpose := strings.TrimSpace(c.PostForm("purpose"))
	if purpose != "" && !purposeRe.MatchString(purpose) {
		response.Fail(c, invalid("purpose", "format", "The purpose may use a-z, 0-9, - and _ (at most 50 characters)."))
		return
	}
	if fh.Size == 0 {
		response.Fail(c, invalid("file", "empty", "The file is empty."))
		return
	}
	if fh.Size > h.max {
		response.Abort(c, response.CodePayloadTooLarge, "The file is larger than "+strconv.FormatInt(h.max>>20, 10)+" MiB.")
		return
	}

	src, err := fh.Open()
	if err != nil {
		response.Fail(c, err)
		return
	}
	defer func() { _ = src.Close() }()

	head := make([]byte, 512)
	n, err := io.ReadFull(src, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		response.Fail(c, err)
		return
	}
	head = head[:n]
	ctype, _, _ := strings.Cut(http.DetectContentType(head), ";")
	if !h.allowed[ctype] {
		response.Fail(c, invalid("file", "type", "This kind of file is not allowed. Allowed: "+strings.Join(h.types(), ", ")+"."))
		return
	}

	f := &File{
		ID: idgen.New(), Name: cleanName(fh.Filename), ContentType: ctype, Purpose: purpose,
	}
	f.StorageKey = tenantID + "/" + f.ID
	if claims, ok := middleware.ClaimsFrom(c); ok && claims.UserID != "" {
		f.UploadedBy = &claims.UserID
	}

	size, err := h.blobs.Put(ctx, f.StorageKey, io.MultiReader(bytes.NewReader(head), src), h.max)
	if errors.Is(err, storage.ErrTooLarge) {
		response.Abort(c, response.CodePayloadTooLarge, "The file is larger than "+strconv.FormatInt(h.max>>20, 10)+" MiB.")
		return
	}
	if err != nil {
		response.Fail(c, err)
		return
	}
	f.Size = size
	if err := h.repo.Create(ctx, f); err != nil {
		h.dropBlob(ctx, f.StorageKey)
		response.Fail(c, err)
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
	page, err := h.repo.List(c.Request.Context(), params)
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
	f, err := h.repo.GetByID(c.Request.Context(), id)
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
	ctx := c.Request.Context()
	f, err := h.repo.GetByID(ctx, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	rc, err := h.blobs.Open(ctx, f.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		h.log.Error("file content is missing from the storage", "file_id", f.ID, "key", f.StorageKey)
		response.Abort(c, response.CodeNotFound, "")
		return
	}
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
		h.log.Warn("file download interrupted", "file_id", f.ID, "error", err)
	}
}

func (h *Handler) remove(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	f, err := h.repo.GetByID(ctx, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	if err := h.repo.Delete(ctx, id); err != nil {
		response.Fail(c, err)
		return
	}
	h.dropBlob(ctx, f.StorageKey)
	response.Message(c, "Deleted.")
}

// dropBlob removes content that no row points to (any more). A failure only leaves an orphan, which is
// logged: it is not worth failing the request for.
func (h *Handler) dropBlob(ctx context.Context, key string) {
	if err := h.blobs.Delete(context.WithoutCancel(ctx), key); err != nil {
		h.log.Error("cannot remove file content", "key", key, "error", err)
	}
}

// cleanName reduces a client-supplied file name to something safe to show and to put in a header:
// no directories, no control characters, at most 255 characters.
func cleanName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxNameRunes {
		name = string(r[:maxNameRunes])
	}
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}
