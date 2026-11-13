package response

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

func init() { gin.SetMode(gin.TestMode) }

var allCodes = []Code{
	CodeInvalidRequest, CodeUnauthorized, CodeInvalidCredentials, CodeInvalidRefreshToken, CodeTokenExpired,
	CodeForbidden, CodeNotFound, CodeMethodNotAllowed, CodeConflict, CodePayloadTooLarge, CodeValidationFailed,
	CodeRateLimited, CodeInternal, CodeUnavailable,
}

func TestCatalogIsComplete(t *testing.T) {
	seen := map[int]Code{}
	for _, c := range allCodes {
		if _, ok := statusOf[c]; !ok {
			t.Errorf("%s has no HTTP status", c)
		}
		if defaultMessage[c] == "" {
			t.Errorf("%s has no default message", c)
		}
		if strings.ToLower(string(c)) != string(c) || strings.Contains(string(c), " ") {
			t.Errorf("%q must be lower snake_case", c)
		}
		seen[c.Status()] = c
	}
	if len(statusOf) != len(allCodes) || len(defaultMessage) != len(allCodes) {
		t.Errorf("catalog tables (%d, %d) and the code list (%d) are out of sync",
			len(statusOf), len(defaultMessage), len(allCodes))
	}
	if got := Code("made_up").Status(); got != http.StatusInternalServerError {
		t.Errorf("unknown code status = %d, want 500", got)
	}
}

func TestNewPagination(t *testing.T) {
	tests := []struct {
		name             string
		total            int64
		page, perPage    int
		totalPages, next int
		prev             *int
	}{
		{"empty", 0, 1, 20, 0, 0, nil},
		{"first of three", 45, 1, 20, 3, 2, nil},
		{"middle", 45, 2, 20, 3, 3, ptr(1)},
		{"last stays on last", 45, 3, 20, 3, 3, ptr(2)},
		{"exact multiple", 40, 2, 20, 2, 2, ptr(1)},
		{"page beyond the end", 5, 9, 20, 1, 1, ptr(8)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPagination([]int{}, tt.total, tt.page, tt.perPage)
			if p.TotalPages != tt.totalPages || p.NextPage != tt.next || p.CurrPage != tt.page || p.DataPerPage != tt.perPage {
				t.Errorf("got %+v", p)
			}
			if (p.PrevPage == nil) != (tt.prev == nil) || (tt.prev != nil && *p.PrevPage != *tt.prev) {
				t.Errorf("prev = %v, want %v", p.PrevPage, tt.prev)
			}
		})
	}

	b, _ := json.Marshal(NewPagination([]int{1}, 1, 1, 20))
	if strings.Contains(string(b), "prev_page") {
		t.Errorf("prev_page must be absent on the first page: %s", b)
	}
	for _, k := range []string{"total_data", "total_pages", "curr_page", "next_page", "data_per_page", `"data"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
}

func ptr(i int) *int { return &i }

func run(h gin.HandlerFunc) *httptest.ResponseRecorder {
	r := gin.New()
	r.GET("/x", h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil))
	return w
}

func TestSuccessEnvelopes(t *testing.T) {
	w := run(func(c *gin.Context) { OK(c, gin.H{"a": 1}) })
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"success":true`) || !strings.Contains(w.Body.String(), `"code":200`) {
		t.Errorf("OK: %d %s", w.Code, w.Body)
	}
	w = run(func(c *gin.Context) { Created(c, gin.H{"a": 1}) })
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"code":201`) {
		t.Errorf("Created: %d %s", w.Code, w.Body)
	}
	w = run(func(c *gin.Context) { Message(c, "Deleted.") })
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"message":"Deleted."`) {
		t.Errorf("Message: %d %s", w.Code, w.Body)
	}
}

func TestFail(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   Code
		leak       string // text that must never reach the client
	}{
		{"not found", core.ErrNotFound, 404, CodeNotFound, ""},
		{"wrapped not found", fmt.Errorf("groups: get: %w", core.ErrNotFound), 404, CodeNotFound, "groups"},
		{"conflict", core.ErrConflict, 409, CodeConflict, ""},
		{"reference", core.ErrReference, 422, CodeValidationFailed, ""},
		{"no tenant", core.ErrNoTenant, 403, CodeForbidden, ""},
		{"no fields", core.ErrNoFields, 400, CodeInvalidRequest, ""},
		{"protected field", fmt.Errorf("%w: tenant_id", core.ErrProtectedField), 400, CodeInvalidRequest, "tenant_id"},
		{"invalid column", core.ErrInvalidColumn, 400, CodeInvalidRequest, ""},
		{"invalid input", core.ErrInvalidInput, 400, CodeInvalidRequest, ""},
		{"custom error", NewError(CodeRateLimited, "slow down"), 429, CodeRateLimited, ""},
		{"wrapped custom error", fmt.Errorf("x: %w", NewError(CodeForbidden, "")), 403, CodeForbidden, ""},
		{"max bytes", &http.MaxBytesError{Limit: 10}, 413, CodePayloadTooLarge, ""},
		{"json syntax", &json.SyntaxError{}, 400, CodeInvalidRequest, ""},
		{"json type", &json.UnmarshalTypeError{Field: "x"}, 400, CodeInvalidRequest, ""},
		{"unexpected", errors.New("pq: authentication failed for user internal_db_user"), 500, CodeInternal, "internal_db_user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := run(func(c *gin.Context) { Fail(c, tt.err) })
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.wantStatus, w.Body)
			}
			var body ErrorBody
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Success || body.Code != tt.wantStatus || body.Error.Code != string(tt.wantCode) || body.Error.Message == "" {
				t.Errorf("body = %+v", body)
			}
			if tt.leak != "" && strings.Contains(w.Body.String(), tt.leak) {
				t.Errorf("response leaks %q: %s", tt.leak, w.Body)
			}
		})
	}
}

func TestFailUnexpectedErrorIsAttachedForTheLog(t *testing.T) {
	r := gin.New()
	var attached int
	r.Use(func(c *gin.Context) { c.Next(); attached = len(c.Errors) })
	r.GET("/x", func(c *gin.Context) { Fail(c, errors.New("boom")) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil))
	if attached != 1 {
		t.Errorf("c.Errors has %d entries, want 1", attached)
	}
}

func TestFailStopsTheChain(t *testing.T) {
	r := gin.New()
	called := false
	r.GET("/x", func(c *gin.Context) { Fail(c, core.ErrNotFound) }, func(c *gin.Context) { called = true })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil))
	if called {
		t.Error("handlers after Fail must not run")
	}
}

func TestValidationDetailsUseJSONNames(t *testing.T) {
	type in struct {
		Price    int    `json:"price" binding:"required,gt=0"`
		UnitName string `json:"unit_name,omitempty" binding:"required"`
		NoTag    string `binding:"required"`
	}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	var body in
	err := c.ShouldBindJSON(&body)
	e := classify(err, c)
	var got []string
	for _, d := range e.Details {
		got = append(got, d.Field)
	}
	want := "price,unit_name,NoTag"
	if strings.Join(got, ",") != want {
		t.Fatalf("fields = %v, want %s", got, want)
	}
}
