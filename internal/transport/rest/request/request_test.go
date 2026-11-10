package request

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

func init() { gin.SetMode(gin.TestMode) }

func ctxFor(target string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	return c
}

var policy = ListPolicy{Filterable: []string{"name", "group_id"}, Sortable: []string{"name", "created_at"}}

func TestParseList(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	tests := []struct {
		name   string
		query  string
		want   core.ListParams
		errMsg string // substring of the error message; empty = success
	}{
		{name: "empty", query: "", want: core.ListParams{}},
		{name: "paging", query: "?page=2&per_page=50", want: core.ListParams{Page: 2, PerPage: 50}},
		{name: "sort", query: "?sort=-created_at,name", want: core.ListParams{Sort: []core.Sort{{Column: "created_at", Desc: true}, {Column: "name"}}}},
		{name: "filters sorted by column", query: "?name=a&group_id=b",
			want: core.ListParams{Filters: []core.Filter{{Column: "group_id", Value: "b"}, {Column: "name", Value: "a"}}}},
		{name: "date range inclusive end", query: "?from=2026-01-01&to=2026-01-31",
			want: core.ListParams{CreatedFrom: d("2026-01-01"), CreatedBefore: d("2026-02-01")}},
		{name: "timestamp range", query: "?from=2026-01-01T00:00:00Z&to=2026-01-01T12:00:00Z",
			want: core.ListParams{CreatedFrom: d("2026-01-01"), CreatedBefore: d("2026-01-01").Add(12*time.Hour + time.Microsecond)}},

		{name: "unknown parameter", query: "?nam=a", errMsg: `Unknown query parameter "nam"`},
		{name: "column not exposed", query: "?tenant_id=x", errMsg: "Unknown query parameter"},
		{name: "repeated parameter", query: "?name=a&name=b", errMsg: "must be given once"},
		{name: "page zero", query: "?page=0", errMsg: "positive integer"},
		{name: "page text", query: "?page=abc", errMsg: "positive integer"},
		{name: "per_page negative", query: "?per_page=-5", errMsg: "positive integer"},
		{name: "sort not exposed", query: "?sort=password_hash", errMsg: `Cannot sort by "password_hash"`},
		{name: "sort injection", query: "?sort=name,password_hash", errMsg: "Cannot sort by"},
		{name: "semicolon is not silently dropped", query: "?name=a;b", errMsg: "malformed"},
		{name: "bad from", query: "?from=yesterday", errMsg: `"from"`},
		{name: "bad to", query: "?to=31-01-2026", errMsg: `"to"`},
		{name: "reversed range", query: "?from=2026-02-01&to=2026-01-01", errMsg: "before"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseList(ctxFor("/things"+tt.query), policy)
			if tt.errMsg != "" {
				var apiErr *response.Error
				ok := errors.As(err, &apiErr)
				if !ok || apiErr.Code != response.CodeInvalidRequest || !strings.Contains(apiErr.Message, tt.errMsg) {
					t.Fatalf("err = %#v, want invalid_request containing %q", err, tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !equalParams(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func equalParams(a, b core.ListParams) bool {
	if a.Page != b.Page || a.PerPage != b.PerPage || !a.CreatedFrom.Equal(b.CreatedFrom) || !a.CreatedBefore.Equal(b.CreatedBefore) {
		return false
	}
	if len(a.Filters) != len(b.Filters) || len(a.Sort) != len(b.Sort) {
		return false
	}
	for i := range a.Filters {
		if a.Filters[i] != b.Filters[i] {
			return false
		}
	}
	for i := range a.Sort {
		if a.Sort[i] != b.Sort[i] {
			return false
		}
	}
	return true
}

func TestUUIDParam(t *testing.T) {
	for _, tt := range []struct {
		val string
		ok  bool
	}{
		{"0198f6a0-1c2d-7abc-8def-0123456789ab", true},
		{"not-a-uuid", false},
		{"1", false},
		{"show_by", false},
		{"' OR 1=1 --", false},
	} {
		w := httptest.NewRecorder()
		r := gin.New()
		var got bool
		r.GET("/things/:id", func(c *gin.Context) { _, got = UUIDParam(c, "id") })
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/things/"+strings.ReplaceAll(tt.val, " ", "%20"), nil))
		if got != tt.ok {
			t.Errorf("UUIDParam(%q) = %v, want %v", tt.val, got, tt.ok)
		}
		if !tt.ok && w.Code != http.StatusNotFound {
			t.Errorf("UUIDParam(%q): status %d, want 404", tt.val, w.Code)
		}
	}
}

func TestBindJSON(t *testing.T) {
	type in struct {
		Name string `json:"name" binding:"required,max=5"`
	}
	tests := []struct {
		name       string
		body       string
		max        int64
		wantOK     bool
		wantStatus int
		wantCode   string
	}{
		{"valid", `{"name":"abc"}`, 0, true, 200, ""},
		{"empty body", ``, 0, false, 400, "invalid_request"},
		{"not json", `{nope`, 0, false, 400, "invalid_request"},
		{"wrong type", `{"name":5}`, 0, false, 400, "invalid_request"},
		{"missing required", `{}`, 0, false, 422, "validation_failed"},
		{"too long", `{"name":"abcdefgh"}`, 0, false, 422, "validation_failed"},
		{"body too large", `{"name":"` + strings.Repeat("a", 100) + `"}`, 50, false, 413, "payload_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := gin.New()
			var ok bool
			r.POST("/x", func(c *gin.Context) {
				var v in
				if ok = BindJSON(c, &v, tt.max); ok {
					c.Status(http.StatusOK)
				}
			})
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if ok != tt.wantOK || w.Code != tt.wantStatus {
				t.Fatalf("ok=%v status=%d body=%s", ok, w.Code, w.Body)
			}
			if tt.wantCode != "" && !strings.Contains(w.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Errorf("body = %s, want code %s", w.Body, tt.wantCode)
			}
		})
	}
}
