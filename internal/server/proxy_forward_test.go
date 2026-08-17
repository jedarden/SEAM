package server

import (
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
