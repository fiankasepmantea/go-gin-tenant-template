package session

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// maxBody limits the size of a request body. Auth requests are tiny.
const maxBody = 16 << 10

// authService is what the handler needs from Service (and what tests replace).
type authService interface {
	Login(ctx context.Context, in LoginInput) (*Tokens, error)
	LoginPlatform(ctx context.Context, in LoginInput) (*Tokens, error)
	Refresh(ctx context.Context, refreshToken, userAgent, ip string) (*Tokens, error)
	Logout(ctx context.Context, refreshToken string) error
}

// Handler serves the /auth endpoints.
type Handler struct {
	svc          authService
	resolver     tenant.Resolver
	authenticate gin.HandlerFunc
	log          *slog.Logger
}

// NewHandler creates the handler. resolver finds the tenant code from the subdomain or header (it may be nil);
// the tenant_code field in the body is the last fallback. authenticate protects /auth/me.
func NewHandler(svc authService, resolver tenant.Resolver, authenticate gin.HandlerFunc, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Handler{svc: svc, resolver: resolver, authenticate: authenticate, log: log}
}

// RegisterRoutes registers the endpoints under the given group (normally /api/v1).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/auth")
	g.POST("/login", h.login)
	g.POST("/platform/login", h.loginPlatform)
	g.POST("/refresh", h.refresh)
	g.POST("/logout", h.logout)
	g.GET("/me", h.authenticate, h.me)
}

type loginRequest struct {
	TenantCode string `json:"tenant_code" binding:"max=63"`
	Username   string `json:"username" binding:"required,max=255"`
	Password   string `json:"password" binding:"required,max=1024"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required,max=200"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) login(c *gin.Context) {
	var req loginRequest
	if !bind(c, &req) {
		return
	}
	code := req.TenantCode
	if h.resolver != nil {
		if resolved, ok := h.resolver.Resolve(c.Request); ok {
			code = resolved // subdomain / header wins over the body
		}
	}
	tok, err := h.svc.Login(c.Request.Context(), LoginInput{
		TenantCode: code, Username: req.Username, Password: req.Password,
		UserAgent: c.Request.UserAgent(), IP: c.ClientIP(),
	})
	h.respond(c, tok, err)
}

func (h *Handler) loginPlatform(c *gin.Context) {
	var req loginRequest
	if !bind(c, &req) {
		return
	}
	tok, err := h.svc.LoginPlatform(c.Request.Context(), LoginInput{
		Username: req.Username, Password: req.Password,
		UserAgent: c.Request.UserAgent(), IP: c.ClientIP(),
	})
	h.respond(c, tok, err)
}

func (h *Handler) refresh(c *gin.Context) {
	var req refreshRequest
	if !bind(c, &req) {
		return
	}
	tok, err := h.svc.Refresh(c.Request.Context(), strings.TrimSpace(req.RefreshToken), c.Request.UserAgent(), c.ClientIP())
	h.respond(c, tok, err)
}

func (h *Handler) logout(c *gin.Context) {
	var req refreshRequest
	if !bind(c, &req) {
		return
	}
	if err := h.svc.Logout(c.Request.Context(), strings.TrimSpace(req.RefreshToken)); err != nil {
		h.log.Error("logout failed", "error", err)
		response.Error(c, http.StatusInternalServerError, "internal_error", "Something went wrong.")
		return
	}
	c.Status(http.StatusNoContent)
}

// me returns who the access token belongs to. It reads the token only, no database access.
func (h *Handler) me(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "unauthorized", "Authentication is required.")
		return
	}
	body := gin.H{"user_id": claims.UserID, "level": claims.Level}
	if claims.TenantID != "" {
		body["tenant_id"] = claims.TenantID
	}
	c.JSON(http.StatusOK, body)
}

func (h *Handler) respond(c *gin.Context, tok *Tokens, err error) {
	switch {
	case err == nil:
		// Tokens must never be cached by browsers or proxies.
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.JSON(http.StatusOK, tokenResponse{
			AccessToken:  tok.AccessToken,
			TokenType:    "Bearer",
			ExpiresIn:    int64(tok.ExpiresIn.Seconds()),
			RefreshToken: tok.RefreshToken,
		})
	case errors.Is(err, ErrInvalidCredentials):
		response.Error(c, http.StatusUnauthorized, "invalid_credentials", "Invalid tenant, username or password.")
	case errors.Is(err, ErrInvalidRefreshToken):
		response.Error(c, http.StatusUnauthorized, "invalid_refresh_token", "The refresh token is invalid or expired. Please log in again.")
	default:
		h.log.Error("auth request failed", "path", c.FullPath(), "error", err)
		response.Error(c, http.StatusInternalServerError, "internal_error", "Something went wrong.")
	}
}

// bind reads a small JSON body. It writes the 400 response itself and reports whether the caller may continue.
func bind(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
	if err := c.ShouldBindJSON(dst); err != nil {
		response.Error(c, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return false
	}
	return true
}
