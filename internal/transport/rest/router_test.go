package rest

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
)

func testRouter() http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(&config.Config{Env: "testing"}, log, nil)
}

func TestHealthz(t *testing.T) {
	w := httptest.NewRecorder()
	testRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Error("response must contain X-Request-ID")
	}
}

func TestReadyz_WithoutDatabase(t *testing.T) {
	w := httptest.NewRecorder()
	testRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the database is absent", w.Code)
	}
}
