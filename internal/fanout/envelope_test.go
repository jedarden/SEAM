package fanout

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestNewEnvelope(t *testing.T) {
	total := 5
	envelope := NewEnvelope(total)

	if envelope.Summary.Total != total {
		t.Errorf("Expected Total=%d, got %d", total, envelope.Summary.Total)
	}
	if envelope.Instances == nil {
		t.Error("Instances map should be initialized")
	}
	if len(envelope.Instances) != 0 {
		t.Error("Instances map should be empty initially")
	}
}

func TestAddResult(t *testing.T) {
	envelope := NewEnvelope(3)

	// Test adding OK result
	okResult := OKResult("instance-1", 200, []byte(`{"success":true}`))
	envelope.AddResult("instance-1", okResult)

	if envelope.Summary.Dispatched != 1 {
		t.Errorf("Expected Dispatched=1, got %d", envelope.Summary.Dispatched)
	}
	if envelope.Summary.OK != 1 {
		t.Errorf("Expected OK=1, got %d", envelope.Summary.OK)
	}

	// Test adding error result
	errorResult := ErrorResult("instance-2", 500, []byte(`{"error":"internal"}`))
	envelope.AddResult("instance-2", errorResult)

	if envelope.Summary.Dispatched != 2 {
		t.Errorf("Expected Dispatched=2, got %d", envelope.Summary.Dispatched)
	}
	if envelope.Summary.Error != 1 {
		t.Errorf("Expected Error=1, got %d", envelope.Summary.Error)
	}

	// Test adding timeout result
	timeoutResult := TimeoutResult("instance-3", 30000)
	envelope.AddResult("instance-3", timeoutResult)

	if envelope.Summary.Timeout != 1 {
		t.Errorf("Expected Timeout=1, got %d", envelope.Summary.Timeout)
	}
}

func TestAddScopeWithheld(t *testing.T) {
	envelope := NewEnvelope(2)
	envelope.AddScopeWithheld("instance-1")

	if envelope.Summary.ScopeWithheld != 1 {
		t.Errorf("Expected ScopeWithheld=1, got %d", envelope.Summary.ScopeWithheld)
	}

	result := envelope.Instances["instance-1"]
	if result == nil {
		t.Fatal("Instance result should exist")
	}
	if result.Status != InstanceStatusScopeWithheld {
		t.Errorf("Expected status=%s, got %s", InstanceStatusScopeWithheld, result.Status)
	}
}

func TestDeriveStatus_PerfectSuccess(t *testing.T) {
	envelope := NewEnvelope(3)
	envelope.AddResult("instance-1", OKResult("instance-1", 200, []byte(`{}`)))
	envelope.AddResult("instance-2", OKResult("instance-2", 200, []byte(`{}`)))
	envelope.AddResult("instance-3", OKResult("instance-3", 200, []byte(`{}`)))

	statusCode, partialHeader := envelope.DeriveStatus()

	if statusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", statusCode)
	}
	if partialHeader != "" {
		t.Errorf("Expected empty partial header, got %q", partialHeader)
	}
}

func TestDeriveStatus_PartialFailure(t *testing.T) {
	envelope := NewEnvelope(3)
	envelope.AddResult("instance-1", OKResult("instance-1", 200, []byte(`{}`)))
	envelope.AddResult("instance-2", ErrorResult("instance-2", 500, []byte(`{}`)))
	envelope.AddResult("instance-3", OKResult("instance-3", 200, []byte(`{}`)))

	statusCode, partialHeader := envelope.DeriveStatus()

	if statusCode != http.StatusMultiStatus {
		t.Errorf("Expected status 207, got %d", statusCode)
	}
	if partialHeader != "1" {
		t.Errorf("Expected partial header '1', got %q", partialHeader)
	}
}

func TestDeriveStatus_TimeoutFailure(t *testing.T) {
	envelope := NewEnvelope(2)
	envelope.AddResult("instance-1", OKResult("instance-1", 200, []byte(`{}`)))
	envelope.AddResult("instance-2", TimeoutResult("instance-2", 30000))

	statusCode, partialHeader := envelope.DeriveStatus()

	if statusCode != http.StatusMultiStatus {
		t.Errorf("Expected status 207, got %d", statusCode)
	}
	if partialHeader != "1" {
		t.Errorf("Expected partial header '1', got %q", partialHeader)
	}
}

func TestDeriveStatus_BreakerRefused(t *testing.T) {
	envelope := NewEnvelope(2)
	envelope.AddResult("instance-1", BreakerRefusedResult("instance-1"))
	envelope.AddResult("instance-2", OKResult("instance-2", 200, []byte(`{}`)))

	statusCode, partialHeader := envelope.DeriveStatus()

	if statusCode != http.StatusMultiStatus {
		t.Errorf("Expected status 207, got %d", statusCode)
	}
	if partialHeader != "1" {
		t.Errorf("Expected partial header '1', got %q", partialHeader)
	}
}

func TestDeriveStatus_AllBreakerRefused(t *testing.T) {
	envelope := NewEnvelope(3)
	envelope.AddResult("instance-1", BreakerRefusedResult("instance-1"))
	envelope.AddResult("instance-2", BreakerRefusedResult("instance-2"))
	envelope.AddResult("instance-3", BreakerRefusedResult("instance-3"))

	statusCode, partialHeader := envelope.DeriveStatus()

	if statusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", statusCode)
	}
	if partialHeader != "" {
		t.Errorf("Expected empty partial header for 503, got %q", partialHeader)
	}
}

func TestDeriveStatus_Truncated(t *testing.T) {
	envelope := NewEnvelope(2)
	envelope.AddResult("instance-1", OKResult("instance-1", 200, []byte(`{}`)))
	envelope.AddResult("instance-2", TruncateResponse("instance-2", 200, 1000000, "envelope limit"))

	statusCode, partialHeader := envelope.DeriveStatus()

	if statusCode != http.StatusMultiStatus {
		t.Errorf("Expected status 207, got %d", statusCode)
	}
	if partialHeader != "1" {
		t.Errorf("Expected partial header '1', got %q", partialHeader)
	}
}

func TestDeriveStatus_ScopeWithheld(t *testing.T) {
	envelope := NewEnvelope(2)
	envelope.AddScopeWithheld("instance-1")
	envelope.AddResult("instance-2", OKResult("instance-2", 200, []byte(`{}`)))

	statusCode, partialHeader := envelope.DeriveStatus()

	if statusCode != http.StatusMultiStatus {
		t.Errorf("Expected status 207, got %d", statusCode)
	}
	if partialHeader != "1" {
		t.Errorf("Expected partial header '1', got %q", partialHeader)
	}
}

func TestDeriveStatus_AllScopeWithheld(t *testing.T) {
	envelope := NewEnvelope(2)
	envelope.AddScopeWithheld("instance-1")
	envelope.AddScopeWithheld("instance-2")

	statusCode, partialHeader := envelope.DeriveStatus()

	if statusCode != http.StatusMultiStatus {
		t.Errorf("Expected status 207, got %d", statusCode)
	}
	if partialHeader != "1" {
		t.Errorf("Expected partial header '1', got %q", partialHeader)
	}
}

func TestMaxFanoutEnvelopeBytes(t *testing.T) {
	tests := []struct {
		name                string
		maxBufferedResponse int64
		totalInstances      int
		expectedMax         int64
	}{
		{
			name:                "small fanout under hard cap",
			maxBufferedResponse: 1 * 1024 * 1024, // 1 MiB
			totalInstances:      5,
			expectedMax:         5 * 1024 * 1024, // 5 MiB
		},
		{
			name:                "large fanout hits hard cap",
			maxBufferedResponse: 1 * 1024 * 1024, // 1 MiB
			totalInstances:      100,
			expectedMax:         64 * 1024 * 1024, // 64 MiB hard cap
		},
		{
			name:                "very large buffered response",
			maxBufferedResponse: 10 * 1024 * 1024, // 10 MiB
			totalInstances:      10,
			expectedMax:         64 * 1024 * 1024, // 64 MiB hard cap
		},
		{
			name:                "single instance",
			maxBufferedResponse: 1 * 1024 * 1024, // 1 MiB
			totalInstances:      1,
			expectedMax:         1 * 1024 * 1024, // 1 MiB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaxFanoutEnvelopeBytes(tt.maxBufferedResponse, tt.totalInstances)
			if result != tt.expectedMax {
				t.Errorf("MaxFanoutEnvelopeBytes(%d, %d) = %d, want %d",
					tt.maxBufferedResponse, tt.totalInstances, result, tt.expectedMax)
			}
		})
	}
}

func TestEstimateSize(t *testing.T) {
	envelope := NewEnvelope(3)

	// Add results and check size estimation
	envelope.AddResult("instance-1", OKResult("instance-1", 200, []byte(`{"data":"test"}`)))
	envelope.AddResult("instance-2", ErrorResult("instance-2", 500, []byte(`{"error":"bad"}`)))
	envelope.AddResult("instance-3", TimeoutResult("instance-3", 30000))

	size := envelope.EstimateSize()
	if size <= 0 {
		t.Errorf("Estimated size should be positive, got %d", size)
	}

	// Size should be reasonable (not in gigabytes for small envelopes)
	maxReasonableSize := int64(10 * 1024 * 1024) // 10 MiB
	if size > maxReasonableSize {
		t.Errorf("Estimated size %d seems unreasonably large for small envelope", size)
	}
}

func TestShouldTruncate(t *testing.T) {
	envelope := NewEnvelope(1)
	maxEnvelopeBytes := int64(1024) // 1 KiB limit

	// Small body should not truncate
	smallBody := int64(100)
	if envelope.ShouldTruncate(smallBody, maxEnvelopeBytes) {
		t.Error("Small body should not trigger truncation")
	}

	// Large body should truncate
	largeBody := int64(2000)
	if !envelope.ShouldTruncate(largeBody, maxEnvelopeBytes) {
		t.Error("Large body should trigger truncation")
	}
}

func TestJSONBytes(t *testing.T) {
	envelope := NewEnvelope(2)
	envelope.AddResult("instance-1", OKResult("instance-1", 200, []byte(`{"success":true}`)))
	envelope.AddResult("instance-2", ErrorResult("instance-2", 500, []byte(`{"error":"failed"}`)))

	bytes, err := envelope.JSONBytes()
	if err != nil {
		t.Fatalf("JSONBytes() returned error: %v", err)
	}

	if len(bytes) == 0 {
		t.Error("JSONBytes() should return non-empty bytes")
	}

	// Verify it's valid JSON
	var decoded Envelope
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		t.Errorf("Result is not valid JSON: %v", err)
	}

	// Verify structure
	if decoded.Summary.Total != 2 {
		t.Errorf("Expected Total=2, got %d", decoded.Summary.Total)
	}
	if len(decoded.Instances) != 2 {
		t.Errorf("Expected 2 instances, got %d", len(decoded.Instances))
	}
}

func TestIsValidInstanceStatus(t *testing.T) {
	validStatuses := []string{
		"ok", "error", "timeout", "breaker-refused", "truncated", "scope-withheld",
	}

	for _, status := range validStatuses {
		if !IsValidInstanceStatus(status) {
			t.Errorf("Status %q should be valid", status)
		}
	}

	invalidStatuses := []string{
		"invalid", "pending", "running", "", "OK", "Error",
	}

	for _, status := range invalidStatuses {
		if IsValidInstanceStatus(status) {
			t.Errorf("Status %q should be invalid", status)
		}
	}
}

func TestTruncateResponse(t *testing.T) {
	result := TruncateResponse("instance-1", 200, 50000, "envelope size limit")

	if result.Status != InstanceStatusTruncated {
		t.Errorf("Expected status %s, got %s", InstanceStatusTruncated, result.Status)
	}
	if result.StatusCode != 200 {
		t.Errorf("Expected StatusCode 200, got %d", result.StatusCode)
	}
	if result.BodyBytes != 50000 {
		t.Errorf("Expected BodyBytes 50000, got %d", result.BodyBytes)
	}
	if result.Error != "envelope size limit" {
		t.Errorf("Expected error message 'envelope size limit', got %q", result.Error)
	}
	if !result.Truncated {
		t.Error("Expected Truncated=true")
	}
	if len(result.Body) != 0 {
		t.Error("Truncated response should have no body")
	}
}

func TestBreakerRefusedResult(t *testing.T) {
	result := BreakerRefusedResult("instance-1")

	if result.Status != InstanceStatusBreakerRefused {
		t.Errorf("Expected status %s, got %s", InstanceStatusBreakerRefused, result.Status)
	}
	if result.Instance != "instance-1" {
		t.Errorf("Expected instance 'instance-1', got %q", result.Instance)
	}
	if result.Error == "" {
		t.Error("Breaker refused result should have error message")
	}
}

func TestTimeoutResult(t *testing.T) {
	result := TimeoutResult("instance-1", 30000)

	if result.Status != InstanceStatusTimeout {
		t.Errorf("Expected status %s, got %s", InstanceStatusTimeout, result.Status)
	}
	if result.Instance != "instance-1" {
		t.Errorf("Expected instance 'instance-1', got %q", result.Instance)
	}
	if result.Error == "" {
		t.Error("Timeout result should have error message")
	}
}

func TestErrorResult(t *testing.T) {
	body := []byte(`{"error":"internal server error"}`)
	result := ErrorResult("instance-1", 500, body)

	if result.Status != InstanceStatusError {
		t.Errorf("Expected status %s, got %s", InstanceStatusError, result.Status)
	}
	if result.StatusCode != 500 {
		t.Errorf("Expected StatusCode 500, got %d", result.StatusCode)
	}
	if len(result.Body) != len(body) {
		t.Errorf("Expected body length %d, got %d", len(body), len(result.Body))
	}
}

func TestOKResult(t *testing.T) {
	body := []byte(`{"success":true}`)
	result := OKResult("instance-1", 200, body)

	if result.Status != InstanceStatusOK {
		t.Errorf("Expected status %s, got %s", InstanceStatusOK, result.Status)
	}
	if result.StatusCode != 200 {
		t.Errorf("Expected StatusCode 200, got %d", result.StatusCode)
	}
	if len(result.Body) != len(body) {
		t.Errorf("Expected body length %d, got %d", len(body), len(result.Body))
	}
}

func TestEnvelope_SummaryAccuracy(t *testing.T) {
	envelope := NewEnvelope(10)

	// Add various results
	envelope.AddResult("ok-1", OKResult("ok-1", 200, []byte(`{}`)))
	envelope.AddResult("ok-2", OKResult("ok-2", 201, []byte(`{}`)))
	envelope.AddResult("error-1", ErrorResult("error-1", 500, []byte(`{}`)))
	envelope.AddResult("error-2", ErrorResult("error-2", 404, []byte(`{}`)))
	envelope.AddResult("error-3", ErrorResult("error-3", 403, []byte(`{}`)))
	envelope.AddResult("timeout-1", TimeoutResult("timeout-1", 30000))
	envelope.AddResult("breaker-1", BreakerRefusedResult("breaker-1"))
	envelope.AddResult("truncated-1", TruncateResponse("truncated-1", 200, 1000000, "limit"))
	envelope.AddScopeWithheld("scope-1")

	// Verify exact counts
	if envelope.Summary.Total != 10 {
		t.Errorf("Expected Total=10, got %d", envelope.Summary.Total)
	}
	if envelope.Summary.Dispatched != 8 { // All except scope-withheld
		t.Errorf("Expected Dispatched=8, got %d", envelope.Summary.Dispatched)
	}
	if envelope.Summary.OK != 2 {
		t.Errorf("Expected OK=2, got %d", envelope.Summary.OK)
	}
	if envelope.Summary.Error != 3 {
		t.Errorf("Expected Error=3, got %d", envelope.Summary.Error)
	}
	if envelope.Summary.Timeout != 1 {
		t.Errorf("Expected Timeout=1, got %d", envelope.Summary.Timeout)
	}
	if envelope.Summary.BreakerRefused != 1 {
		t.Errorf("Expected BreakerRefused=1, got %d", envelope.Summary.BreakerRefused)
	}
	if envelope.Summary.Truncated != 1 {
		t.Errorf("Expected Truncated=1, got %d", envelope.Summary.Truncated)
	}
	if envelope.Summary.ScopeWithheld != 1 {
		t.Errorf("Expected ScopeWithheld=1, got %d", envelope.Summary.ScopeWithheld)
	}
}
