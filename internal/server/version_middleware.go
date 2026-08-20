package server

import "net/http"

const unversionedAPIVersion = "_unversioned"

// versionMiddleware validates every supplied version query parameter before
// the request reaches endpoint-specific middleware or handlers. Requests that
// omit version remain valid and use the default unversioned API.
func (s *Server) versionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version, valid := validVersionParameter(r); !valid {
			InvalidVersion(version, unversionedAPIVersion).Write(w, r)
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
