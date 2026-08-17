package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestForwardRequestSimpleGET(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/test/path" || r.URL.RawQuery != "answer=42" {
			t.Errorf("URL = %s, want /test/path?answer=42", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte("streamed response"))
	}))
	defer upstream.Close()

	in := httptest.NewRequest(http.MethodGet, "/test/path?answer=42", nil)
	resp, err := ForwardRequest(context.Background(), in, nil, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	if string(body) != "streamed response" {
		t.Errorf("body = %q, want streamed response", body)
	}
}

func TestForwardRequestPOSTWithBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		if string(body) != `{"message":"hello"}` {
			t.Errorf("body = %q, want JSON request body", body)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()

	in := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"message":"hello"}`))
	in.Header.Set("Content-Type", "application/json")
	resp, err := ForwardRequest(context.Background(), in, nil, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
}

func TestForwardRequestPreservesHeadersAndAddsForwardingHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Request-ID"); got != "request-123" {
			t.Errorf("X-Request-ID = %q, want request-123", got)
		}
		if got := r.Header.Get("Connection"); got != "" {
			t.Errorf("Connection = %q, want omitted", got)
		}
		if got := r.Header.Get("Keep-Alive"); got != "" {
			t.Errorf("Keep-Alive = %q, want omitted", got)
		}
		if got := r.Header.Get("X-Remove-Me"); got != "" {
			t.Errorf("Connection-listed X-Remove-Me = %q, want omitted", got)
		}
		if got := r.Header.Get("X-Forwarded-For"); got != "192.0.2.10" {
			t.Errorf("X-Forwarded-For = %q, want 192.0.2.10", got)
		}
		if got := r.Header.Get("X-Forwarded-Proto"); got != "http" {
			t.Errorf("X-Forwarded-Proto = %q, want http", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	in := httptest.NewRequest(http.MethodGet, "/headers", nil)
	in.RemoteAddr = "192.0.2.10:4567"
	in.Header.Set("X-Request-ID", "request-123")
	in.Header.Set("Connection", "keep-alive, X-Remove-Me")
	in.Header.Set("Keep-Alive", "timeout=5")
	in.Header.Set("X-Remove-Me", "connection-specific")

	resp, err := ForwardRequest(context.Background(), in, nil, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()
}

func TestForwardRequestAcceptsRouteMatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	match := &RouteMatch{
		Route:      RouteEntry{PathTemplate: "/users/{id}", Method: http.MethodGet, APIVersion: "v1"},
		PathParams: map[string]string{"id": "123"},
	}
	resp, err := ForwardRequest(context.Background(), httptest.NewRequest(http.MethodGet, "/users/123", nil), match, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()
}

func TestForwardRequestTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := ForwardRequest(ctx, httptest.NewRequest(http.MethodGet, "/slow", nil), nil, upstream.URL)
	if err == nil {
		t.Fatal("ForwardRequest() error = nil, want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline exceeded") {
		t.Errorf("error = %v, want deadline exceeded", err)
	}
}

func TestForwardRequestConnectionError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	address := upstream.URL
	upstream.Close()

	_, err := ForwardRequest(context.Background(), httptest.NewRequest(http.MethodGet, "/unavailable", nil), nil, address)
	if err == nil {
		t.Fatal("ForwardRequest() error = nil, want connection error")
	}
	if !strings.Contains(err.Error(), "upstream request failed") {
		t.Errorf("error = %v, want upstream request failed", err)
	}
}

func TestUpstreamHTTPClientConfiguration(t *testing.T) {
	if defaultUpstreamClient.Timeout != upstreamRequestTimeout {
		t.Errorf("client timeout = %s, want %s", defaultUpstreamClient.Timeout, upstreamRequestTimeout)
	}
	transport, ok := defaultUpstreamClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport type = %T, want *http.Transport", defaultUpstreamClient.Transport)
	}
	if transport.MaxIdleConns != upstreamMaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d", transport.MaxIdleConns, upstreamMaxIdleConns)
	}
	if transport.IdleConnTimeout != upstreamIdleConnTimeout {
		t.Errorf("IdleConnTimeout = %s, want %s", transport.IdleConnTimeout, upstreamIdleConnTimeout)
	}
}

func TestStreamResponseOKWithBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "custom-value")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","message":"response body"}`))
	}))
	defer upstream.Close()

	// Forward request to get response
	in := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := ForwardRequest(context.Background(), in, nil, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()

	// Stream response to a test ResponseRecorder
	recorder := httptest.NewRecorder()
	if err := StreamResponse(recorder, resp); err != nil {
		t.Fatalf("StreamResponse() error = %v", err)
	}

	// Verify status code
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	// Verify headers were copied
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if customHeader := recorder.Header().Get("X-Custom-Header"); customHeader != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want custom-value", customHeader)
	}

	// Verify body was streamed
	body := recorder.Body.String()
	expectedBody := `{"status":"ok","message":"response body"}`
	if body != expectedBody {
		t.Errorf("body = %q, want %q", body, expectedBody)
	}
}

func TestStreamResponseNotFound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("resource not found"))
	}))
	defer upstream.Close()

	in := httptest.NewRequest(http.MethodGet, "/missing", nil)
	resp, err := ForwardRequest(context.Background(), in, nil, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()

	recorder := httptest.NewRecorder()
	if err := StreamResponse(recorder, resp); err != nil {
		t.Fatalf("StreamResponse() error = %v", err)
	}

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}

	body := recorder.Body.String()
	if body != "resource not found" {
		t.Errorf("body = %q, want resource not found", body)
	}
}

func TestStreamResponseServerError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer upstream.Close()

	in := httptest.NewRequest(http.MethodGet, "/error", nil)
	resp, err := ForwardRequest(context.Background(), in, nil, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()

	recorder := httptest.NewRecorder()
	if err := StreamResponse(recorder, resp); err != nil {
		t.Fatalf("StreamResponse() error = %v", err)
	}

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	body := recorder.Body.String()
	expectedBody := `{"error":"internal server error"}`
	if body != expectedBody {
		t.Errorf("body = %q, want %q", body, expectedBody)
	}
}

func TestStreamResponseLargeBody(t *testing.T) {
	// Create a 1MB response body
	largeBody := make([]byte, 1024*1024)
	for i := range largeBody {
		largeBody[i] = byte('A' + (i % 26))
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(largeBody)
	}))
	defer upstream.Close()

	in := httptest.NewRequest(http.MethodGet, "/large", nil)
	resp, err := ForwardRequest(context.Background(), in, nil, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()

	recorder := httptest.NewRecorder()
	if err := StreamResponse(recorder, resp); err != nil {
		t.Fatalf("StreamResponse() error = %v", err)
	}

	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	body := recorder.Body.Bytes()
	if len(body) != len(largeBody) {
		t.Errorf("body length = %d, want %d", len(body), len(largeBody))
	}
	if !bytes.Equal(body, largeBody) {
		t.Error("body content does not match expected large body")
	}
}

func TestStreamResponseFiltersHopByHopHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set various headers including hop-by-hop headers
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Keep-Alive", "timeout=5, max=100")
		w.Header().Set("Proxy-Authenticate", "Basic")
		w.Header().Set("Proxy-Authorization", "credentials")
		w.Header().Set("TE", "trailers")
		w.Header().Set("Trailer", "X-Custom-Trailer")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Header().Set("Upgrade", "h2c")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Custom-Header", "should-be-present")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("response"))
	}))
	defer upstream.Close()

	in := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := ForwardRequest(context.Background(), in, nil, upstream.URL)
	if err != nil {
		t.Fatalf("ForwardRequest() error = %v", err)
	}
	defer resp.Body.Close()

	recorder := httptest.NewRecorder()
	if err := StreamResponse(recorder, resp); err != nil {
		t.Fatalf("StreamResponse() error = %v", err)
	}

	// Verify hop-by-hop headers were filtered out
	if recorder.Header().Get("Connection") != "" {
		t.Error("Connection header should be filtered out")
	}
	if recorder.Header().Get("Keep-Alive") != "" {
		t.Error("Keep-Alive header should be filtered out")
	}
	if recorder.Header().Get("Proxy-Authenticate") != "" {
		t.Error("Proxy-Authenticate header should be filtered out")
	}
	if recorder.Header().Get("Proxy-Authorization") != "" {
		t.Error("Proxy-Authorization header should be filtered out")
	}
	if recorder.Header().Get("TE") != "" {
		t.Error("TE header should be filtered out")
	}
	if recorder.Header().Get("Trailer") != "" {
		t.Error("Trailer header should be filtered out")
	}
	if recorder.Header().Get("Transfer-Encoding") != "" {
		t.Error("Transfer-Encoding header should be filtered out")
	}
	if recorder.Header().Get("Upgrade") != "" {
		t.Error("Upgrade header should be filtered out")
	}

	// Verify custom header was preserved
	if customHeader := recorder.Header().Get("X-Custom-Header"); customHeader != "should-be-present" {
		t.Errorf("X-Custom-Header = %q, want should-be-present", customHeader)
	}

	// Verify Content-Type was preserved
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", contentType)
	}
}

func TestStreamResponseNilResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	err := StreamResponse(recorder, nil)
	if err == nil {
		t.Fatal("StreamResponse() error = nil, want error for nil response")
	}
	if !strings.Contains(err.Error(), "response is nil") {
		t.Errorf("error = %v, want response is nil error", err)
	}
}
