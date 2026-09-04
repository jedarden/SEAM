package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHTTPErrorPathsUseCommonEnvelope(t *testing.T) {
	controlPlane := New(&Config{
		CallerPort:   8080,
		OperatorPort: 8081,
		BaseURL:      "http://localhost:8080",
		SpecDir:      "../../spec",
	})

	captureFailure := NewCaptureMiddleware(t.TempDir(), "test", "test-incumbent", false)
	captureFailure.writeCorpusFile = func(string, []byte, os.FileMode) error {
		return errors.New("private capture storage failure")
	}

	tests := []struct {
		name       string
		method     string
		target     string
		serve      func(http.ResponseWriter, *http.Request)
		wantStatus int
		wantCode   ErrorCode
	}{
		{
			name:       "invalid version",
			method:     http.MethodGet,
			target:     "/docs?version=v1",
			serve:      controlPlane.versionMiddleware(controlPlane.callerMux).ServeHTTP,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrCodeInvalidVersion,
		},
		{
			name:       "missing docs parameter",
			method:     http.MethodGet,
			target:     "/docs/route",
			serve:      controlPlane.docsRouteHandler,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrCodeMissingParameter,
		},
		{
			name:       "docs route not found",
			method:     http.MethodGet,
			target:     "/docs/route?path=/absent&method=GET",
			serve:      controlPlane.docsRouteHandler,
			wantStatus: http.StatusNotFound,
			wantCode:   ErrCodeRouteNotFound,
		},
		{
			name:       "method not allowed",
			method:     http.MethodPost,
			target:     "/config/status",
			serve:      controlPlane.configStatusHandler,
			wantStatus: http.StatusMethodNotAllowed,
			wantCode:   ErrCodeMethodNotAllowed,
		},
		{
			name:       "operator endpoint not found",
			method:     http.MethodGet,
			target:     "/operator/absent",
			serve:      controlPlane.operatorMux.ServeHTTP,
			wantStatus: http.StatusNotFound,
			wantCode:   ErrCodeNotFound,
		},
		{
			name:       "capture disabled",
			method:     http.MethodPost,
			target:     "/_seam/capture/save",
			serve:      (&Server{}).captureSaveHandler,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   ErrCodeServiceUnavailable,
		},
		{
			name:       "capture save failed",
			method:     http.MethodPost,
			target:     "/_seam/capture/save",
			serve:      (&Server{captureMiddleware: captureFailure}).captureSaveHandler,
			wantStatus: http.StatusInternalServerError,
			wantCode:   ErrCodeCaptureFailed,
		},
		{
			name:       "proxy route not found",
			method:     http.MethodGet,
			target:     "/absent",
			serve:      newErrorPathDispatchServer(nil).dispatchHandler,
			wantStatus: http.StatusNotFound,
			wantCode:   ErrCodeRouteNotFound,
		},
		{
			name:   "upstream not configured",
			method: http.MethodGet,
			target: "/no-upstream",
			serve: newErrorPathDispatchServer([]RouteEntry{{
				PathTemplate: "/no-upstream",
				Method:       http.MethodGet,
				APIVersion:   unversionedAPIVersion,
			}}).dispatchHandler,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   ErrCodeNoUpstreamConfigured,
		},
		{
			name:   "proxy construction failed",
			method: http.MethodGet,
			target: "/bad-upstream",
			serve: newErrorPathDispatchServer([]RouteEntry{{
				PathTemplate:   "/bad-upstream",
				Method:         http.MethodGet,
				APIVersion:     unversionedAPIVersion,
				UpstreamTarget: "http://invalid:999999",
			}}).dispatchHandler,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   ErrCodeProxyCreationFailed,
		},
		{
			name:   "quota exceeded",
			method: http.MethodGet,
			target: "/quota",
			serve: func(w http.ResponseWriter, r *http.Request) {
				(&Server{}).writeQuotaExceededResponse(w, r, r.URL.Path, 0, 0)
			},
			wantStatus: http.StatusTooManyRequests,
			wantCode:   ErrCodeQuotaExceeded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := (&Server{}).requestIDMiddleware(http.HandlerFunc(test.serve))
			request := httptest.NewRequest(test.method, test.target, nil)
			request.Header.Set("X-Request-ID", "request-error-path")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			var body ErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
			}
			if body.Error != test.wantCode || body.Message == "" {
				t.Errorf("error envelope = (%q, %q), want code %q and a message", body.Error, body.Message, test.wantCode)
			}
			if body.RequestID != "request-error-path" {
				t.Errorf("request_id = %q, want request-error-path", body.RequestID)
			}
			if strings.Contains(recorder.Body.String(), "private capture storage failure") {
				t.Fatalf("response exposed internal cause: %s", recorder.Body.String())
			}
		})
	}
}

func newErrorPathDispatchServer(routes []RouteEntry) *Server {
	return &Server{
		config:            &Config{},
		routeTableHolder:  NewThreadSafeTableHolder(&RouteTable{routes: routes}),
		proxyMap:          make(map[string]*ReverseProxy),
		upstreamClientMap: make(map[string]*http.Client),
		cache:             NewResponseCache(),
		singleFlight:      NewSingleFlight(),
		cacheTTLs:         make(map[string]int),
		circuitBreakers:   NewCircuitBreakerStateRegistry(),
		quotaTracker:      NewQuotaTracker(),
		costPerCalls:      make(map[string]float64),
	}
}
