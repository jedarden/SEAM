package fanout

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// mockExecutor implements the Executor interface for testing.
type mockExecutor struct {
	// callCount tracks how many times ExecuteRequest was called
	callCount int64
	// responses maps instance IDs to mock responses
	responses map[string]mockResponse
	// delay is an optional delay before responding (for testing concurrency)
	delay time.Duration
}

type mockResponse struct {
	statusCode int
	body       []byte
	err        error
}

func (m *mockExecutor) ExecuteRequest(ctx context.Context, dispatch *InstanceDispatch) (int, []byte, error) {
	atomic.AddInt64(&m.callCount, 1)

	// Simulate delay if configured
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		}
	}

	if dispatch == nil {
		return 0, nil, errors.New("dispatch is nil")
	}

	resp, ok := m.responses[dispatch.InstanceID]
	if !ok {
		return 0, nil, errors.New("unknown instance")
	}

	return resp.statusCode, resp.body, resp.err
}

func TestNewDispatcher(t *testing.T) {
	config := &DispatchConfig{
		MaxEnvelopeBytes: 64 * 1024 * 1024,
		Timeout:          30 * time.Second,
		ConcurrentLimit:  10,
	}

	executor := &mockExecutor{}
	breakerCheck := func(instanceID string, targetURL string) bool { return false }
	scopeCheck := func(instanceID string) bool { return true }

	dispatcher := NewDispatcher(config, executor, breakerCheck, scopeCheck)

	if dispatcher == nil {
		t.Fatal("NewDispatcher should return non-nil dispatcher")
	}
	if dispatcher.config != config {
		t.Error("Dispatcher should use provided config")
	}
	if dispatcher.executor != executor {
		t.Error("Dispatcher should use provided executor")
	}
}

func TestNewDispatcher_DefaultConfig(t *testing.T) {
	executor := &mockExecutor{}
	dispatcher := NewDispatcher(nil, executor, nil, nil)

	if dispatcher == nil {
		t.Fatal("NewDispatcher with nil config should return non-nil dispatcher")
	}
	if dispatcher.config == nil {
		t.Error("Dispatcher should have default config")
	}
	if dispatcher.config.MaxEnvelopeBytes != 64*1024*1024 {
		t.Errorf("Default MaxEnvelopeBytes should be 64 MiB, got %d", dispatcher.config.MaxEnvelopeBytes)
	}
	if dispatcher.config.Timeout != 30*time.Second {
		t.Errorf("Default Timeout should be 30s, got %v", dispatcher.config.Timeout)
	}
	if dispatcher.config.ConcurrentLimit != 10 {
		t.Errorf("Default ConcurrentLimit should be 10, got %d", dispatcher.config.ConcurrentLimit)
	}
}

func TestDefaultDispatchConfig(t *testing.T) {
	config := DefaultDispatchConfig()

	if config.MaxEnvelopeBytes != 64*1024*1024 {
		t.Errorf("Default MaxEnvelopeBytes should be 64 MiB, got %d", config.MaxEnvelopeBytes)
	}
	if config.Timeout != 30*time.Second {
		t.Errorf("Default Timeout should be 30s, got %v", config.Timeout)
	}
	if config.ConcurrentLimit != 10 {
		t.Errorf("Default ConcurrentLimit should be 10, got %d", config.ConcurrentLimit)
	}
}

func TestDispatch_EmptyInstances(t *testing.T) {
	dispatcher := NewDispatcher(nil, &mockExecutor{}, nil, nil)

	envelope := dispatcher.Dispatch(context.Background(), []InstanceDispatch{})

	if envelope == nil {
		t.Fatal("Dispatch should return non-nil envelope")
	}
	if envelope.Summary.Total != 0 {
		t.Errorf("Expected Total=0, got %d", envelope.Summary.Total)
	}
	if len(envelope.Instances) != 0 {
		t.Errorf("Expected no instances, got %d", len(envelope.Instances))
	}
}

func TestDispatch_BreakerFiltering(t *testing.T) {
	breakerCalled := make(map[string]bool)
	breakerCheck := func(instanceID string, targetURL string) bool {
		breakerCalled[instanceID] = true
		// Instance-2 has open breaker
		return instanceID == "instance-2"
	}

	executor := &mockExecutor{
		responses: map[string]mockResponse{
			"instance-1": {statusCode: 200, body: []byte(`{}`)},
			"instance-2": {statusCode: 200, body: []byte(`{}`)}, // Won't be called due to breaker
			"instance-3": {statusCode: 200, body: []byte(`{}`)},
		},
	}

	dispatcher := NewDispatcher(nil, executor, breakerCheck, nil)

	instances := []InstanceDispatch{
		{InstanceID: "instance-1", Target: "http://example.com"},
		{InstanceID: "instance-2", Target: "http://example.com"},
		{InstanceID: "instance-3", Target: "http://example.com"},
	}

	envelope := dispatcher.Dispatch(context.Background(), instances)

	// Verify breaker was checked for all instances
	if !breakerCalled["instance-1"] || !breakerCalled["instance-2"] || !breakerCalled["instance-3"] {
		t.Error("Breaker check should be called for all instances")
	}

	// Verify results
	if envelope.Summary.Total != 3 {
		t.Errorf("Expected Total=3, got %d", envelope.Summary.Total)
	}
	if envelope.Summary.Dispatched != 2 {
		t.Errorf("Expected Dispatched=2 (one refused), got %d", envelope.Summary.Dispatched)
	}
	if envelope.Summary.BreakerRefused != 1 {
		t.Errorf("Expected BreakerRefused=1, got %d", envelope.Summary.BreakerRefused)
	}

	// Verify instance-2 was refused
	refusedResult := envelope.Instances["instance-2"]
	if refusedResult == nil {
		t.Fatal("instance-2 should have a result")
	}
	if refusedResult.Status != InstanceStatusBreakerRefused {
		t.Errorf("Expected status breaker-refused, got %s", refusedResult.Status)
	}

	// Verify instance-1 and instance-3 were dispatched
	ok1 := envelope.Instances["instance-1"]
	if ok1 == nil || ok1.Status != InstanceStatusOK {
		t.Error("instance-1 should be OK")
	}
	ok3 := envelope.Instances["instance-3"]
	if ok3 == nil || ok3.Status != InstanceStatusOK {
		t.Error("instance-3 should be OK")
	}
}

func TestDispatch_ScopeFiltering(t *testing.T) {
	scopeCheck := func(instanceID string) bool {
		// Only instance-1 and instance-3 are in scope
		return instanceID == "instance-1" || instanceID == "instance-3"
	}

	executor := &mockExecutor{
		responses: map[string]mockResponse{
			"instance-1": {statusCode: 200, body: []byte(`{}`)},
			"instance-3": {statusCode: 200, body: []byte(`{}`)},
		},
	}

	dispatcher := NewDispatcher(nil, executor, nil, scopeCheck)

	instances := []InstanceDispatch{
		{InstanceID: "instance-1", Target: "http://example.com"},
		{InstanceID: "instance-2", Target: "http://example.com"},
		{InstanceID: "instance-3", Target: "http://example.com"},
	}

	envelope := dispatcher.Dispatch(context.Background(), instances)

	// Verify results
	if envelope.Summary.Total != 3 {
		t.Errorf("Expected Total=3, got %d", envelope.Summary.Total)
	}
	if envelope.Summary.Dispatched != 2 {
		t.Errorf("Expected Dispatched=2 (one withheld), got %d", envelope.Summary.Dispatched)
	}
	if envelope.Summary.ScopeWithheld != 1 {
		t.Errorf("Expected ScopeWithheld=1, got %d", envelope.Summary.ScopeWithheld)
	}

	// Verify instance-2 was scope-withheld
	withheldResult := envelope.Instances["instance-2"]
	if withheldResult == nil {
		t.Fatal("instance-2 should have a result")
	}
	if withheldResult.Status != InstanceStatusScopeWithheld {
		t.Errorf("Expected status scope-withheld, got %s", withheldResult.Status)
	}
}

func TestDispatch_ConcurrentExecution(t *testing.T) {
	executor := &mockExecutor{
		responses: map[string]mockResponse{
			"instance-1": {statusCode: 200, body: []byte(`{"result":1}`)},
			"instance-2": {statusCode: 200, body: []byte(`{"result":2}`)},
			"instance-3": {statusCode: 200, body: []byte(`{"result":3}`)},
		},
		delay: 50 * time.Millisecond,
	}

	dispatcher := NewDispatcher(nil, executor, nil, nil)

	instances := []InstanceDispatch{
		{InstanceID: "instance-1", Target: "http://example.com"},
		{InstanceID: "instance-2", Target: "http://example.com"},
		{InstanceID: "instance-3", Target: "http://example.com"},
	}

	start := time.Now()
	envelope := dispatcher.Dispatch(context.Background(), instances)
	elapsed := time.Since(start)

	// All three should execute concurrently, so elapsed time should be close to the delay
	// not 3x the delay (which would indicate serial execution)
	if elapsed > 200*time.Millisecond {
		t.Errorf("Concurrent execution should take ~50ms, took %v", elapsed)
	}

	// Verify all were dispatched successfully
	if envelope.Summary.Dispatched != 3 {
		t.Errorf("Expected Dispatched=3, got %d", envelope.Summary.Dispatched)
	}
	if envelope.Summary.OK != 3 {
		t.Errorf("Expected OK=3, got %d", envelope.Summary.OK)
	}

	// Verify executor was called exactly 3 times
	if executor.callCount != 3 {
		t.Errorf("Expected executor called 3 times, got %d", executor.callCount)
	}
}

func TestDispatch_Timeout(t *testing.T) {
	executor := &mockExecutor{
		responses: map[string]mockResponse{
			"instance-1": {statusCode: 200, body: []byte(`{}`)},
			"instance-2": {statusCode: 200, body: []byte(`{}`)},
		},
		delay: 200 * time.Millisecond, // Will timeout
	}

	config := &DispatchConfig{
		Timeout:         50 * time.Millisecond,
		ConcurrentLimit: 10,
	}

	dispatcher := NewDispatcher(config, executor, nil, nil)

	instances := []InstanceDispatch{
		{InstanceID: "instance-1", Target: "http://example.com"},
		{InstanceID: "instance-2", Target: "http://example.com"},
	}

	envelope := dispatcher.Dispatch(context.Background(), instances)

	// Both should timeout due to slow executor
	if envelope.Summary.Dispatched != 2 {
		t.Errorf("Expected Dispatched=2, got %d", envelope.Summary.Dispatched)
	}
	if envelope.Summary.Timeout != 2 {
		t.Errorf("Expected Timeout=2, got %d", envelope.Summary.Timeout)
	}

	// Verify both instances have timeout status
	for instanceID, result := range envelope.Instances {
		if result.Status != InstanceStatusTimeout {
			t.Errorf("Instance %s: expected status timeout, got %s", instanceID, result.Status)
		}
	}
}

func TestDispatch_Truncation(t *testing.T) {
	smallBody := make([]byte, 10*1024)  // 10 KiB body
	largeBody := make([]byte, 100*1024) // 100 KiB body

	executor := &mockExecutor{
		responses: map[string]mockResponse{
			"instance-1": {statusCode: 200, body: smallBody},
			"instance-2": {statusCode: 200, body: largeBody}, // Will be truncated
		},
	}

	config := &DispatchConfig{
		MaxEnvelopeBytes: 50 * 1024, // 50 KiB limit (small enough to trigger truncation)
		ConcurrentLimit:  10,
		Timeout:          10 * time.Second, // Sufficient timeout
	}

	dispatcher := NewDispatcher(config, executor, nil, nil)

	instances := []InstanceDispatch{
		{InstanceID: "instance-1", Target: "http://example.com"},
		{InstanceID: "instance-2", Target: "http://example.com"},
	}

	envelope := dispatcher.Dispatch(context.Background(), instances)

	// Verify results
	if envelope.Summary.OK != 1 {
		t.Errorf("Expected OK=1, got %d", envelope.Summary.OK)
	}

	// Verify truncation occurred
	if envelope.Summary.Truncated != 1 {
		t.Errorf("Expected Truncated=1, got %d", envelope.Summary.Truncated)
	}

	truncatedResult := envelope.Instances["instance-2"]
	if truncatedResult == nil {
		t.Fatal("instance-2 should have a result")
	}
	if truncatedResult.Status != InstanceStatusTruncated {
		t.Errorf("Expected status truncated, got %s", truncatedResult.Status)
	}
	if !truncatedResult.Truncated {
		t.Error("Truncated flag should be true")
	}
	if truncatedResult.BodyBytes != len(largeBody) {
		t.Errorf("Expected BodyBytes=%d, got %d", len(largeBody), truncatedResult.BodyBytes)
	}
	if len(truncatedResult.Body) != 0 {
		t.Error("Truncated response should have no body")
	}
}

func TestDispatch_ErrorHandling(t *testing.T) {
	executor := &mockExecutor{
		responses: map[string]mockResponse{
			"instance-1": {statusCode: 200, body: []byte(`{}`)},
			"instance-2": {statusCode: 500, body: []byte(`{"error":"internal"}`)},
			"instance-3": {statusCode: 404, body: []byte(`{"error":"not found"}`)},
		},
	}

	dispatcher := NewDispatcher(nil, executor, nil, nil)

	instances := []InstanceDispatch{
		{InstanceID: "instance-1", Target: "http://example.com"},
		{InstanceID: "instance-2", Target: "http://example.com"},
		{InstanceID: "instance-3", Target: "http://example.com"},
	}

	envelope := dispatcher.Dispatch(context.Background(), instances)

	// Verify error counts
	if envelope.Summary.OK != 1 {
		t.Errorf("Expected OK=1, got %d", envelope.Summary.OK)
	}
	if envelope.Summary.Error != 2 {
		t.Errorf("Expected Error=2, got %d", envelope.Summary.Error)
	}

	// Verify error results have correct status codes
	errorResult := envelope.Instances["instance-2"]
	if errorResult.StatusCode != 500 {
		t.Errorf("Expected StatusCode 500, got %d", errorResult.StatusCode)
	}

	notFoundResult := envelope.Instances["instance-3"]
	if notFoundResult.StatusCode != 404 {
		t.Errorf("Expected StatusCode 404, got %d", notFoundResult.StatusCode)
	}
}

func TestDispatch_ContextCancellation(t *testing.T) {
	executor := &mockExecutor{
		delay: 100 * time.Millisecond,
		responses: map[string]mockResponse{
			"instance-1": {statusCode: 200, body: []byte(`{}`)},
		},
	}

	dispatcher := NewDispatcher(nil, executor, nil, nil)

	instances := []InstanceDispatch{
		{InstanceID: "instance-1", Target: "http://example.com"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	envelope := dispatcher.Dispatch(ctx, instances)

	// Should have error result (canceled contexts return context.Canceled error)
	if envelope.Summary.Dispatched != 1 {
		t.Errorf("Expected Dispatched=1, got %d", envelope.Summary.Dispatched)
	}

	result := envelope.Instances["instance-1"]
	// Context cancellation is treated as an error, not timeout
	if result.Status != InstanceStatusError {
		t.Errorf("Expected status error for cancelled context, got %s", result.Status)
	}
	if result.Error == "" {
		t.Error("Error result should have error message")
	}
}

func TestDispatch_ConcurrentLimit(t *testing.T) {
	executor := &mockExecutor{
		delay: 20 * time.Millisecond, // Shorter delay for faster test
		responses: map[string]mockResponse{
			"instance-1": {statusCode: 200, body: []byte(`{}`)},
			"instance-2": {statusCode: 200, body: []byte(`{}`)},
			"instance-3": {statusCode: 200, body: []byte(`{}`)},
			"instance-4": {statusCode: 200, body: []byte(`{}`)},
			"instance-5": {statusCode: 200, body: []byte(`{}`)},
		},
	}

	config := &DispatchConfig{
		ConcurrentLimit:  2,                // Only 2 concurrent requests
		Timeout:          10 * time.Second, // Sufficient timeout
		MaxEnvelopeBytes: 10 * 1024 * 1024, // Large envelope to prevent truncation
	}

	dispatcher := NewDispatcher(config, executor, nil, nil)

	instances := []InstanceDispatch{
		{InstanceID: "instance-1", Target: "http://example.com"},
		{InstanceID: "instance-2", Target: "http://example.com"},
		{InstanceID: "instance-3", Target: "http://example.com"},
		{InstanceID: "instance-4", Target: "http://example.com"},
		{InstanceID: "instance-5", Target: "http://example.com"},
	}

	start := time.Now()
	envelope := dispatcher.Dispatch(context.Background(), instances)
	elapsed := time.Since(start)

	// With limit of 2 and 5 instances taking 20ms each, should take roughly 3 cycles
	// (2+2+1) * 20ms ≈ 60ms
	if elapsed < 50*time.Millisecond {
		t.Errorf("With ConcurrentLimit=2, execution should take ~60ms, took %v", elapsed)
	}

	// All should complete successfully
	if envelope.Summary.OK != 5 {
		t.Errorf("Expected OK=5, got %d", envelope.Summary.OK)
	}

	// Verify executor was called 5 times
	if executor.callCount != 5 {
		t.Errorf("Expected executor called 5 times, got %d", executor.callCount)
	}
}

func TestIsAllParameter(t *testing.T) {
	tests := []struct {
		name          string
		instanceParam string
		value         string
		expected      bool
	}{
		{"_all value", "cluster", "_all", true},
		{"not _all", "cluster", "ardenone-cluster", false},
		{"empty param", "", "_all", false},
		{"empty value", "cluster", "", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsAllParameter(tt.instanceParam, tt.value)
			if result != tt.expected {
				t.Errorf("IsAllParameter(%q, %q) = %v, want %v",
					tt.instanceParam, tt.value, result, tt.expected)
			}
		})
	}
}

func TestExtractInstanceParam(t *testing.T) {
	tests := []struct {
		name          string
		instanceParam string
		pathParams    map[string]string
		expected      string
	}{
		{
			name:          "param exists",
			instanceParam: "cluster",
			pathParams:    map[string]string{"cluster": "ardenone-cluster"},
			expected:      "ardenone-cluster",
		},
		{
			name:          "param missing",
			instanceParam: "cluster",
			pathParams:    map[string]string{"region": "us-east"},
			expected:      "",
		},
		{
			name:          "empty instance param",
			instanceParam: "",
			pathParams:    map[string]string{"cluster": "ardenone-cluster"},
			expected:      "",
		},
		{
			name:          "nil path params",
			instanceParam: "cluster",
			pathParams:    nil,
			expected:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractInstanceParam(tt.instanceParam, tt.pathParams)
			if result != tt.expected {
				t.Errorf("ExtractInstanceParam(%q, %v) = %q, want %q",
					tt.instanceParam, tt.pathParams, result, tt.expected)
			}
		})
	}
}
