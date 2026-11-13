package observability

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func render(r *Registry) string {
	var b bytes.Buffer
	r.Write(&b)
	return b.String()
}

func TestRegistry_CountersAndHistogram(t *testing.T) {
	r := NewRegistry("v1")
	r.Observe("GET", "/api/v1/groups", 200, 20*time.Millisecond)
	r.Observe("GET", "/api/v1/groups", 200, 3*time.Second)
	r.Observe("GET", "/api/v1/groups", 500, 20*time.Second)
	out := render(r)
	for _, want := range []string{
		`app_build_info{version="v1"} 1`,
		`http_requests_total{method="GET",route="/api/v1/groups",status="200"} 2`,
		`http_requests_total{method="GET",route="/api/v1/groups",status="500"} 1`,
		`http_request_duration_seconds_bucket{method="GET",route="/api/v1/groups",le="0.025"} 1`,
		`http_request_duration_seconds_bucket{method="GET",route="/api/v1/groups",le="5"} 2`,
		`http_request_duration_seconds_bucket{method="GET",route="/api/v1/groups",le="+Inf"} 3`,
		`http_request_duration_seconds_count{method="GET",route="/api/v1/groups"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRegistry_LabelValuesAreEscaped(t *testing.T) {
	r := NewRegistry(`a"b`)
	r.Observe("GET", "x\ny\\", 200, time.Millisecond)
	out := render(r)
	if !strings.Contains(out, `version="a\"b"`) || !strings.Contains(out, `route="x\ny\\"`) {
		t.Fatalf("not escaped:\n%s", out)
	}
}

func TestRegistry_SeriesAreBounded(t *testing.T) {
	r := NewRegistry("v")
	for i := 0; i < maxSeries+50; i++ {
		r.Observe("GET", "/r"+string(rune('a'+i%26))+strings.Repeat("x", i), 200, time.Millisecond)
	}
	if len(r.requests) > maxSeries {
		t.Fatalf("series: %d", len(r.requests))
	}
	if !strings.Contains(render(r), "http_metrics_dropped_total 50") {
		t.Fatal("dropped observations must be counted")
	}
}

func TestRegistry_Pools(t *testing.T) {
	r := NewRegistry("v")
	r.AddPool("shared", func() sql.DBStats { return sql.DBStats{OpenConnections: 4, InUse: 1, Idle: 3} })
	out := render(r)
	for _, want := range []string{`db_pool_open_connections{db="shared"} 4`, `db_pool_in_use_connections{db="shared"} 1`, `db_pool_idle_connections{db="shared"} 3`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestMiddlewareAndHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := NewRegistry("v")
	e := gin.New()
	e.Use(Middleware(reg))
	e.GET("/things/:id", func(c *gin.Context) { c.Status(http.StatusTeapot) })
	e.GET("/metrics", Handler(reg, "s3cret"))

	do := func(method, path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		return w
	}
	do("GET", "/things/123", "")
	do("GET", "/things/456", "")
	do("GET", "/nope", "")

	if w := do("GET", "/metrics", ""); w.Code != 404 {
		t.Fatalf("without token: %d", w.Code)
	}
	if w := do("GET", "/metrics", "s3cret"); w.Code != 404 {
		t.Fatalf("a token without Bearer: %d", w.Code)
	}
	w := do("GET", "/metrics", "Bearer s3cret")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("with token: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	// the route pattern, not the raw path: two ids make one series
	if !strings.Contains(body, `route="/things/:id",status="418"} 2`) || strings.Contains(body, "/things/123") {
		t.Fatalf("route labels:\n%s", body)
	}
	if !strings.Contains(body, `route="unmatched",status="404"} 1`) {
		t.Fatalf("unmatched:\n%s", body)
	}
	if !strings.Contains(body, "http_requests_in_flight 1") { // the scrape itself
		t.Fatalf("in flight:\n%s", body)
	}
}
