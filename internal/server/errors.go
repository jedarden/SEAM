package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ErrorCode represents a unique error code identifier
type ErrorCode string

// Error codes taxonomy
const (
	// Client errors (4xx)
	ErrCodeBadRequest        ErrorCode = "bad_request"
	ErrCodeUnauthorized      ErrorCode = "unauthorized"
	ErrCodeForbidden         ErrorCode = "forbidden"
	ErrCodeNotFound          ErrorCode = "not_found"
	ErrCodeMethodNotAllowed  ErrorCode = "method_not_allowed"
	ErrCodeInvalidVersion    ErrorCode = "invalid_version_parameter"
	ErrCodeMissingParameter  ErrorCode = "missing_required_parameter"
	ErrCodeInvalidPayload    ErrorCode = "invalid_payload"
	ErrCodeRouteNotFound     ErrorCode = "route_not_found"
	ErrCodeValidationError   ErrorCode = "validation_error"
	ErrCodeQuotaExceeded     ErrorCode = "quota_exceeded"
	ErrCodeRateLimitExceeded ErrorCode = "rate_limit_exceeded"

	// Server errors (5xx)
	ErrCodeInternalServer     ErrorCode = "internal_server_error"
	ErrCodeBadGateway         ErrorCode = "bad_gateway"
	ErrCodeServiceUnavailable ErrorCode = "service_unavailable"
	ErrCodeGatewayTimeout     ErrorCode = "gateway_timeout"
	ErrCodeUpstreamFailed     ErrorCode = "upstream_failed"
	ErrCodeCaptureFailed      ErrorCode = "capture_failed"
	ErrCodeSpecLoadFailed     ErrorCode = "spec_load_failed"
	ErrCodeConfigError        ErrorCode = "config_error"
)

// HTTPStatusMapping maps error codes to their HTTP status codes
var HTTPStatusMapping = map[ErrorCode]int{
	// Client errors (4xx)
	ErrCodeBadRequest:        http.StatusBadRequest,
	ErrCodeUnauthorized:      http.StatusUnauthorized,
	ErrCodeForbidden:         http.StatusForbidden,
	ErrCodeNotFound:          http.StatusNotFound,
	ErrCodeMethodNotAllowed:  http.StatusMethodNotAllowed,
	ErrCodeInvalidVersion:    http.StatusBadRequest,
	ErrCodeMissingParameter:  http.StatusBadRequest,
	ErrCodeInvalidPayload:    http.StatusBadRequest,
	ErrCodeRouteNotFound:     http.StatusNotFound,
	ErrCodeValidationError:   http.StatusBadRequest,
	ErrCodeQuotaExceeded:     http.StatusTooManyRequests,
	ErrCodeRateLimitExceeded: http.StatusTooManyRequests,

	// Server errors (5xx)
	ErrCodeInternalServer:     http.StatusInternalServerError,
	ErrCodeBadGateway:         http.StatusBadGateway,
	ErrCodeServiceUnavailable: http.StatusServiceUnavailable,
	ErrCodeGatewayTimeout:     http.StatusGatewayTimeout,
	ErrCodeUpstreamFailed:     http.StatusBadGateway,
	ErrCodeCaptureFailed:      http.StatusInternalServerError,
	ErrCodeSpecLoadFailed:     http.StatusInternalServerError,
	ErrCodeConfigError:        http.StatusInternalServerError,
}

// GetHTTPStatus returns the HTTP status code for an error code
func GetHTTPStatus(code ErrorCode) int {
	if status, ok := HTTPStatusMapping[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// ErrorResponse is the structured error response format
// All error responses follow this schema as defined in the OpenAPI spec
type ErrorResponse struct {
	// Error is the error code identifier
	Error ErrorCode `json:"error"`

	// Message is a human-readable error description
	Message string `json:"message"`

	// Details contains additional error context (optional)
	Details map[string]interface{} `json:"details,omitempty"`

	// DocsURL contains a link to relevant documentation (optional)
	DocsURL string `json:"docs_url,omitempty"`

	// RequestID contains the unique request identifier for tracing (optional)
	RequestID string `json:"request_id,omitempty"`
}

// NewErrorResponse creates a new error response
func NewErrorResponse(code ErrorCode, message string) *ErrorResponse {
	return &ErrorResponse{
		Error:   code,
		Message: message,
		Details: make(map[string]interface{}),
	}
}

// WithDetail adds a detail field to the error response
func (e *ErrorResponse) WithDetail(key string, value interface{}) *ErrorResponse {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithDocsURL sets the documentation URL
func (e *ErrorResponse) WithDocsURL(url string) *ErrorResponse {
	e.DocsURL = url
	return e
}

// WithRequestID sets the request ID
func (e *ErrorResponse) WithRequestID(requestID string) *ErrorResponse {
	e.RequestID = requestID
	return e
}

// Write writes the error response to the HTTP response writer
// If request is provided and request_id is not set, it extracts it from the request context
func (e *ErrorResponse) Write(w http.ResponseWriter, r *http.Request) {
	// Auto-populate request_id from context if not already set
	if e.RequestID == "" && r != nil {
		if requestID := GetRequestID(r); requestID != "" {
			e.RequestID = requestID
		}
	}

	statusCode := GetHTTPStatus(e.Error)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(e)
}

// WriteError is a convenience function to write an error response
func WriteError(w http.ResponseWriter, r *http.Request, code ErrorCode, message string) {
	NewErrorResponse(code, message).Write(w, r)
}

// ValidationError represents a specific field validation error
type ValidationError struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual,omitempty"`
	DocsURL  string `json:"docs_url,omitempty"`
}

// ValidationErrorResponse is the structured response for validation errors
type ValidationErrorResponse struct {
	Error   ErrorCode         `json:"error"`
	Message string            `json:"message"`
	Errors  []ValidationError `json:"errors"`
}

// WriteValidationError writes a validation error response
func WriteValidationError(w http.ResponseWriter, errors []ValidationError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(ValidationErrorResponse{
		Error:   ErrCodeValidationError,
		Message: "Request validation failed",
		Errors:  errors,
	})
}

// Common error response constructors for frequent error cases

// BadRequest creates a 400 Bad Request error
func BadRequest(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeBadRequest, message)
}

// Unauthorized creates a 401 Unauthorized error
func Unauthorized(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeUnauthorized, message)
}

// Forbidden creates a 403 Forbidden error
func Forbidden(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeForbidden, message)
}

// NotFound creates a 404 Not Found error
func NotFound(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeNotFound, message)
}

// MethodNotAllowed creates a 405 Method Not Allowed error
func MethodNotAllowed(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeMethodNotAllowed, message)
}

// InternalError creates a 500 Internal Server Error
func InternalError(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeInternalServer, message)
}

// BadGateway creates a 502 Bad Gateway error
func BadGateway(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeBadGateway, message)
}

// ServiceUnavailable creates a 503 Service Unavailable error
func ServiceUnavailable(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeServiceUnavailable, message)
}

// RouteNotFound creates a route not found error with docs link
func RouteNotFound(method, path string) *ErrorResponse {
	return NewErrorResponse(ErrCodeRouteNotFound,
		fmt.Sprintf("No route found for %s %s", method, path)).
		WithDetail("method", method).
		WithDetail("path", path).
		WithDocsURL("/docs")
}

// InvalidVersion creates an invalid version parameter error
func InvalidVersion(version, expected string) *ErrorResponse {
	return NewErrorResponse(ErrCodeInvalidVersion,
		fmt.Sprintf("Invalid version parameter: %s. Only %s is supported.", version, expected)).
		WithDetail("expected_version", expected).
		WithDetail("actual_version", version).
		WithDocsURL("/docs")
}

// MissingParameter creates a missing required parameter error
func MissingParameter(paramName string) *ErrorResponse {
	return NewErrorResponse(ErrCodeMissingParameter,
		fmt.Sprintf("The '%s' parameter is required", paramName)).
		WithDetail("parameter", paramName)
}

// UpstreamFailed creates an upstream request failed error
func UpstreamFailed(err error) *ErrorResponse {
	return NewErrorResponse(ErrCodeUpstreamFailed,
		"Upstream request failed").
		WithDetail("error", err.Error())
}
