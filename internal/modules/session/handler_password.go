package session

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// accountService is what the handler needs from Account (and what tests replace).
type accountService interface {
	RequestReset(ctx context.Context, in ResetRequest) error
	ResetPassword(ctx context.Context, token, newPassword, ip string) error
	ChangePassword(ctx context.Context, in ChangeInput) error
}

// WithAccount enables the password endpoints:
//
//	POST /auth/password/forgot            {tenant_code?, identifier}  always 202
//	POST /auth/platform/password/forgot   {identifier}                always 202
//	POST /auth/password/reset             {token, new_password}       204
//	POST /auth/password/change            {current_password, new_password}  204, needs a token
//
// Call it before RegisterRoutes. Without it these routes do not exist.
func (h *Handler) WithAccount(a accountService) *Handler {
	h.account = a
	return h
}

func (h *Handler) registerPassword(g *gin.RouterGroup) {
	if h.account == nil {
		return
	}
	g.POST("/password/forgot", h.forgot(false))
	g.POST("/platform/password/forgot", h.forgot(true))
	g.POST("/password/reset", h.resetPassword)
	g.POST("/password/change", h.authenticate, h.changePassword)
}

type forgotRequest struct {
	TenantCode string `json:"tenant_code" binding:"max=63"`
	Identifier string `json:"identifier" binding:"required,max=255"`
}

type resetRequest struct {
	Token       string `json:"token" binding:"required,max=200"`
	NewPassword string `json:"new_password" binding:"required,max=1024"`
}

type changeRequest struct {
	CurrentPassword string `json:"current_password" binding:"required,max=1024"`
	NewPassword     string `json:"new_password" binding:"required,max=1024"`
}

// forgot always answers 202 with the same message, whether or not the account exists.
func (h *Handler) forgot(platform bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req forgotRequest
		if !bind(c, &req) {
			return
		}
		code := req.TenantCode
		if !platform && h.resolver != nil {
			if resolved, ok := h.resolver.Resolve(c.Request); ok {
				code = resolved
			}
		}
		err := h.account.RequestReset(c.Request.Context(), ResetRequest{
			TenantCode: code, Platform: platform, Identifier: req.Identifier, IP: c.ClientIP(),
		})
		if err != nil {
			h.fail(c, err)
			return
		}
		c.JSON(http.StatusAccepted, response.SuccessBody{
			Success: true, Code: http.StatusAccepted,
			Message: "If the account exists, a message with instructions was sent to its e-mail address.",
		})
	}
}

func (h *Handler) resetPassword(c *gin.Context) {
	var req resetRequest
	if !bind(c, &req) {
		return
	}
	if err := h.account.ResetPassword(c.Request.Context(), strings.TrimSpace(req.Token), req.NewPassword, c.ClientIP()); err != nil {
		h.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) changePassword(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		response.Abort(c, response.CodeUnauthorized, "Authentication is required.")
		return
	}
	var req changeRequest
	if !bind(c, &req) {
		return
	}
	err := h.account.ChangePassword(c.Request.Context(), ChangeInput{
		UserID: claims.UserID, TenantID: claims.TenantID, Level: claims.Level,
		Current: req.CurrentPassword, New: req.NewPassword, IP: c.ClientIP(),
	})
	if err != nil {
		h.fail(c, err)
		return
	}
	// every session of the user ended, including this one: the client logs in again
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

// fail maps the errors of the password flows to the response catalog.
func (h *Handler) fail(c *gin.Context, err error) {
	switch {
	case isRateLimited(c, err):
	case errors.Is(err, ErrInvalidResetToken):
		response.Abort(c, response.CodeInvalidRequest, "The reset link is invalid or has expired. Please ask for a new one.")
	case errors.Is(err, ErrWrongPassword):
		response.Fail(c, &response.Error{
			Code: response.CodeValidationFailed, Message: "The current password is not correct.",
			Details: []response.FieldError{{Field: "current_password", Rule: "incorrect"}},
		})
	case errors.Is(err, ErrWeakPassword):
		response.Fail(c, &response.Error{
			Code: response.CodeValidationFailed, Message: weakMessage(err),
			Details: []response.FieldError{{Field: "new_password", Rule: weakRule(err)}},
		})
	case errors.Is(err, ErrAccountUnavailable):
		response.Abort(c, response.CodeUnauthorized, "Authentication is required.")
	default:
		h.log.Error("password request failed", "path", c.FullPath(), "error", err)
		response.Abort(c, response.CodeInternal, "Something went wrong.")
	}
}

func weakRule(err error) string {
	switch {
	case errors.Is(err, password.ErrTooShort):
		return "min"
	case errors.Is(err, password.ErrTooLong):
		return "max"
	default:
		return "different"
	}
}

func weakMessage(err error) string {
	switch {
	case errors.Is(err, password.ErrTooShort):
		return "The new password is too short."
	case errors.Is(err, password.ErrTooLong):
		return "The new password is too long."
	default:
		return "The new password must differ from the current one."
	}
}
