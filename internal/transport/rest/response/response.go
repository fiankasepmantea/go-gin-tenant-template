// Package response holds the JSON shapes shared by all handlers, so every error looks the same to clients.
package response

import "github.com/gin-gonic/gin"

// ErrorBody is the error envelope: {"error": {"code": "...", "message": "..."}}.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine-readable code and a human-readable message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error writes the error envelope and stops the handler chain.
func Error(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}
