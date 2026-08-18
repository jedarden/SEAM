package server

import (
	"encoding/json"
	"net/http"
)

// SpecValidationResponse is the structured error response for OpenAPI spec validation failures
type SpecValidationResponse struct {
	Error            string                 `json:"error"`
	Message          string                 `json:"message"`
	ValidationErrors []ValidationFieldError `json:"validation_errors"`
	DocsURL          string                 `json:"docs_url"`
}

// ValidationFieldError represents a single field validation error
type ValidationFieldError struct {
	Field         string `json:"field"`
	ExpectedShape string `json:"expected_shape"`
	Actual        string `json:"actual"`
	Reason        string `json:"reason"`
	Line          int    `json:"line,omitempty"`
	Column        int    `json:"column,omitempty"`
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
			writeValidationError(w, errJSON)
			return
		}

		// Request is valid, proceed to next handler
		next.ServeHTTP(w, r)
	})
}

// writeValidationError writes a structured 400 error response
func writeValidationError(w http.ResponseWriter, validationErrors map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")

	response := SpecValidationResponse{
		Error:   validationErrors["error"].(string),
		Message: validationErrors["message"].(string),
		DocsURL: validationErrors["docs_url"].(string),
	}

	// Handle validation errors array
	if validationErrorsSlice, ok := validationErrors["validation_errors"].([]map[string]interface{}); ok {
		for _, err := range validationErrorsSlice {
			response.ValidationErrors = append(response.ValidationErrors, ValidationFieldError{
				Field:         getStringField(err, "field"),
				ExpectedShape: getStringField(err, "expected_shape"),
				Actual:        getStringField(err, "actual"),
				Reason:        getStringField(err, "reason"),
				Line:          getIntField(err, "line"),
				Column:        getIntField(err, "column"),
			})
		}
	}

	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(response)
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
