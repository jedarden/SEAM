package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorTaxonomyWritesDocumentedEnvelopeAndStatus(t *testing.T) {
	tests := []struct {
		code   ErrorCode
		status int
	}{
		{ErrCodeBadRequest, http.StatusBadRequest},
		{ErrCodeUnauthorized, http.StatusUnauthorized},
		{ErrCodeForbidden, http.StatusForbidden},
		{ErrCodeNotFound, http.StatusNotFound},
		{ErrCodeMethodNotAllowed, http.StatusMethodNotAllowed},
		{ErrCodeInvalidVersion, http.StatusBadRequest},
		{ErrCodeMissingParameter, http.StatusBadRequest},
		{ErrCodeInvalidPayload, http.StatusBadRequest},
		{ErrCodeRouteNotFound, http.StatusNotFound},
		{ErrCodeValidationFailed, http.StatusBadRequest},
		{ErrCodeQuotaExceeded, http.StatusTooManyRequests},
		{ErrCodeRateLimitExceeded, http.StatusTooManyRequests},
		{ErrCodeLoopGuardExceeded, http.StatusTooManyRequests},
		{ErrCodeInternalServer, http.StatusInternalServerError},
		{ErrCodeBadGateway, http.StatusBadGateway},
		{ErrCodeServiceUnavailable, http.StatusServiceUnavailable},
		{ErrCodeGatewayTimeout, http.StatusGatewayTimeout},
		{ErrCodeUpstreamFailed, http.StatusBadGateway},
		{ErrCodeNoUpstreamConfigured, http.StatusServiceUnavailable},
		{ErrCodeProxyCreationFailed, http.StatusServiceUnavailable},
		{ErrCodeCaptureFailed, http.StatusInternalServerError},
		{ErrCodeSpecLoadFailed, http.StatusInternalServerError},
		{ErrCodeConfigError, http.StatusInternalServerError},

		// Credential health sentinels (Phase 12).
		{ErrCodeCredentialRefreshNotRetried, http.StatusUnauthorized},
		{ErrCodeSecretStoreUnavailable, http.StatusServiceUnavailable},
	}

	if len(HTTPStatusMapping) != len(tests) {
		t.Fatalf("taxonomy contains %d codes, test covers %d", len(HTTPStatusMapping), len(tests))
	}

	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			if !IsKnownErrorCode(test.code) {
				t.Fatalf("%q is not registered", test.code)
			}
			if got := GetHTTPStatus(test.code); got != test.status {
				t.Fatalf("GetHTTPStatus(%q) = %d, want %d", test.code, got, test.status)
			}

			handler := (&Server{}).requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				NewErrorResponse(test.code, "test message").Write(w, r)
			}))
			request := httptest.NewRequest(http.MethodGet, "/test", nil)
			request.Header.Set("X-Request-ID", "request-taxonomy")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.status {
				t.Fatalf("response status = %d, want %d", response.Code, test.status)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if got := response.Header().Get("X-Request-ID"); got != "request-taxonomy" {
				t.Errorf("X-Request-ID = %q, want request-taxonomy", got)
			}

			var body ErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error != test.code || body.Message != "test message" {
				t.Errorf("body identity = (%q, %q), want (%q, test message)", body.Error, body.Message, test.code)
			}
			if body.RequestID != "request-taxonomy" {
				t.Errorf("body request_id = %q, want request-taxonomy", body.RequestID)
			}
		})
	}
}

func TestUnknownAndUnencodableErrorsFailClosed(t *testing.T) {
	tests := []struct {
		name     string
		response *ErrorResponse
	}{
		{
			name:     "unknown code",
			response: NewErrorResponse(ErrorCode("not-in-contract"), "must not escape").WithDetail("unsafe", "value"),
		},
		{
			name:     "unencodable details",
			response: NewErrorResponse(ErrCodeBadRequest, "must not escape").WithDetail("invalid", make(chan struct{})),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.response.WithRequestID("request-fallback").Write(recorder, nil)
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", recorder.Code)
			}
			var body ErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("decode fallback: %v", err)
			}
			if body.Error != ErrCodeInternalServer || body.Message != "An unexpected error occurred" {
				t.Errorf("fallback = (%q, %q)", body.Error, body.Message)
			}
			if body.Details != nil || body.ValidationErrors != nil || body.DocsURL != "" {
				t.Errorf("fallback leaked optional fields: %+v", body)
			}
			if body.RequestID != "request-fallback" {
				t.Errorf("fallback request_id = %q", body.RequestID)
			}
		})
	}
}

func TestRequestErrorPropagatesCauseWithoutExposingIt(t *testing.T) {
	cause := errors.New("internal dependency location must remain private")
	requestErr := WrapRequestError(ErrCodeUpstreamFailed, "Upstream request failed", cause).
		WithDetail("phase", "dispatch")

	if !errors.Is(requestErr, cause) {
		t.Fatal("RequestError does not unwrap to its cause")
	}
	if requestErr.HTTPStatus() != http.StatusBadGateway {
		t.Fatalf("HTTPStatus = %d, want 502", requestErr.HTTPStatus())
	}
	if !strings.Contains(requestErr.Error(), cause.Error()) {
		t.Fatal("internal error string lost its cause")
	}

	recorder := httptest.NewRecorder()
	requestErr.Write(recorder, nil)
	if strings.Contains(recorder.Body.String(), cause.Error()) {
		t.Fatalf("public response exposed internal cause: %s", recorder.Body.String())
	}
	var body ErrorResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Details["phase"] != "dispatch" {
		t.Errorf("caller-safe detail was not preserved: %+v", body.Details)
	}
}

func TestValidationFailureUsesCommonEnvelope(t *testing.T) {
	validation := map[string]interface{}{
		"message":  "Request does not conform to the OpenAPI specification",
		"docs_url": "/docs/route?path=/widgets/{id}&method=POST&version=_unversioned",
		"validation_errors": []map[string]interface{}{
			{
				"field":          "body.name",
				"expected_shape": "string",
				"actual":         "number",
				"reason":         "type mismatch",
				"line":           4,
				"column":         9,
			},
		},
	}

	handler := (&Server{}).requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeValidationError(w, r, validation)
	}))
	request := httptest.NewRequest(http.MethodPost, "/widgets/widget-123", nil)
	request.Header.Set("X-Request-ID", "request-validation")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	var body ErrorResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != ErrCodeValidationFailed || body.RequestID != "request-validation" {
		t.Errorf("validation envelope = %+v", body)
	}
	if len(body.ValidationErrors) != 1 || body.ValidationErrors[0].ExpectedShape != "string" {
		t.Errorf("validation errors = %+v", body.ValidationErrors)
	}
}

func TestMustGetRequestIDAcceptsShortIDs(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(context.WithValue(request.Context(), requestIDKey, "short"))
	if got := MustGetRequestID(request); got != "req_short" {
		t.Fatalf("MustGetRequestID = %q, want req_short", got)
	}
}

func TestOpenAPIErrorSchemaMatchesRuntimeTaxonomy(t *testing.T) {
	server := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})
	raw, err := server.specLoader.GetRawJSON()
	if err != nil {
		t.Fatalf("load OpenAPI JSON: %v", err)
	}
	var document map[string]interface{}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode OpenAPI JSON: %v", err)
	}

	components := document["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	errorSchema := schemas["ErrorResponse"].(map[string]interface{})
	properties := errorSchema["properties"].(map[string]interface{})
	errorProperty := properties["error"].(map[string]interface{})
	enumValues := errorProperty["enum"].([]interface{})

	openAPICodes := make(map[ErrorCode]bool, len(enumValues))
	for _, value := range enumValues {
		code, ok := value.(string)
		if !ok {
			t.Fatalf("non-string error code in OpenAPI enum: %T", value)
		}
		openAPICodes[ErrorCode(code)] = true
	}
	if len(openAPICodes) != len(HTTPStatusMapping) {
		t.Fatalf("OpenAPI has %d unique codes, runtime has %d", len(openAPICodes), len(HTTPStatusMapping))
	}
	for code := range HTTPStatusMapping {
		if !openAPICodes[code] {
			t.Errorf("runtime error code %q is missing from OpenAPI", code)
		}
	}
	if _, ok := properties["validation_errors"]; !ok {
		t.Error("ErrorResponse schema does not define validation_errors")
	}

	required := errorSchema["required"].([]interface{})
	requiredFields := make(map[string]bool, len(required))
	for _, field := range required {
		requiredFields[field.(string)] = true
	}
	for _, field := range []string{"error", "message"} {
		if !requiredFields[field] {
			t.Errorf("ErrorResponse does not require %q", field)
		}
	}
}
