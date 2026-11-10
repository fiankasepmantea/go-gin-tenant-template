package response

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

// Fail turns any error into the matching error response and stops the handler chain.
// It is the single place that decides which error becomes which status and code:
//
//	*Error (or wrapped)             its own code, message and details
//	core.ErrNotFound                404 not_found
//	core.ErrConflict                409 conflict
//	core.ErrReference               422 validation_failed
//	core.ErrNoTenant                403 forbidden (a platform token on a tenant resource)
//	other core.Err*                 400 invalid_request
//	validation / JSON / body size   422 validation_failed, 400 invalid_request, 413 payload_too_large
//	anything else                   500 internal_error with a generic message
//
// An unexpected error is attached to the Gin context (c.Error) so the access log can record it; its text is
// never sent to the client.
func Fail(c *gin.Context, err error) {
	abort(c, classify(err, c))
}

func classify(err error, c *gin.Context) *Error {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, core.ErrNotFound):
		return &Error{Code: CodeNotFound}
	case errors.Is(err, core.ErrConflict):
		return &Error{Code: CodeConflict}
	case errors.Is(err, core.ErrReference):
		return &Error{Code: CodeValidationFailed, Message: "A referenced record does not exist."}
	case errors.Is(err, core.ErrNoTenant):
		return &Error{Code: CodeForbidden, Message: "A tenant context is required for this resource."}
	case errors.Is(err, core.ErrNoFields):
		return &Error{Code: CodeInvalidRequest, Message: "No fields to update."}
	case errors.Is(err, core.ErrProtectedField),
		errors.Is(err, core.ErrInvalidColumn),
		errors.Is(err, core.ErrInvalidInput):
		return &Error{Code: CodeInvalidRequest}
	}

	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return &Error{Code: CodePayloadTooLarge}
	}

	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		details := make([]FieldError, 0, len(verrs))
		for _, fe := range verrs {
			details = append(details, FieldError{Field: fe.Field(), Rule: fe.Tag()})
		}
		return &Error{Code: CodeValidationFailed, Details: details}
	}

	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntaxErr), errors.As(err, &typeErr),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &Error{Code: CodeInvalidRequest, Message: "The request body is not valid JSON."}
	}

	if c != nil {
		_ = c.Error(err) // for the access log; never shown to the client
	}
	return &Error{Code: CodeInternal}
}
