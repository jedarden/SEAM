package stubupstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStubUpstream_EchoBehavior(t *testing.T) {
	stub := New(Config{
		Addr:     "localhost:15810",
		Behavior: BehaviorEcho,
	})
	if err := stub.Start(); err != nil {
		t.Fatalf("failed to start stub: %v", err)
	}
	defer func() { _ = stub.Stop(context.Background()) }()

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	// Make a request with Authorization header
	req, _ := http.NewRequest(http.MethodGet, stub.URL()+"/", nil)
	req.Header.Set("Authorization", "Bearer test-token-12345")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "test-token-12345") {
		t.Error("response body does not contain echoed credential")
	}

	// Verify the call was logged
	calls := stub.GetCallLog()
	if len(calls) != 1 {
		t.Errorf("expected 1 call, got %d", len(calls))
	}

	if calls[0].AuthHeader != "Bearer test-token-12345" {
		t.Errorf("logged auth header was not empty")
	}
}

func TestStubUpstream_BehaviorChange(t *testing.T) {
	stub := New(Config{
		Addr:     "localhost:15811",
		Behavior: BehaviorNormal,
	})
	if err := stub.Start(); err != nil {
		t.Fatalf("failed to start stub: %v", err)
	}
	defer func() { _ = stub.Stop(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// First request should get 200
	req, _ := http.NewRequest(http.MethodGet, stub.URL()+"/", nil)
	client := &http.Client{}
	resp, _ := client.Do(req)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("initial request: expected 200, got %d", resp.StatusCode)
	}

	// Change behavior to 401
	stub.SetBehavior(Behavior401)

	// Second request should get 401
	req2, _ := http.NewRequest(http.MethodGet, stub.URL()+"/", nil)
	resp2, _ := client.Do(req2)
	_ = resp2.Body.Close()

	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("after behavior change: expected 401, got %d", resp2.StatusCode)
	}
}

func TestStubUpstream_FiveHundred(t *testing.T) {
	stub := New(Config{
		Addr:     "localhost:15812",
		Behavior: Behavior5xx,
	})
	if err := stub.Start(); err != nil {
		t.Fatalf("failed to start stub: %v", err)
	}
	defer func() { _ = stub.Stop(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, stub.URL()+"/", nil)
	client := &http.Client{}
	resp, _ := client.Do(req)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

func TestStubUpstream_OversizedResponse(t *testing.T) {
	stub := New(Config{
		Addr:          "localhost:15813",
		Behavior:      BehaviorOversized,
		OversizedSize: 1024 * 1024, // 1 MiB
	})
	if err := stub.Start(); err != nil {
		t.Fatalf("failed to start stub: %v", err)
	}
	defer func() { _ = stub.Stop(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, stub.URL()+"/", nil)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	if len(body) != 1024*1024 {
		t.Errorf("expected %d bytes, got %d", 1024*1024, len(body))
	}
}

func TestStubUpstream_CallLog(t *testing.T) {
	stub := New(Config{
		Addr:     "localhost:15814",
		Behavior: BehaviorNormal,
	})
	if err := stub.Start(); err != nil {
		t.Fatalf("failed to start stub: %v", err)
	}
	defer func() { _ = stub.Stop(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Make some requests
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, stub.URL()+"/", nil)
		client := &http.Client{}
		resp, _ := client.Do(req)
		_ = resp.Body.Close()
	}

	calls := stub.GetCallLog()
	if len(calls) != 3 {
		t.Errorf("expected 3 calls, got %d", len(calls))
	}

	// Clear the log
	stub.ClearCallLog()

	calls = stub.GetCallLog()
	if len(calls) != 0 {
		t.Errorf("expected 0 calls after clear, got %d", len(calls))
	}
}

func TestStubUpstream_ControlEndpoint(t *testing.T) {
	stub := New(Config{
		Addr:     "localhost:15815",
		Behavior: BehaviorNormal,
	})
	if err := stub.Start(); err != nil {
		t.Fatalf("failed to start stub: %v", err)
	}
	defer func() { _ = stub.Stop(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// GET status
	req, _ := http.NewRequest(http.MethodGet, stub.URL()+"/_control", nil)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("control GET failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("control GET: expected 200, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// POST to change behavior
	// (This is tested indirectly via SetBehavior)
}

func TestStubUpstream_MultipleServers(t *testing.T) {
	// Start multiple stub servers concurrently
	stub1 := New(Config{Addr: "localhost:15816", Behavior: BehaviorNormal})
	stub2 := New(Config{Addr: "localhost:15817", Behavior: Behavior401})
	stub3 := New(Config{Addr: "localhost:15818", Behavior: Behavior5xx})

	for _, stub := range []*Server{stub1, stub2, stub3} {
		if err := stub.Start(); err != nil {
			t.Fatalf("failed to start stub: %v", err)
		}
		defer func() { _ = stub.Stop(context.Background()) }()
	}

	time.Sleep(100 * time.Millisecond)

	// Make requests to each
	client := &http.Client{}

	resp1, _ := client.Get(stub1.URL() + "/")
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("stub1: expected 200, got %d", resp1.StatusCode)
	}
	_ = resp1.Body.Close()

	resp2, _ := client.Get(stub2.URL() + "/")
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("stub2: expected 401, got %d", resp2.StatusCode)
	}
	_ = resp2.Body.Close()

	resp3, _ := client.Get(stub3.URL() + "/")
	if resp3.StatusCode != http.StatusInternalServerError {
		t.Errorf("stub3: expected 500, got %d", resp3.StatusCode)
	}
	_ = resp3.Body.Close()
}
