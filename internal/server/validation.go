package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ValidationError is the structured error response for validation failures
type ValidationErrorResponse struct {
	Error           string `json:"error"`
	Message         string `json:"message"`
	ValidationErrors []ValidationFieldError `json:"validation_errors"`
	DocsPointer     string `json:"docs_pointer"`
}

// ValidationFieldError represents a single field validation error
type ValidationFieldError struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Reason   string `json:"reason"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

// validationMiddleware returns a middleware that validates requests against the OpenAPI spec
// It only validates non-reserved paths (routes that will be proxied)
func (s *Server) validationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip validation for reserved paths (control plane endpoints)
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Validate the request against the OpenAPI spec
		validationErr := s.specLoader.ValidateRequest(r)
		if validationErr != nil {
			// Request validation failed - return structured 400 error
			errJSON := validationErr.ToJSON(r.URL.Path, r.Method)
			writeValidationError(w, r.URL.Path, r.Method, errJSON)
			return
		}

		// Request is valid, proceed to next handler
		next.ServeHTTP(w, r)
	})
}

// writeValidationError writes a structured 400 error response
func writeValidationError(w http.ResponseWriter, path, method string, validationErrors interface{}) {
	w.Header().Set("Content-Type", "application/json")

	response := ValidationErrorResponse{
		Error:   "validation_failed",
		Message: "Request does not conform to the OpenAPI specification",
		DocsPointer: fmt.Sprintf("/docs/route?path=%s&method=%s&version=_unversioned", path, method),
	}

	// Handle both slice and single validation error formats
	switch v := validationErrors.(type) {
	case map[string]interface{}:
		// Already formatted as JSON map from ValidationError.ToJSON()
		if validationErrorsSlice, ok := v["validation_errors"].([]map[string]interface{}); ok {
			for _, err := range validationErrorsSlice {
				response.ValidationErrors = append(response.ValidationErrors, ValidationFieldError{
					Field:    getStringField(err, "field"),
					Expected: getStringField(err, "expected"),
					Actual:   getStringField(err, "actual"),
					Reason:   getStringField(err, "reason"),
					Line:     getIntField(err, "line"),
					Column:   getIntField(err, "column"),
				})
			}
		}
	case []map[string]interface{}:
		// Direct slice of validation errors
		for _, err := range v {
			response.ValidationErrors = append(response.ValidationErrors, ValidationFieldError{
				Field:    getStringField(err, "field"),
				Expected: getStringField(err, "expected"),
				Actual:   getStringField(err, "actual"),
				Reason:   getStringField(err, "reason"),
				Line:     getIntField(err, "line"),
				Column:   getIntField(err, "column"),
			})
		}
	}

	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(response)
}

// getStringField safely extracts a string field from a map
func getStringField(m map[string]interface{}, key string) string {
	if val, ok := m[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

// getIntField safely extracts an int field from a map
func getIntField(m map[string]interface{}, key string) int {
	if val, ok := m[key]; ok {
		if num, ok := val.(int); ok {
			return num
		}
		if num, ok := val.(float64); ok {
			return int(num)
		}
	}
	return 0
}
