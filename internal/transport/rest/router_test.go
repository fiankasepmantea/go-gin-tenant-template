package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
)

func testRouter() http.Handler {
	log := slog.New(slog.DiscardHandler)
	return NewRouter(&config.Config{Env: "testing"}, log, nil)
}

func TestHealthz(t *testing.T) {
	w := httptest.NewRecorder()
	testRouter().ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Error("response must contain X-Request-ID")
	}
}

func TestReadyz_WithoutDatabase(t *testing.T) {
	w := httptest.NewRecorder()
	testRouter().ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the database is absent", w.Code)
	}
}

type probeModule struct{}

func (probeModule) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/probe", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	rg.GET("/boom", func(c *gin.Context) { panic("secret internal detail") })
}

func TestErrorsUseTheSameEnvelope(t *testing.T) {
	r := NewRouter(&config.Config{Env: "testing"}, slog.New(slog.DiscardHandler), nil, probeModule{})

	tests := []struct {
		name, method, target string
		wantStatus           int
		wantCode             string
	}{
		{"unknown route", http.MethodGet, "/api/v1/nothing", http.StatusNotFound, "not_found"},
		{"unknown root route", http.MethodGet, "/nothing", http.StatusNotFound, "not_found"},
		{"wrong method", http.MethodPost, "/api/v1/probe", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"panic in a handler", http.MethodGet, "/api/v1/boom", http.StatusInternalServerError, "internal_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), tt.method, tt.target, nil))
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.wantStatus, w.Body)
			}
			var body struct {
				Success bool `json:"success"`
				Code    int  `json:"code"`
				Error   struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v: %s", err, w.Body)
			}
			if body.Success || body.Code != tt.wantStatus || body.Error.Code != tt.wantCode || body.Error.Message == "" {
				t.Errorf("body = %+v", body)
			}
			if strings.Contains(w.Body.String(), "secret internal detail") {
				t.Error("a panic message reached the client")
			}
			if w.Header().Get("X-Request-ID") == "" {
				t.Error("error responses must carry X-Request-ID too")
			}
		})
	}
}
