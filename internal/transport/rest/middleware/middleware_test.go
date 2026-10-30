package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newEngine(mw ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw...)
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, RequestIDFrom(c)) })
	r.GET("/boom", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	return r
}

func TestRequestID_GeneratesAndEchoes(t *testing.T) {
	r := newEngine(RequestID())

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
	id := w.Header().Get(HeaderRequestID)
	if id == "" || w.Body.String() != id {
		t.Fatalf("header=%q body=%q", id, w.Body.String())
	}

	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(HeaderRequestID, "from-client")
	r.ServeHTTP(w, req)
	if got := w.Header().Get(HeaderRequestID); got != "from-client" {
		t.Errorf("the client request id must be used, got %q", got)
	}
}

func TestRequestID_RejectsOversized(t *testing.T) {
	r := newEngine(RequestID())
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(HeaderRequestID, strings.Repeat("x", 500))
	r.ServeHTTP(w, req)
	if got := w.Header().Get(HeaderRequestID); len(got) > 128 {
		t.Errorf("oversized request id was used as-is: %d", len(got))
	}
}

func TestAccessLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	r := newEngine(RequestID(), AccessLog(log))

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))

	out := buf.String()
	for _, want := range []string{`"route":"/ping"`, `"level":"ERROR"`, `"status":500`, `"route":"unmatched"`, `"level":"WARN"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log does not contain %s:\n%s", want, out)
		}
	}
}
