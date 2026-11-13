package rbac

import (
	"context"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/group"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/openapi"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/request"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// groupFinder checks that a group exists in the tenant of the context (*group.Repository).
type groupFinder interface {
	GetByID(ctx context.Context, id string) (*group.Group, error)
}

// grantWriter replaces the grants of a group (*Repository).
type grantWriter interface {
	ReplaceGroupEndpoints(ctx context.Context, groupID string, endpointIDs []string) error
}

// Handler serves the permission endpoints that an admin uses to decide what a group may do:
//
//	GET /rbac/endpoints            the endpoints that can be granted
//	GET /groups/:id/permissions    the endpoints granted to a group
//	PUT /groups/:id/permissions    replace them (body: {"endpoint_ids": [...]})
//
// These endpoints are themselves protected by RBAC like any other (mount them with the same middleware chain).
type Handler struct {
	svc    *Service
	groups groupFinder
	grants grantWriter
	mw     []gin.HandlerFunc
}

func NewHandler(svc *Service, groups groupFinder, grants grantWriter, mw ...gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, groups: groups, grants: grants, mw: mw}
}

// RegisterRoutes registers the endpoints under rg (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("", h.mw...)
	g.GET("/rbac/endpoints", h.listEndpoints)
	g.GET("/groups/:id/permissions", h.getPermissions)
	g.PUT("/groups/:id/permissions", h.setPermissions)
}

// Document adds the permission endpoints to the OpenAPI document.
func (h *Handler) Document(d *openapi.Doc) {
	d.Add(openapi.Op{Method: "GET", Path: "/api/v1/rbac/endpoints", Summary: "Endpoints that can be granted to a group", Tag: "rbac", Result: []endpointView{}})
	d.Add(openapi.Op{Method: "GET", Path: "/api/v1/groups/:id/permissions", Summary: "Endpoints granted to a group", Tag: "rbac", Result: permissions{}})
	d.Add(openapi.Op{Method: "PUT", Path: "/api/v1/groups/:id/permissions", Summary: "Replace the endpoints granted to a group ([] revokes everything)", Tag: "rbac", Body: setPermissionsInput{}, Result: permissions{}})
}

type endpointView struct {
	ID     string `json:"endpoint_id"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

func (h *Handler) listEndpoints(c *gin.Context) {
	list, err := h.svc.Assignable(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]endpointView, 0, len(list))
	for _, e := range list {
		out = append(out, endpointView{ID: e.ID, Method: e.Method, Path: e.Path})
	}
	response.OK(c, out)
}

type permissions struct {
	GroupID     string   `json:"group_id"`
	EndpointIDs []string `json:"endpoint_ids"`
}

func (h *Handler) getPermissions(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := h.groups.GetByID(ctx, id); err != nil {
		response.Fail(c, err)
		return
	}
	ids, err := h.svc.GroupEndpointIDs(ctx, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, permissions{GroupID: id, EndpointIDs: nonNil(ids)})
}

type setPermissionsInput struct {
	// required (not "omitempty"): sending nothing is an error, sending [] revokes everything.
	EndpointIDs []string `json:"endpoint_ids" binding:"required,max=1000,dive,uuid"`
}

func (h *Handler) setPermissions(c *gin.Context) {
	id, ok := request.UUIDParam(c, "id")
	if !ok {
		return
	}
	var in setPermissionsInput
	if !request.BindJSON(c, &in, 0) {
		return
	}
	ctx := c.Request.Context()

	if _, err := h.groups.GetByID(ctx, id); err != nil { // 404 for a group of another tenant too
		response.Fail(c, err)
		return
	}

	// Only endpoints that exist, have tenant scope and are not public can be granted. This is what stops a
	// tenant admin from granting a platform endpoint, whatever ids the client sends.
	assignable, err := h.svc.Assignable(ctx)
	if err != nil {
		response.Fail(c, err)
		return
	}
	allowed := make(map[string]bool, len(assignable))
	for _, e := range assignable {
		allowed[e.ID] = true
	}
	ids := dedupe(in.EndpointIDs)
	for _, eid := range ids {
		if !allowed[eid] {
			response.Abort(c, response.CodeValidationFailed, "Unknown endpoint: "+eid)
			return
		}
	}

	if err := h.grants.ReplaceGroupEndpoints(ctx, id, ids); err != nil {
		response.Fail(c, err)
		return
	}
	if tid, ok := tenant.IDFrom(ctx); ok {
		h.svc.InvalidateGroup(tid, id)
	}
	response.OK(c, permissions{GroupID: id, EndpointIDs: ids})
}

// dedupe returns the ids normalized (lower case canonical UUID text), without duplicates, sorted.
func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, s := range ids {
		u, err := uuid.Parse(s)
		if err != nil {
			continue // already rejected by validation; never reached
		}
		k := u.String()
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
