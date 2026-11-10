// Package response holds the JSON shapes shared by all handlers, so every response looks the same to clients.
//
// Success: {"success": true, "code": 200, "data": ..., "message": "..."}
// Error:   {"success": false, "code": 404, "error": {"code": "not_found", "message": "...", "details": [...]}}
//
// "code" at the top level is the HTTP status; error.code is the stable machine-readable code from the catalog.
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// SuccessBody is the success envelope.
type SuccessBody struct {
	Success bool   `json:"success"`
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data"`
}

// ErrorBody is the error envelope.
type ErrorBody struct {
	Success bool        `json:"success"`
	Code    int         `json:"code"`
	Error   ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine-readable code and a human-readable message.
type ErrorDetail struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Details []FieldError `json:"details,omitempty"`
}

// FieldError describes one invalid field of a request body.
type FieldError struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
}

// OK writes 200 with data.
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, SuccessBody{Success: true, Code: http.StatusOK, Data: data})
}

// Created writes 201 with the created record.
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, SuccessBody{Success: true, Code: http.StatusCreated, Data: data})
}

// Message writes 200 with a message and no data (e.g. after a delete).
func Message(c *gin.Context, message string) {
	c.JSON(http.StatusOK, SuccessBody{Success: true, Code: http.StatusOK, Message: message})
}

// Pagination is the data of a paginated list. The field names are kept compatible with the earlier CI4 template.
type Pagination struct {
	TotalData   int64 `json:"total_data"`
	TotalPages  int   `json:"total_pages"`
	CurrPage    int   `json:"curr_page"`
	NextPage    int   `json:"next_page"`
	PrevPage    *int  `json:"prev_page,omitempty"` // absent on the first page
	DataPerPage int   `json:"data_per_page"`
	Data        any   `json:"data"`
}

// NewPagination computes the page numbers. On the last page next_page stays on the last page (as in CI4).
func NewPagination(items any, total int64, page, perPage int) Pagination {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 1
	}
	totalPages := int((total + int64(perPage) - 1) / int64(perPage))
	p := Pagination{
		TotalData:   total,
		TotalPages:  totalPages,
		CurrPage:    page,
		NextPage:    page + 1,
		DataPerPage: perPage,
		Data:        items,
	}
	if page >= totalPages {
		p.NextPage = totalPages
	}
	if page > 1 {
		prev := page - 1
		p.PrevPage = &prev
	}
	return p
}

// Paginated writes 200 with a Pagination.
func Paginated(c *gin.Context, items any, total int64, page, perPage int) {
	OK(c, NewPagination(items, total, page, perPage))
}

// Abort writes the error envelope for code (status and default message come from the catalog) and stops the
// handler chain. An empty message means the default message.
func Abort(c *gin.Context, code Code, message string) {
	abort(c, &Error{Code: code, Message: message})
}

func abort(c *gin.Context, e *Error) {
	msg := e.Message
	if msg == "" {
		msg = e.Code.DefaultMessage()
	}
	status := e.Code.Status()
	c.AbortWithStatusJSON(status, ErrorBody{
		Code:  status,
		Error: ErrorDetail{Code: string(e.Code), Message: msg, Details: e.Details},
	})
}
