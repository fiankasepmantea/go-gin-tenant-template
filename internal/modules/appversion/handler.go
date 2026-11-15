package appversion

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

const (
	multipartOverhead = 1 << 20
	memoryLimit       = 8 << 20
	apkMIME           = "application/vnd.android.package-archive"
)

type createInput struct {
	Platform            string `json:"platform" binding:"required,oneof=android ios web"`
	Version             string `json:"version" binding:"required,max=32"`
	MinSupportedVersion string `json:"min_supported_version" binding:"max=32"`
	IsForceUpdate       bool   `json:"is_force_update"`
	ReleaseNotes        string `json:"release_notes" binding:"max=5000"`
	StoreURL            string `json:"store_url" binding:"max=2048"`
}

type updateInput struct { // pointers: nil means "not sent"
	MinSupportedVersion *string `json:"min_supported_version" binding:"omitempty,max=32"`
	IsForceUpdate       *bool   `json:"is_force_update"`
	ReleaseNotes        *string `json:"release_notes" binding:"omitempty,max=5000"`
	StoreURL            *string `json:"store_url" binding:"omitempty,max=2048"`
}

var versionPolicy = request.ListPolicy{
	Filterable: []string{"platform", "version", "is_force_update"},
	Sortable:   []string{"created_at", "version", "platform"},
}

// PublicVersion is the version as the app sees it: no internal ids of storage, only what it needs.
type PublicVersion struct {
	Platform            string    `json:"platform"`
	Version             string    `json:"version"`
	MinSupportedVersion string    `json:"min_supported_version"`
	IsForceUpdate       bool      `json:"is_force_update"`
	ReleaseNotes        string    `json:"release_notes"`
	StoreURL            string    `json:"store_url"`
	Package             *Pkg      `json:"package"`
	ReleasedAt          time.Time `json:"released_at"`
}

// Pkg describes the downloadable package of a version.
type Pkg struct {
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url"`
}

func publicOf(a *AppVersion) PublicVersion {
	p := PublicVersion{
		Platform: a.Platform, Version: a.Version, MinSupportedVersion: a.MinSupportedVersion,
		IsForceUpdate: a.IsForceUpdate, ReleaseNotes: a.ReleaseNotes, StoreURL: a.StoreURL,
	}
	if a.HasPackage() && a.PackageSize != nil && a.PackageSHA256 != nil {
		p.Package = &Pkg{Size: *a.PackageSize, SHA256: *a.PackageSHA256, DownloadURL: DownloadPath(a.ID)}
	}
	p.ReleasedAt = a.CreatedAt
	return p
}

// Handler serves the app version routes. It reads the request and writes the response; the rules are in the
// Service.
//
//	platform admins (scope platform, the middleware chain applies):
//	  GET/POST /app-versions, GET/PATCH/DELETE /app-versions/:id, PUT /app-versions/:id/package
//	everybody, no token (the app asks before anyone has logged in):
//	  GET /public/app-versions/latest, /latest/download, /check, /:id/download
type Handler struct {
	svc  *Service
	mw   []gin.HandlerFunc
	base string
}

// NewHandler creates the handler. mw runs before the platform admin endpoints (Authenticate, TenantTx,
// Authorize); the public endpoints never run it.
func NewHandler(svc *Service, mw ...gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, mw: mw}
}

// RegisterRoutes registers the routes under rg (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	h.base = rg.BasePath()
	a := rg.Group("/app-versions", h.mw...)
	a.GET("", h.list)
	a.POST("", h.create)
	a.GET("/:id", h.get)
	a.PATCH("/:id", h.update)
	a.DELETE("/:id", h.remove)
	a.PUT("/:id/package", h.upload)

	p := rg.Group("/public/app-versions")
	p.GET("/latest", h.latest)
	p.GET("/latest/download", h.latestDownload)
	p.GET("/check", h.check)
	p.GET("/:id/download", h.download)
}

// Document adds the routes to the OpenAPI document.
func (h *Handler) Document(d *openapi.Doc) {
	if h.base == "" {
		return
	}
	const tag = "app-versions"
	adm := h.base + "/app-versions"
	pub := h.base + "/public/app-versions"
	var v AppVersion
	q := []openapi.Param{
		{Name: "page", Description: "page number, from 1"}, {Name: "per_page", Description: "items per page (at most 100)"},
		{Name: "sort", Description: "columns, a leading - means descending. Allowed: " + strings.Join(versionPolicy.Sortable, ", ")},
	}
	for _, c := range versionPolicy.Filterable {
		q = append(q, openapi.Param{Name: c, Description: "equality filter"})
	}
	platform := openapi.Param{Name: "platform", Description: "android, ios or web"}

	d.Add(openapi.Op{Method: "GET", Path: adm, Summary: "List app versions (platform admin)", Tag: tag, Result: v, Kind: openapi.List, Query: q})
	d.Add(openapi.Op{Method: "POST", Path: adm, Summary: "Create an app version (platform admin). Upload the package next with PUT /:id/package", Tag: tag, Body: createInput{}, Result: v, Status: 201})
	d.Add(openapi.Op{Method: "GET", Path: adm + "/:id", Summary: "Get an app version (platform admin)", Tag: tag, Result: v})
	d.Add(openapi.Op{Method: "PATCH", Path: adm + "/:id", Summary: "Change an app version (platform admin; platform and version never change)", Tag: tag, Body: updateInput{}, Result: v})
	d.Add(openapi.Op{Method: "DELETE", Path: adm + "/:id", Summary: "Delete an app version and its package (platform admin)", Tag: tag, Kind: openapi.Message})
	d.Add(openapi.Op{Method: "PUT", Path: adm + "/:id/package", Tag: tag, Result: v,
		Summary: "Upload the APK of an android version (platform admin, multipart/form-data, field file, at most " + strconv.FormatInt(h.svc.MaxPackageBytes()>>20, 10) + " MiB). Replaces an earlier package",
		Upload:  &openapi.Upload{File: "file"}})

	d.Add(openapi.Op{Method: "GET", Path: pub + "/latest", Summary: "The latest version of a platform (no token)", Tag: tag, Result: PublicVersion{}, Public: true, Query: []openapi.Param{platform}})
	d.Add(openapi.Op{Method: "GET", Path: pub + "/latest/download", Summary: "Download the package of the latest android version (no token; supports Range)", Tag: tag, Binary: true, Public: true, Query: []openapi.Param{platform}})
	d.Add(openapi.Op{Method: "GET", Path: pub + "/check", Summary: "Does the installed version have to update? (no token)", Tag: tag, Result: CheckResult{}, Public: true,
		Query: []openapi.Param{platform, {Name: "version", Description: "the installed version, like 1.4.2"}}})
	d.Add(openapi.Op{Method: "GET", Path: pub + "/:id/download", Summary: "Download the package of a version (no token; supports Range)", Tag: tag, Binary: true, Public: true})
}

func (h *Handler) list(c *gin.Context) {
	params, err := request.ParseList(c, versionPolicy)
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
	a, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, a)
}

func (h *Handler) create(c *gin.Context) {
	var in createInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	a, err := h.svc.Create(c.Request.Context(), CreateInput(in))
	if err != nil {
		h.fail(c, err)
		return
	}
	response.Created(c, a)
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
	a, err := h.svc.Update(c.Request.Context(), id, Changes(in))
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, a)
}

func (h *Handler) remove(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		h.fail(c, err)
		return
	}
	response.Message(c, "Deleted.")
}

func (h *Handler) upload(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.svc.MaxPackageBytes()+multipartOverhead)
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
		h.fail(c, invalid("file", "required", "The file field is required."))
		return
	}
	src, err := fh.Open()
	if err != nil {
		response.Fail(c, err)
		return
	}
	defer func() { _ = src.Close() }()

	a, err := h.svc.UploadPackage(c.Request.Context(), id, src, fh.Size)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, a)
}

// ---- public ----

func (h *Handler) latest(c *gin.Context) {
	a, err := h.svc.Latest(c.Request.Context(), c.Query("platform"))
	if err != nil {
		h.fail(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	response.OK(c, publicOf(a))
}

func (h *Handler) check(c *gin.Context) {
	res, err := h.svc.Check(c.Request.Context(), c.Query("platform"), c.Query("version"))
	if err != nil {
		h.fail(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	response.OK(c, res)
}

func (h *Handler) latestDownload(c *gin.Context) {
	a, rc, err := h.svc.OpenLatestPackage(c.Request.Context(), c.Query("platform"))
	h.send(c, a, rc, err)
}

func (h *Handler) download(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	a, rc, err := h.svc.OpenPackage(c.Request.Context(), id)
	h.send(c, a, rc, err)
}

// send writes the package. When the storage can seek (the local driver) http.ServeContent does the work, and a
// download that broke off continues with a Range request instead of starting over; ETag (the SHA-256) lets the
// app ask "has it changed?" with If-None-Match.
func (h *Handler) send(c *gin.Context, a *AppVersion, rc io.ReadCloser, err error) {
	if err != nil {
		h.fail(c, err)
		return
	}
	defer func() { _ = rc.Close() }()

	hd := c.Writer.Header()
	hd.Set("Content-Type", apkMIME)
	hd.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Platform + "-" + a.Version + ".apk"}))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "public, max-age=300")
	if a.PackageSHA256 != nil {
		hd.Set("ETag", `"`+*a.PackageSHA256+`"`)
	}
	if rs, ok := rc.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, "", a.UpdatedAt, rs)
		return
	}
	if a.PackageSize != nil {
		hd.Set("Content-Length", strconv.FormatInt(*a.PackageSize, 10))
	}
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, rc); err != nil {
		_ = c.Error(err) // the status is sent already: the access log records the interrupted download
	}
}

// fail turns the errors of the service into responses; everything else goes through response.Fail.
func (h *Handler) fail(c *gin.Context, err error) {
	var ve *ValidationError
	switch {
	case errors.As(err, &ve):
		response.Fail(c, &response.Error{
			Code: response.CodeValidationFailed, Message: ve.Message,
			Details: []response.FieldError{{Field: ve.Field, Rule: ve.Rule}},
		})
	case errors.Is(err, ErrPackageTooLarge):
		response.Abort(c, response.CodePayloadTooLarge, "The package is larger than "+strconv.FormatInt(h.svc.MaxPackageBytes()>>20, 10)+" MiB.")
	case errors.Is(err, ErrNoPackage):
		response.Abort(c, response.CodeNotFound, "This version has no package to download.")
	default:
		response.Fail(c, err)
	}
}
