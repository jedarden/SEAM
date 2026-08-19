package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ardenone/seam/internal/vault"
)

func TestReverseProxyServeHTTPUpstreamDialFailureUsesSafeJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	upstreamURL := upstream.URL + "/internal/upstream-target"
	upstream.Close()

	proxy, err := NewReverseProxy(upstreamURL)
	if err != nil {
		t.Fatalf("NewReverseProxy() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/request", nil))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	body := recorder.Body.Bytes()
	if !json.Valid(body) {
		t.Fatalf("response body is not valid JSON: %q", body)
	}
	var response ErrorResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error != ErrCodeUpstreamFailed {
		t.Errorf("error = %q, want %q", response.Error, ErrCodeUpstreamFailed)
	}
	if response.Message != "Upstream request failed" {
		t.Errorf("message = %q, want safe upstream failure message", response.Message)
	}
	if strings.Contains(string(body), upstreamURL) || strings.Contains(string(body), "dial tcp") {
		t.Fatalf("response body leaked upstream dial details: %q", body)
	}
}

func TestReverseProxyServeHTTPSecretStoreUnavailableUses503(t *testing.T) {
	proxy, err := NewReverseProxy("http://upstream.invalid")
	if err != nil {
		t.Fatalf("NewReverseProxy() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/request", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(_ context.Context, _ RouteEntry) ([]byte, error) {
		return nil, &vault.SecretStoreUnavailableError{}
	})
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	var response ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error != ErrCodeServiceUnavailable {
		t.Errorf("error = %q, want %q", response.Error, ErrCodeServiceUnavailable)
	}
	if response.Message != "Secret store unavailable" {
		t.Errorf("message = %q, want safe secret-store message", response.Message)
	}
	if strings.Contains(recorder.Body.String(), "OpenBao") {
		t.Errorf("response body leaked dependency detail: %q", recorder.Body.String())
	}
}

func TestReverseProxyServeHTTPDoesNotWriteSecondErrorAfterStreamingStarts(t *testing.T) {
	proxy, err := NewReverseProxy("http://upstream.invalid")
	if err != nil {
		t.Fatalf("NewReverseProxy() error = %v", err)
	}
	proxy.Client = &http.Client{Transport: proxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(&proxyFailingReader{}),
			Request:    req,
		}, nil
	})}

	recorder := &countingResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	proxy.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/request", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.writeHeaders != 1 {
		t.Fatalf("WriteHeader calls = %d, want 1", recorder.writeHeaders)
	}
	if got, want := recorder.Body.String(), "partial response"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

type proxyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f proxyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type proxyFailingReader struct {
	wrote bool
}

func (r *proxyFailingReader) Read(p []byte) (int, error) {
	if r.wrote {
		return 0, errors.New("streaming dial target detail")
	}
	r.wrote = true
	return copy(p, "partial response"), nil
}

type countingResponseWriter struct {
	*httptest.ResponseRecorder
	writeHeaders int
}

func (w *countingResponseWriter) WriteHeader(statusCode int) {
	w.writeHeaders++
	w.ResponseRecorder.WriteHeader(statusCode)
}
