package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const unversionedAPIVersion = "_unversioned"

// invalidVersionParameterResponse preserves the public error shape introduced
// by the documentation endpoints while allowing validation to happen at the
// listener boundary for every endpoint.
type invalidVersionParameterResponse struct {
	Error           ErrorCode `json:"error"`
	Message         string    `json:"message"`
	ExpectedVersion string    `json:"expected_version"`
	ActualVersion   string    `json:"actual_version"`
	DocsURL         string    `json:"docs_url"`
}

// versionMiddleware validates every supplied version query parameter before
// the request reaches endpoint-specific middleware or handlers. Requests that
// omit version remain valid and use the default unversioned API.
func (s *Server) versionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version, valid := validVersionParameter(r); !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(invalidVersionParameterResponse{
				Error:           ErrCodeInvalidVersion,
				Message:         fmt.Sprintf("Invalid version parameter. Expected: %s", unversionedAPIVersion),
				ExpectedVersion: unversionedAPIVersion,
				ActualVersion:   version,
				DocsURL:         "/docs",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// validVersionParameter accepts an omitted version or one or more occurrences
// of the sole supported value. Checking every value prevents a valid first
// value from hiding a later invalid duplicate.
func validVersionParameter(r *http.Request) (invalidValue string, valid bool) {
	versions, present := r.URL.Query()["version"]
	if !present {
		return "", true
	}

	for _, version := range versions {
		if version != unversionedAPIVersion {
			return version, false
		}
	}

	return "", true
}
