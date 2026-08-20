package server

import "net/http"

// SpecValidationResponse is retained as an alias for callers that decode
// validation errors directly. Validation failures now use the common error
// envelope returned by every other API path.
type SpecValidationResponse = ErrorResponse

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
			path := validationErr.PathTemplate
			if path == "" {
				path = r.URL.Path
			}
			errJSON := validationErr.ToJSON(path, r.Method)
			writeValidationError(w, r, errJSON)
			return
		}

		// Request is valid, proceed to next handler
		next.ServeHTTP(w, r)
	})
}

// writeValidationError writes a structured 400 error response
func writeValidationError(w http.ResponseWriter, r *http.Request, validationErrors map[string]interface{}) {
	response := NewErrorResponse(
		ErrCodeValidationFailed,
		getStringField(validationErrors, "message"),
	).WithDocsURL(getStringField(validationErrors, "docs_url"))

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
	if response.Message == "" {
		response.Message = "Request does not conform to the OpenAPI specification"
	}
	response.Write(w, r)
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
