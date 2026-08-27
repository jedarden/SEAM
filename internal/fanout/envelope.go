package fanout

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// InstanceStatus represents the status of a single instance in a fan-out request.
type InstanceStatus string

const (
	// InstanceStatusOK indicates the instance returned a successful 2xx response.
	InstanceStatusOK InstanceStatus = "ok"

	// InstanceStatusError indicates the instance returned a non-2xx response.
	InstanceStatusError InstanceStatus = "error"

	// InstanceStatusTimeout indicates the instance request timed out.
	InstanceStatusTimeout InstanceStatus = "timeout"

	// InstanceStatusBreakerRefused indicates the circuit breaker refused the request.
	InstanceStatusBreakerRefused InstanceStatus = "breaker-refused"

	// InstanceStatusTruncated indicates the response was truncated due to envelope size limits.
	InstanceStatusTruncated InstanceStatus = "truncated"

	// InstanceStatusScopeWithheld indicates the instance was withheld due to scope constraints.
	InstanceStatusScopeWithheld InstanceStatus = "scope-withheld"
)

// IsValidInstanceStatus checks if a status string is a valid InstanceStatus.
func IsValidInstanceStatus(status string) bool {
	switch InstanceStatus(status) {
	case InstanceStatusOK, InstanceStatusError, InstanceStatusTimeout,
		InstanceStatusBreakerRefused, InstanceStatusTruncated, InstanceStatusScopeWithheld:
		return true
	default:
		return false
	}
}

// InstanceResult represents the result of a single instance request in a fan-out.
type InstanceResult struct {
	// Status is the outcome status of this instance request.
	Status InstanceStatus `json:"status"`

	// Instance is the identifier for this instance (the key from the upstream map).
	Instance string `json:"instance"`

	// StatusCode is the HTTP status code returned by the upstream.
	// Only present for non-scope-withheld statuses.
	StatusCode int `json:"statusCode,omitempty"`

	// Body contains the response body for successful requests.
	// Only present for status=ok and may be truncated if oversized.
	Body []byte `json:"body,omitempty"`

	// BodyBytes is the original body size before truncation.
	// Present when status=truncated to show what was omitted.
	BodyBytes int `json:"bodyBytes,omitempty"`

	// Error contains a human-readable error message for failed requests.
	// Present for status=error, timeout, breaker-refused.
	Error string `json:"error,omitempty"`

	// Truncated is true when the response body was truncated due to envelope size limits.
	Truncated bool `json:"truncated,omitempty"`
}

// Summary contains aggregate statistics about the fan-out request.
type Summary struct {
	// Total is the total number of instances in the upstream map.
	Total int `json:"total"`

	// Dispatched is the number of instances that were actually dispatched.
	// Instances with open breakers or scope constraints are not dispatched.
	Dispatched int `json:"dispatched"`

	// OK is the count of instances that returned successful 2xx responses.
	OK int `json:"ok"`

	// Error is the count of instances that returned non-2xx responses.
	Error int `json:"error"`

	// Timeout is the count of instances that timed out.
	Timeout int `json:"timeout"`

	// BreakerRefused is the count of instances refused by the circuit breaker.
	BreakerRefused int `json:"breaker-refused"`

	// Truncated is the count of instances whose responses were truncated.
	Truncated int `json:"truncated"`

	// ScopeWithheld is the count of instances withheld due to scope constraints.
	ScopeWithheld int `json:"scope-withheld"`
}

// Envelope represents the multi-status response for a fan-out request.
// This envelope is returned with HTTP 207 Multi-Status when the fan-out
// produces mixed results or when any instance fails/refuses/withholds.
type Envelope struct {
	// Instances contains per-instance results keyed by instance identifier.
	// The order of results in this map is not guaranteed to match dispatch order.
	Instances map[string]*InstanceResult `json:"instances"`

	// Summary contains aggregate statistics across all instances.
	Summary *Summary `json:"summary"`
}

// NewEnvelope creates an empty envelope with the given total instance count.
func NewEnvelope(totalInstances int) *Envelope {
	return &Envelope{
		Instances: make(map[string]*InstanceResult),
		Summary: &Summary{
			Total: totalInstances,
		},
	}
}

// AddResult adds a single instance result to the envelope and updates the summary.
func (e *Envelope) AddResult(instanceID string, result *InstanceResult) {
	e.Instances[instanceID] = result
	e.Summary.Dispatched++

	switch result.Status {
	case InstanceStatusOK:
		e.Summary.OK++
	case InstanceStatusError:
		e.Summary.Error++
	case InstanceStatusTimeout:
		e.Summary.Timeout++
	case InstanceStatusBreakerRefused:
		e.Summary.BreakerRefused++
	case InstanceStatusTruncated:
		e.Summary.Truncated++
	case InstanceStatusScopeWithheld:
		e.Summary.ScopeWithheld++
	}
}

// AddScopeWithheld adds a scope-withheld entry for an instance that was not dispatched.
func (e *Envelope) AddScopeWithheld(instanceID string) {
	e.Instances[instanceID] = &InstanceResult{
		Status:   InstanceStatusScopeWithheld,
		Instance: instanceID,
	}
	e.Summary.ScopeWithheld++
}

// AddBreakerRefused adds a breaker-refused entry for an instance that was not dispatched.
func (e *Envelope) AddBreakerRefused(instanceID string) {
	e.Instances[instanceID] = BreakerRefusedResult(instanceID)
	e.Summary.BreakerRefused++
}

// DeriveStatus computes the appropriate HTTP status code and fan-out header
// based on the envelope contents.
//
// Returns 200 OK only if:
// - Every dispatched instance returned a 2xx response
// - No instances were refused, truncated, or withheld
//
// Returns 207 Multi-Status with X-SEAM-Fanout-Partial: 1 if:
// - Any dispatched instance returned a non-2xx response
// - Any instance was refused, truncated, or withheld
//
// Returns 503 Service Unavailable if:
// - All dispatched instances were refused by the circuit breaker
//
// SEAM's own refusals (400/403/404/429/402) never appear as 207 - they
// are returned directly without constructing an envelope.
func (e *Envelope) DeriveStatus() (statusCode int, partialHeader string) {
	// All scope-withheld (none dispatched) = 207 with partial
	if e.Summary.Dispatched == 0 && e.Summary.ScopeWithheld > 0 {
		return http.StatusMultiStatus, "1"
	}

	// All breaker-refused (dispatched but none succeeded) = 503
	if e.Summary.Dispatched > 0 && e.Summary.BreakerRefused == e.Summary.Dispatched {
		return http.StatusServiceUnavailable, ""
	}

	// Any non-OK outcome = 207 with partial
	if e.Summary.Error > 0 || e.Summary.Timeout > 0 ||
		e.Summary.BreakerRefused > 0 || e.Summary.Truncated > 0 ||
		e.Summary.ScopeWithheld > 0 {
		return http.StatusMultiStatus, "1"
	}

	// Perfect success = 200
	return http.StatusOK, ""
}

// MaxFanoutEnvelopeBytes computes the maximum envelope size in bytes.
// This is min(maxBufferedResponseBytes × total_instances, 64 MiB).
func MaxFanoutEnvelopeBytes(maxBufferedResponseBytes int64, totalInstances int) int64 {
	maxPerResponse := maxBufferedResponseBytes * int64(totalInstances)
	hardCap := int64(64 * 1024 * 1024) // 64 MiB

	if maxPerResponse < hardCap {
		return maxPerResponse
	}
	return hardCap
}

// EstimateSize estimates the size of this envelope in bytes when JSON-encoded.
// This is an approximation used for admission control before encoding.
func (e *Envelope) EstimateSize() int64 {
	// Base structure overhead
	size := int64(20) // {"instances":{},"summary":{}}

	// Summary overhead (approximate JSON string length)
	size += int64(80) // "total":0,"dispatched":0,"ok":0,"error":0,"timeout":0,"breaker-refused":0,"truncated":0,"scope-withheld":0

	// Per-instance overhead
	for instanceID, result := range e.Instances {
		// Instance ID as JSON key with quotes and colon
		size += int64(len(instanceID)) + 4 // `"instance":`

		// InstanceResult fields
		size += int64(len(`"status":"")`)) + int64(len(result.Status))
		size += int64(len(`,"instance":"`)) + int64(len(result.Instance))

		if result.StatusCode > 0 {
			size += int64(len(`,"statusCode":`)) + 4 // integer
		}
		if len(result.Body) > 0 {
			size += int64(len(`,"body":"`)) + int64(len(result.Body))
			if result.Truncated {
				size += int64(len(`,"truncated":true`))
			}
			size += int64(len(`,"bodyBytes":`)) + 6 // integer
		}
		if result.Error != "" {
			size += int64(len(`,"error":"`)) + int64(len(result.Error))
		}
	}

	return size
}

// ShouldTruncate reports whether adding a response body of the given size
// would exceed the maximum envelope size limit.
func (e *Envelope) ShouldTruncate(bodySize int64, maxEnvelopeBytes int64) bool {
	estimatedSize := e.EstimateSize() + bodySize
	return estimatedSize > maxEnvelopeBytes
}

// JSONBytes returns the JSON-encoded envelope.
func (e *Envelope) JSONBytes() ([]byte, error) {
	return json.Marshal(e)
}

// TruncateResponse creates a truncated instance result when the full response
// would exceed envelope size limits. Returns only the status and size info, not the body.
func TruncateResponse(instanceID string, statusCode int, originalBodySize int, truncationReason string) *InstanceResult {
	return &InstanceResult{
		Status:     InstanceStatusTruncated,
		Instance:   instanceID,
		StatusCode: statusCode,
		BodyBytes:  originalBodySize,
		Error:      truncationReason,
		Truncated:  true,
	}
}

// BreakerRefusedResult creates a result for an instance refused by the circuit breaker.
func BreakerRefusedResult(instanceID string) *InstanceResult {
	return &InstanceResult{
		Status:   InstanceStatusBreakerRefused,
		Instance: instanceID,
		Error:    "Circuit breaker is open for this upstream",
	}
}

// TimeoutResult creates a result for a timed-out instance request.
func TimeoutResult(instanceID string, timeoutMs int) *InstanceResult {
	return &InstanceResult{
		Status:   InstanceStatusTimeout,
		Instance: instanceID,
		Error:    fmt.Sprintf("Request timed out after %dms", timeoutMs),
	}
}

// ErrorResult creates a result for an instance that returned an error HTTP status.
func ErrorResult(instanceID string, statusCode int, body []byte) *InstanceResult {
	return &InstanceResult{
		Status:     InstanceStatusError,
		Instance:   instanceID,
		StatusCode: statusCode,
		Body:       body,
	}
}

// OKResult creates a result for a successful instance response.
func OKResult(instanceID string, statusCode int, body []byte) *InstanceResult {
	return &InstanceResult{
		Status:     InstanceStatusOK,
		Instance:   instanceID,
		StatusCode: statusCode,
		Body:       body,
	}
}
