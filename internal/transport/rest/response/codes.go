package response

import "net/http"

// Code is a stable, machine-readable error code. Clients branch on the code, never on the message text.
// New codes may be added; existing codes and their HTTP status never change.
type Code string

const (
	CodeInvalidRequest      Code = "invalid_request"       // 400: malformed body, unknown or invalid query parameter
	CodeUnauthorized        Code = "unauthorized"          // 401: missing or invalid access token
	CodeInvalidCredentials  Code = "invalid_credentials"   //nolint:gosec // an error code name, not a credential. 401: wrong tenant, username or password
	CodeInvalidRefreshToken Code = "invalid_refresh_token" // 401: refresh token unknown, expired, or already used
	CodeTokenExpired        Code = "token_expired"         // 401: access token expired; refresh it
	CodeForbidden           Code = "forbidden"             // 403: authenticated, but not allowed
	CodeNotFound            Code = "not_found"             // 404: no such record or route (also for other tenants' records)
	CodeMethodNotAllowed    Code = "method_not_allowed"    // 405
	CodeConflict            Code = "conflict"              // 409: the record already exists
	CodePayloadTooLarge     Code = "payload_too_large"     // 413
	CodeValidationFailed    Code = "validation_failed"     // 422: well-formed, but a field value is not acceptable
	CodeRateLimited         Code = "rate_limited"          // 429
	CodeInternal            Code = "internal_error"        // 500: unexpected; details are in the server log only
	CodeUnavailable         Code = "service_unavailable"   // 503
)

var statusOf = map[Code]int{
	CodeInvalidRequest:      http.StatusBadRequest,
	CodeUnauthorized:        http.StatusUnauthorized,
	CodeInvalidCredentials:  http.StatusUnauthorized,
	CodeInvalidRefreshToken: http.StatusUnauthorized,
	CodeTokenExpired:        http.StatusUnauthorized,
	CodeForbidden:           http.StatusForbidden,
	CodeNotFound:            http.StatusNotFound,
	CodeMethodNotAllowed:    http.StatusMethodNotAllowed,
	CodeConflict:            http.StatusConflict,
	CodePayloadTooLarge:     http.StatusRequestEntityTooLarge,
	CodeValidationFailed:    http.StatusUnprocessableEntity,
	CodeRateLimited:         http.StatusTooManyRequests,
	CodeInternal:            http.StatusInternalServerError,
	CodeUnavailable:         http.StatusServiceUnavailable,
}

var defaultMessage = map[Code]string{
	CodeInvalidRequest:      "The request is invalid.",
	CodeUnauthorized:        "Authentication is required.",
	CodeInvalidCredentials:  "Invalid tenant, username or password.",
	CodeInvalidRefreshToken: "The refresh token is invalid or expired. Please log in again.",
	CodeTokenExpired:        "The access token has expired.",
	CodeForbidden:           "You are not allowed to access this resource.",
	CodeNotFound:            "The requested resource was not found.",
	CodeMethodNotAllowed:    "This method is not allowed for the resource.",
	CodeConflict:            "The record already exists.",
	CodePayloadTooLarge:     "The request body is too large.",
	CodeValidationFailed:    "One or more fields are not valid.",
	CodeRateLimited:         "Too many requests. Please try again later.",
	CodeInternal:            "Something went wrong.",
	CodeUnavailable:         "The service is temporarily unavailable.",
}

// Status returns the HTTP status of the code (500 for a code that is not in the catalog).
func (c Code) Status() int {
	if s, ok := statusOf[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// DefaultMessage returns the standard message of the code.
func (c Code) DefaultMessage() string {
	if m, ok := defaultMessage[c]; ok {
		return m
	}
	return defaultMessage[CodeInternal]
}

// Error is an error that already knows how it must be presented. Services and handlers can return it
// (or wrap it with %w) and Fail will use the code, message and details as they are.
type Error struct {
	Code    Code
	Message string       // empty = the default message of the code
	Details []FieldError // optional, for validation_failed
}

func (e *Error) Error() string {
	if e.Message != "" {
		return string(e.Code) + ": " + e.Message
	}
	return string(e.Code)
}

// NewError creates an *Error. An empty message means the default message of the code.
func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}
