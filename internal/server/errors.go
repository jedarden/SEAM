package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// ErrorCode is the stable, machine-readable identifier returned in an API
// error response. The value is part of SEAM's public HTTP contract.
type ErrorCode string

// Error codes taxonomy.
const (
	// Client errors (4xx).
	ErrCodeBadRequest          ErrorCode = "bad_request"
	ErrCodeUnauthorized        ErrorCode = "unauthorized"
	ErrCodeForbidden           ErrorCode = "forbidden"
	ErrCodeNotFound            ErrorCode = "not_found"
	ErrCodeMethodNotAllowed    ErrorCode = "method_not_allowed"
	ErrCodeInvalidVersion      ErrorCode = "invalid_version_parameter"
	ErrCodeMissingParameter    ErrorCode = "missing_required_parameter"
	ErrCodeInvalidPayload      ErrorCode = "invalid_payload"
	ErrCodeRouteNotFound       ErrorCode = "route_not_found"
	ErrCodeValidationFailed    ErrorCode = "validation_failed"
	ErrCodeQuotaExceeded       ErrorCode = "quota_exceeded"
	ErrCodeRateLimitExceeded   ErrorCode = "rate_limit_exceeded"
	ErrCodeLoopGuardExceeded   ErrorCode = "loop_guard_exceeded"

	// Dependency and server errors (5xx).
	ErrCodeInternalServer       ErrorCode = "internal_server_error"
	ErrCodeBadGateway           ErrorCode = "bad_gateway"
	ErrCodeServiceUnavailable   ErrorCode = "service_unavailable"
	ErrCodeGatewayTimeout       ErrorCode = "gateway_timeout"
	ErrCodeUpstreamFailed       ErrorCode = "upstream_failed"
	ErrCodeNoUpstreamConfigured ErrorCode = "no_upstream_configured"
	ErrCodeProxyCreationFailed  ErrorCode = "proxy_creation_failed"
	ErrCodeCaptureFailed        ErrorCode = "capture_failed"
	ErrCodeSpecLoadFailed       ErrorCode = "spec_load_failed"
	ErrCodeConfigError          ErrorCode = "config_error"

	// ErrCodeValidationError is retained as a source-compatible alias. The
	// wire value has always been validation_failed in the validator path.
	ErrCodeValidationError = ErrCodeValidationFailed

	// Fan-out multi-status errors (Phase 10.2).
	// These are used within 207 Multi-Status responses, not as top-level error codes.
	ErrCodeFanoutBreakerRefused ErrorCode = "fanout_breaker_refused"
	ErrCodeFanoutTimeout        ErrorCode = "fanout_timeout"
	ErrCodeFanoutTruncated      ErrorCode = "fanout_truncated"
	ErrCodeFanoutScopeWithheld  ErrorCode = "fanout_scope_withheld"

	// Credential health sentinel errors (Phase 12).
	// These errors are returned when credential refresh fails or cannot be retried.
	ErrCodeCredentialRefreshNotRetried ErrorCode = "credential_refresh_not_retried"
	ErrCodeSecretStoreUnavailable     ErrorCode = "secret_store_unavailable"
)

// HTTPStatusMapping is the canonical mapping from public error codes to HTTP
// status codes. Unknown codes fail closed as a generic 500 response.
var HTTPStatusMapping = map[ErrorCode]int{
	ErrCodeBadRequest:           http.StatusBadRequest,
	ErrCodeUnauthorized:         http.StatusUnauthorized,
	ErrCodeForbidden:            http.StatusForbidden,
	ErrCodeNotFound:             http.StatusNotFound,
	ErrCodeMethodNotAllowed:     http.StatusMethodNotAllowed,
	ErrCodeInvalidVersion:       http.StatusBadRequest,
	ErrCodeMissingParameter:     http.StatusBadRequest,
	ErrCodeInvalidPayload:       http.StatusBadRequest,
	ErrCodeRouteNotFound:        http.StatusNotFound,
	ErrCodeValidationFailed:     http.StatusBadRequest,
	ErrCodeQuotaExceeded:        http.StatusTooManyRequests,
	ErrCodeRateLimitExceeded:    http.StatusTooManyRequests,
	ErrCodeLoopGuardExceeded:    http.StatusTooManyRequests,
	ErrCodeInternalServer:       http.StatusInternalServerError,
	ErrCodeBadGateway:           http.StatusBadGateway,
	ErrCodeServiceUnavailable:   http.StatusServiceUnavailable,
	ErrCodeGatewayTimeout:       http.StatusGatewayTimeout,
	ErrCodeUpstreamFailed:       http.StatusBadGateway,
	ErrCodeNoUpstreamConfigured: http.StatusServiceUnavailable,
	ErrCodeProxyCreationFailed:  http.StatusServiceUnavailable,
	ErrCodeCaptureFailed:        http.StatusInternalServerError,
	ErrCodeSpecLoadFailed:       http.StatusInternalServerError,
	ErrCodeConfigError:          http.StatusInternalServerError,

	// Fan-out errors map to their appropriate HTTP status codes.
	// These are used within 207 responses, not as top-level codes.
	ErrCodeFanoutBreakerRefused: http.StatusServiceUnavailable,
	ErrCodeFanoutTimeout:        http.StatusGatewayTimeout,
	ErrCodeFanoutTruncated:      http.StatusPartialContent,
	ErrCodeFanoutScopeWithheld:  http.StatusForbidden,

	// Credential health errors map to their appropriate HTTP status codes.
	ErrCodeCredentialRefreshNotRetried: http.StatusUnauthorized,
	ErrCodeSecretStoreUnavailable:     http.StatusServiceUnavailable,
}

// GetHTTPStatus returns the HTTP status for code. Unknown codes are treated as
// internal errors so an accidental new code cannot produce a successful
// response.
func GetHTTPStatus(code ErrorCode) int {
	if status, ok := HTTPStatusMapping[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// IsKnownErrorCode reports whether code is part of the public taxonomy.
func IsKnownErrorCode(code ErrorCode) bool {
	_, ok := HTTPStatusMapping[code]
	return ok
}

// ValidationFieldError identifies one part of a request that did not conform
// to the selected OpenAPI operation.
type ValidationFieldError struct {
	Field         string `json:"field"`
	ExpectedShape string `json:"expected_shape"`
	Actual        string `json:"actual,omitempty"`
	Reason        string `json:"reason"`
	Line          int    `json:"line,omitempty"`
	Column        int    `json:"column,omitempty"`
}

// ErrorResponse is the common JSON envelope returned for every SEAM API
// error. Validation failures add validation_errors without changing the base
// error, message, docs_url, and request_id contract.
type ErrorResponse struct {
	Error            ErrorCode              `json:"error"`
	Message          string                 `json:"message"`
	Details          map[string]interface{} `json:"details,omitempty"`
	ValidationErrors []ValidationFieldError `json:"validation_errors,omitempty"`
	DocsURL          string                 `json:"docs_url,omitempty"`
	RequestID        string                 `json:"request_id,omitempty"`
}

// RequestError carries an internal cause through the request pipeline while
// keeping that cause out of the public response. Use errors.Is/errors.As on a
// RequestError to inspect the underlying failure.
type RequestError struct {
	Code             ErrorCode
	Message          string
	Details          map[string]interface{}
	ValidationErrors []ValidationFieldError
	DocsURL          string
	cause            error
}

// NewRequestError creates a typed request error without an underlying cause.
func NewRequestError(code ErrorCode, message string) *RequestError {
	return &RequestError{Code: code, Message: message}
}

// WrapRequestError attaches a cause for logging and inspection. The cause is
// deliberately excluded from Response and Write.
func WrapRequestError(code ErrorCode, message string, cause error) *RequestError {
	return &RequestError{Code: code, Message: message, cause: cause}
}

func (e *RequestError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the internal cause to errors.Is/errors.As without exposing it
// in the HTTP response.
func (e *RequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// HTTPStatus returns the status associated with the request error.
func (e *RequestError) HTTPStatus() int {
	if e == nil {
		return http.StatusInternalServerError
	}
	return GetHTTPStatus(e.Code)
}

// WithDetail adds caller-safe context to the public response.
func (e *RequestError) WithDetail(key string, value interface{}) *RequestError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithDocsURL sets the documentation pointer for the request error.
func (e *RequestError) WithDocsURL(url string) *RequestError {
	e.DocsURL = url
	return e
}

// WithValidationErrors attaches field-level OpenAPI validation diagnostics.
func (e *RequestError) WithValidationErrors(errors []ValidationFieldError) *RequestError {
	e.ValidationErrors = errors
	return e
}

// Response builds the public representation of the request error.
func (e *RequestError) Response() *ErrorResponse {
	if e == nil {
		return NewErrorResponse(ErrCodeInternalServer, "An unexpected error occurred")
	}
	return &ErrorResponse{
		Error:            e.Code,
		Message:          e.Message,
		Details:          e.Details,
		ValidationErrors: e.ValidationErrors,
		DocsURL:          e.DocsURL,
	}
}

// Write sends the public representation and never serializes the cause.
func (e *RequestError) Write(w http.ResponseWriter, r *http.Request) {
	e.Response().Write(w, r)
}

// logRequestError records the internal cause with the request correlation ID.
// The same ID is returned to the caller by ErrorResponse.Write.
func logRequestError(r *http.Request, operation string, err *RequestError) {
	requestID := GetRequestID(r)
	if requestID == "" {
		requestID = "unknown"
	}
	log.Printf("[%s] request_id=%s error=%v", operation, requestID, err)
}

// NewErrorResponse creates a public error response.
func NewErrorResponse(code ErrorCode, message string) *ErrorResponse {
	return &ErrorResponse{Error: code, Message: message}
}

// WithDetail adds a caller-safe detail field to the response.
func (e *ErrorResponse) WithDetail(key string, value interface{}) *ErrorResponse {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithDocsURL sets the documentation URL.
func (e *ErrorResponse) WithDocsURL(url string) *ErrorResponse {
	e.DocsURL = url
	return e
}

// WithRequestID sets the request ID explicitly.
func (e *ErrorResponse) WithRequestID(requestID string) *ErrorResponse {
	e.RequestID = requestID
	return e
}

// WithValidationErrors attaches field-level validation diagnostics.
func (e *ErrorResponse) WithValidationErrors(errors []ValidationFieldError) *ErrorResponse {
	e.ValidationErrors = errors
	return e
}

// NewLoopGuardErrorResponse creates a 429 response for loop guard intervention.
// Per Phase 13.1: plain-language body naming N repeats + re-read /docs/route pointer
// + Retry-After = seconds to window close + pre-Phase-7 statement that guard is route-wide.
func NewLoopGuardErrorResponse(repeatCount int, retryAfter int, routeID string) *RequestError {
	message := fmt.Sprintf("Loop guard intervention: This request has been repeated %d times with failures. The loop guard is protecting this route-wide (pre-Phase-7 per-request scoping). Please re-read /docs/route for guidance on idempotent request patterns.", repeatCount)

	return &RequestError{
		Code:    ErrCodeLoopGuardExceeded,
		Message: message,
		Details: map[string]interface{}{
			"repeat_count":   repeatCount,
			"max_repeats":    repeatCount, // This indicates we hit the limit
			"route_id":       routeID,
			"guard_scope":    "route-wide",
			"phase_note":     "Phase 7 per-request scoping not yet implemented",
			"retry_after":    retryAfter,
			"retry_after_unit": "seconds",
		},
		DocsURL: "/docs/route",
	}
}

// Write sends a structured JSON error. It normalizes unknown codes and
// serialization failures to the documented internal_server_error response.
func (e *ErrorResponse) Write(w http.ResponseWriter, r *http.Request) {
	response := normalizeErrorResponse(e, r)
	statusCode := GetHTTPStatus(response.Error)
	payload, err := json.Marshal(response)
	if err != nil {
		log.Printf("[error-response] request_id=%s failed to encode response: %v", response.RequestID, err)
		response = normalizeErrorResponse(&ErrorResponse{
			Error:     ErrCodeInternalServer,
			Message:   "An unexpected error occurred",
			RequestID: response.RequestID,
		}, r)
		statusCode = http.StatusInternalServerError
		payload, _ = json.Marshal(response)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(statusCode)
	payload = append(payload, '\n')
	_, _ = w.Write(payload)
}

func normalizeErrorResponse(response *ErrorResponse, r *http.Request) *ErrorResponse {
	if response == nil {
		response = &ErrorResponse{}
	}

	normalized := *response
	if !IsKnownErrorCode(normalized.Error) {
		normalized.Error = ErrCodeInternalServer
		normalized.Message = "An unexpected error occurred"
		normalized.Details = nil
		normalized.ValidationErrors = nil
		normalized.DocsURL = ""
	}
	if normalized.Message == "" {
		normalized.Message = http.StatusText(GetHTTPStatus(normalized.Error))
	}
	if normalized.RequestID == "" {
		normalized.RequestID = GetRequestID(r)
	}
	return &normalized
}

// WriteError is a convenience function to write an error response.
func WriteError(w http.ResponseWriter, r *http.Request, code ErrorCode, message string) {
	NewErrorResponse(code, message).Write(w, r)
}

// ValidationError is the legacy field-validation input accepted by
// WriteValidationError. New code should use ValidationFieldError.
type ValidationError struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual,omitempty"`
	DocsURL  string `json:"docs_url,omitempty"`
}

// WriteValidationError writes legacy validation inputs using the common
// response envelope.
func WriteValidationError(w http.ResponseWriter, errors []ValidationError) {
	fieldErrors := make([]ValidationFieldError, 0, len(errors))
	for _, validationError := range errors {
		fieldErrors = append(fieldErrors, ValidationFieldError{
			Field:         validationError.Field,
			ExpectedShape: validationError.Expected,
			Actual:        validationError.Actual,
			Reason:        "Request validation failed",
		})
	}
	NewErrorResponse(ErrCodeValidationFailed, "Request validation failed").
		WithValidationErrors(fieldErrors).
		Write(w, nil)
}

// Common response constructors.

func BadRequest(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeBadRequest, message)
}

func Unauthorized(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeUnauthorized, message)
}

func Forbidden(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeForbidden, message)
}

func NotFound(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeNotFound, message)
}

func MethodNotAllowed(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeMethodNotAllowed, message)
}

func InternalError(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeInternalServer, message)
}

func BadGateway(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeBadGateway, message)
}

func ServiceUnavailable(message string) *ErrorResponse {
	return NewErrorResponse(ErrCodeServiceUnavailable, message)
}

// RouteNotFound creates a route-not-found response with caller-safe context.
func RouteNotFound(method, path string) *ErrorResponse {
	return NewErrorResponse(ErrCodeRouteNotFound,
		fmt.Sprintf("No route found for %s %s", method, path)).
		WithDetail("method", method).
		WithDetail("path", path).
		WithDocsURL("/docs")
}

// InvalidVersion creates an invalid-version response.
func InvalidVersion(version, expected string) *ErrorResponse {
	return NewErrorResponse(ErrCodeInvalidVersion,
		fmt.Sprintf("Invalid version parameter. Expected: %s", expected)).
		WithDetail("expected_version", expected).
		WithDetail("actual_version", version).
		WithDocsURL("/docs")
}

// MissingParameter creates a missing-required-parameter response.
func MissingParameter(paramName string) *ErrorResponse {
	return NewErrorResponse(ErrCodeMissingParameter,
		fmt.Sprintf("The '%s' parameter is required", paramName)).
		WithDetail("parameter", paramName)
}

// UpstreamFailed creates a safe upstream failure response. The internal cause
// must be logged or wrapped separately and is never added to response details.
func UpstreamFailed(_ error) *ErrorResponse {
	return NewErrorResponse(ErrCodeUpstreamFailed, "Upstream request failed")
}
