package crud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	id1 = "0198f6a0-1c2d-7abc-8def-0123456789ab"
	id2 = "0198f6a0-1c2d-7abc-8def-0123456789ac"
)

type widget struct {
	ID       string `json:"widget_id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	Qty      int    `json:"qty"`
}

func (*widget) TableName() string       { return "widgets" }
func (*widget) PrimaryKey() string      { return "widget_id" }
func (w *widget) GetID() string         { return w.ID }
func (w *widget) SetID(id string)       { w.ID = id }
func (w *widget) SetTenantID(id string) { w.TenantID = id }

type createIn struct {
	Name string `json:"name" binding:"required,max=10"`
	Qty  int    `json:"qty" binding:"gte=0"`
}

type updateIn struct {
	Name *string `json:"name" binding:"omitempty,max=10"`
	Qty  *int    `json:"qty" binding:"omitempty,gte=0"`
}

// fakeStore records what the handler asked for and answers from canned data.
type fakeStore struct {
	items      map[string]*widget
	lastList   core.ListParams
	created    *widget
	updatedID  string
	updatedSet map[string]any
	deletedID  string
	err        error // returned by every call when set
}

func newStore() *fakeStore {
	return &fakeStore{items: map[string]*widget{
		id1: {ID: id1, TenantID: "t", Name: "alpha", Qty: 1},
		id2: {ID: id2, TenantID: "t", Name: "beta", Qty: 2},
	}}
}

func (s *fakeStore) Create(_ context.Context, e *widget) error {
	if s.err != nil {
		return s.err
	}
	if e.ID == "" {
		e.ID = id1[:len(id1)-1] + "f"
	}
	e.TenantID = "tenant-from-context" // what the real repository does
	s.created = e
	return nil
}

func (s *fakeStore) GetByID(_ context.Context, id string) (*widget, error) {
	if s.err != nil {
		return nil, s.err
	}
	if w, ok := s.items[id]; ok {
		return w, nil
	}
	return nil, core.ErrNotFound
}

func (s *fakeStore) List(_ context.Context, p core.ListParams) (core.Page[widget], error) {
	s.lastList = p
	if s.err != nil {
		return core.Page[widget]{}, s.err
	}
	var items []widget
	for _, id := range []string{id1, id2} {
		w := *s.items[id]
		match := true
		for _, f := range p.Filters {
			if f.Column == "name" && f.Value != w.Name {
				match = false
			}
		}
		if match {
			items = append(items, w)
		}
	}
	limit := p.PerPage
	if limit == 0 {
		limit = 20
	}
	page := p.Page
	if page == 0 {
		page = 1
	}
	total := int64(len(items))
	if limit < len(items) {
		items = items[:limit]
	}
	return core.Page[widget]{Items: items, Total: total, Page: page, PerPage: limit}, nil
}

func (s *fakeStore) Update(_ context.Context, id string, fields map[string]any) error {
	if s.err != nil {
		return s.err
	}
	if len(fields) == 0 {
		return core.ErrNoFields
	}
	if _, ok := s.items[id]; !ok {
		return core.ErrNotFound
	}
	s.updatedID, s.updatedSet = id, fields
	return nil
}

func (s *fakeStore) Delete(_ context.Context, id string) error {
	if s.err != nil {
		return s.err
	}
	if _, ok := s.items[id]; !ok {
		return core.ErrNotFound
	}
	s.deletedID = id
	return nil
}

func newHandler(s *fakeStore) *Handler[widget, *widget, createIn, updateIn] {
	return New(Config[widget, *widget, createIn, updateIn]{
		Store:      s,
		Filterable: []string{"name", "qty"},
		Sortable:   []string{"name", "qty"},
		Build: func(in createIn) *widget {
			return &widget{Name: in.Name, Qty: in.Qty}
		},
		Changes: func(in updateIn) map[string]any {
			m := map[string]any{}
			if in.Name != nil {
				m["name"] = *in.Name
			}
			if in.Qty != nil {
				m["qty"] = *in.Qty
			}
			return m
		},
		MaxBody: 200,
	})
}

func serve(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func router(h *Handler[widget, *widget, createIn, updateIn], mw ...gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	h.Register(r.Group("/widgets"), mw...)
	return r
}

type envelope struct {
	Success bool            `json:"success"`
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Field string `json:"field"`
			Rule  string `json:"rule"`
		} `json:"details"`
	} `json:"error"`
}

func decode(t *testing.T, w *httptest.ResponseRecorder) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("not JSON: %v: %s", err, w.Body)
	}
	return e
}

func TestNewPanicsWithoutStore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New must panic when Store is nil")
		}
	}()
	New(Config[widget, *widget, createIn, updateIn]{})
}

func TestList(t *testing.T) {
	s := newStore()
	r := router(newHandler(s))

	w := serve(r, http.MethodGet, "/widgets?page=1&per_page=1&sort=-qty&name=alpha", "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := s.lastList; got.Page != 1 || got.PerPage != 1 || len(got.Sort) != 1 || !got.Sort[0].Desc || got.Sort[0].Column != "qty" ||
		len(got.Filters) != 1 || got.Filters[0].Column != "name" {
		t.Errorf("repository received %+v", got)
	}
	var page struct {
		TotalData  int64 `json:"total_data"`
		TotalPages int   `json:"total_pages"`
		CurrPage   int   `json:"curr_page"`
		Data       []widget
	}
	e := decode(t, w)
	if !e.Success || e.Code != 200 {
		t.Errorf("envelope = %+v", e)
	}
	if err := json.Unmarshal(e.Data, &page); err != nil || page.TotalData != 1 || page.CurrPage != 1 || len(page.Data) != 1 || page.Data[0].Name != "alpha" {
		t.Errorf("page = %+v (%v)", page, err)
	}
}

func TestListRejectsBadQueries(t *testing.T) {
	s := newStore()
	r := router(newHandler(s))
	for _, q := range []string{"?tenant_id=x", "?sort=tenant_id", "?page=0", "?nam=a", "?name=a;b", "?from=nope"} {
		w := serve(r, http.MethodGet, "/widgets"+q, "")
		if w.Code != 400 || decode(t, w).Error.Code != "invalid_request" {
			t.Errorf("%s: status %d body %s", q, w.Code, w.Body)
		}
	}
	if s.lastList.Page != 0 || len(s.lastList.Filters) != 0 {
		t.Error("the repository must not be called for a rejected query")
	}
}

func TestGet(t *testing.T) {
	r := router(newHandler(newStore()))

	w := serve(r, http.MethodGet, "/widgets/"+id1, "")
	var got widget
	e := decode(t, w)
	if w.Code != 200 || json.Unmarshal(e.Data, &got) != nil || got.Name != "alpha" {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}

	for target, desc := range map[string]string{
		"/widgets/0198f6a0-1c2d-7abc-8def-000000000000": "unknown id",
		"/widgets/not-a-uuid":                           "malformed id",
	} {
		w = serve(r, http.MethodGet, target, "")
		if w.Code != 404 || decode(t, w).Error.Code != "not_found" {
			t.Errorf("%s: %d %s", desc, w.Code, w.Body)
		}
	}
}

func TestShowByAndAllBy(t *testing.T) {
	s := newStore()
	r := router(newHandler(s))

	w := serve(r, http.MethodGet, "/widgets/show_by/name/beta", "")
	var got widget
	if err := json.Unmarshal(decode(t, w).Data, &got); w.Code != 200 || err != nil || got.ID != id2 {
		t.Errorf("show_by: %d %s", w.Code, w.Body)
	}
	if s.lastList.PerPage != 1 || s.lastList.Page != 1 {
		t.Errorf("show_by must fetch exactly one row, got %+v", s.lastList)
	}

	if w = serve(r, http.MethodGet, "/widgets/show_by/name/nobody", ""); w.Code != 404 {
		t.Errorf("show_by without a match: %d", w.Code)
	}

	w = serve(r, http.MethodGet, "/widgets/all_by/name/alpha?per_page=5", "")
	var page struct{ Data []widget }
	if err := json.Unmarshal(decode(t, w).Data, &page); w.Code != 200 || err != nil || len(page.Data) != 1 || s.lastList.PerPage != 5 {
		t.Errorf("all_by: %d %s", w.Code, w.Body)
	}

	// a column that is not on the whitelist is rejected before any query
	s.lastList = core.ListParams{}
	for _, col := range []string{"tenant_id", "password_hash", "name%3Bdrop"} {
		w = serve(r, http.MethodGet, "/widgets/show_by/"+col+"/x", "")
		if w.Code != 400 {
			t.Errorf("show_by/%s: status %d", col, w.Code)
		}
		w = serve(r, http.MethodGet, "/widgets/all_by/"+col+"/x", "")
		if w.Code != 400 {
			t.Errorf("all_by/%s: status %d", col, w.Code)
		}
	}
	if len(s.lastList.Filters) != 0 {
		t.Error("the repository must not be called for a rejected column")
	}
}

func TestCreate(t *testing.T) {
	s := newStore()
	r := router(newHandler(s))

	// A client that tries to set the identity or the tenant: those fields are not in the input type, so they are ignored.
	w := serve(r, http.MethodPost, "/widgets", `{"name":"gamma","qty":3,"widget_id":"`+id2+`","tenant_id":"someone-else"}`)
	if w.Code != 201 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if s.created == nil || s.created.Name != "gamma" || s.created.Qty != 3 {
		t.Fatalf("created = %+v", s.created)
	}
	if s.created.ID == id2 {
		t.Error("the client must not be able to choose the ID")
	}
	if s.created.TenantID != "tenant-from-context" {
		t.Errorf("tenant_id = %q: it must come from the context, not the body", s.created.TenantID)
	}
	if e := decode(t, w); !e.Success || e.Code != 201 {
		t.Errorf("envelope = %+v", e)
	}
}

func TestCreateErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		storeErr   error
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{"missing required field", `{"qty":1}`, nil, 422, "validation_failed", "name"},
		{"rule violated", `{"name":"way-too-long-name","qty":1}`, nil, 422, "validation_failed", "name"},
		{"negative number", `{"name":"x","qty":-1}`, nil, 422, "validation_failed", "qty"},
		{"broken JSON", `{"name":`, nil, 400, "invalid_request", ""},
		{"wrong type", `{"name":"x","qty":"many"}`, nil, 400, "invalid_request", ""},
		{"body too large", `{"name":"` + strings.Repeat("a", 500) + `"}`, nil, 413, "payload_too_large", ""},
		{"duplicate", `{"name":"x","qty":1}`, core.ErrConflict, 409, "conflict", ""},
		{"missing tenant", `{"name":"x","qty":1}`, core.ErrNoTenant, 403, "forbidden", ""},
		{"database failure", `{"name":"x","qty":1}`, errors.New("connection refused: host=db-internal-7"), 500, "internal_error", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore()
			s.err = tt.storeErr
			w := serve(router(newHandler(s)), http.MethodPost, "/widgets", tt.body)
			e := decode(t, w)
			if w.Code != tt.wantStatus || e.Error.Code != tt.wantCode || e.Success {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			if tt.wantField != "" && (len(e.Error.Details) == 0 || e.Error.Details[0].Field != tt.wantField) {
				t.Errorf("details = %+v, want field %s", e.Error.Details, tt.wantField)
			}
			if strings.Contains(w.Body.String(), "db-internal-7") {
				t.Error("an internal error text reached the client")
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	s := newStore()
	r := router(newHandler(s))

	// only the fields that were sent are changed; unknown fields (tenant_id) are ignored
	w := serve(r, http.MethodPatch, "/widgets/"+id1, `{"qty":9,"tenant_id":"x"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(s.updatedSet) != 1 || s.updatedSet["qty"] != 9 {
		t.Errorf("update fields = %v, want only qty", s.updatedSet)
	}

	tests := []struct {
		name, target, body string
		wantStatus         int
	}{
		{"empty body object", "/widgets/" + id1, `{}`, 400},
		{"unknown record", "/widgets/0198f6a0-1c2d-7abc-8def-000000000000", `{"qty":1}`, 404},
		{"malformed id", "/widgets/nope", `{"qty":1}`, 404},
		{"invalid value", "/widgets/" + id1, `{"qty":-5}`, 422},
	}
	for _, tt := range tests {
		if w := serve(r, http.MethodPatch, tt.target, tt.body); w.Code != tt.wantStatus {
			t.Errorf("%s: status %d, want %d (%s)", tt.name, w.Code, tt.wantStatus, w.Body)
		}
	}
}

func TestDelete(t *testing.T) {
	s := newStore()
	r := router(newHandler(s))

	w := serve(r, http.MethodDelete, "/widgets/"+id2, "")
	if e := decode(t, w); w.Code != 200 || !e.Success || e.Message == "" || s.deletedID != id2 {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w = serve(r, http.MethodDelete, "/widgets/0198f6a0-1c2d-7abc-8def-000000000000", ""); w.Code != 404 {
		t.Errorf("delete unknown: %d", w.Code)
	}
	if w = serve(r, http.MethodDelete, "/widgets/nope", ""); w.Code != 404 {
		t.Errorf("delete malformed id: %d", w.Code)
	}
}

func TestOptionalEndpoints(t *testing.T) {
	h := newHandler(newStore())
	h.cfg.Build, h.cfg.Changes = nil, nil // a read-only resource
	r := router(h)

	if w := serve(r, http.MethodPost, "/widgets", `{"name":"x"}`); w.Code != 404 && w.Code != 405 {
		t.Errorf("POST on a read-only resource: %d", w.Code)
	}
	if w := serve(r, http.MethodPatch, "/widgets/"+id1, `{"qty":1}`); w.Code != 404 && w.Code != 405 {
		t.Errorf("PATCH on a read-only resource: %d", w.Code)
	}
	if w := serve(r, http.MethodGet, "/widgets", ""); w.Code != 200 {
		t.Errorf("GET: %d", w.Code)
	}
}

func TestMiddlewareRunsBeforeEveryEndpoint(t *testing.T) {
	s := newStore()
	deny := func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }
	r := router(newHandler(s), deny)

	for _, tc := range []struct{ method, target, body string }{
		{http.MethodGet, "/widgets", ""},
		{http.MethodGet, "/widgets/" + id1, ""},
		{http.MethodGet, "/widgets/show_by/name/alpha", ""},
		{http.MethodGet, "/widgets/all_by/name/alpha", ""},
		{http.MethodPost, "/widgets", `{"name":"x"}`},
		{http.MethodPatch, "/widgets/" + id1, `{"qty":1}`},
		{http.MethodDelete, "/widgets/" + id1, ""},
	} {
		if w := serve(r, tc.method, tc.target, tc.body); w.Code != 401 {
			t.Errorf("%s %s: status %d, want 401", tc.method, tc.target, w.Code)
		}
	}
	if s.created != nil || s.deletedID != "" || s.updatedID != "" {
		t.Error("a store call got through the middleware")
	}
}
