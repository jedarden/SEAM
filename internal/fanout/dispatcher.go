package fanout

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DispatchConfig holds configuration for a fan-out dispatch operation.
type DispatchConfig struct {
	// MaxEnvelopeBytes is the maximum size of the response envelope.
	// Responses that would exceed this limit are truncated.
	MaxEnvelopeBytes int64

	// Timeout is the per-instance request timeout.
	Timeout time.Duration

	// ConcurrentLimit is the maximum number of concurrent instance requests.
	// Zero means no limit (dispatch all instances concurrently).
	ConcurrentLimit int
}

// DefaultDispatchConfig returns a dispatch configuration with sensible defaults.
func DefaultDispatchConfig() *DispatchConfig {
	return &DispatchConfig{
		MaxEnvelopeBytes: 64 * 1024 * 1024, // 64 MiB hard cap
		Timeout:          30 * time.Second,
		ConcurrentLimit:  10, // Limit concurrent upstream requests
	}
}

// InstanceDispatch represents a single instance to be dispatched.
type InstanceDispatch struct {
	// InstanceID is the identifier for this instance (from upstream map key).
	InstanceID string

	// Target is the upstream target for this instance.
	Target string

	// VaultPath is the credential path for this instance.
	VaultPath string

	// InjectAs specifies how to inject the credential.
	InjectAs *InjectAsConfig

	// TLSConfig holds TLS configuration for this instance.
	TLSConfig *TLSConfig
}

// InjectAsConfig describes how to inject a credential into an upstream request.
type InjectAsConfig struct {
	Kind string // "header", "query", "bearer"
	Name string // Header/query parameter name
}

// TLSConfig represents TLS configuration for an upstream connection.
type TLSConfig struct {
	CaBundle           string
	ServerName         string
	InsecureSkipVerify bool
	PlaintextAck       bool
}

// Executor is the interface for executing a single instance request.
// Implementations handle stages 10-11 of the SEAM pipeline per instance.
type Executor interface {
	// ExecuteRequest performs a single upstream request for one instance.
	// Returns the HTTP status code, response body, and any error.
	ExecuteRequest(ctx context.Context, dispatch *InstanceDispatch) (statusCode int, body []byte, err error)
}

// CircuitBreakerCheck checks if a circuit breaker is open for an upstream.
type CircuitBreakerCheck func(instanceID string, targetURL string) bool

// Dispatcher coordinates fan-out requests across multiple upstream instances.
type Dispatcher struct {
	config            *DispatchConfig
	executor          Executor
	breakerCheck      CircuitBreakerCheck
	scopeCheck        ScopeCheck
	completionOrderCh chan *InstanceCompletion
	mu                sync.Mutex
}

// InstanceCompletion represents the completion of a single instance request.
type InstanceCompletion struct {
	InstanceID  string
	StatusCode  int
	Body        []byte
	Error       error
	CompletedAt time.Time
}

// NewDispatcher creates a new fan-out dispatcher.
func NewDispatcher(config *DispatchConfig, executor Executor, breakerCheck CircuitBreakerCheck, scopeCheck ScopeCheck) *Dispatcher {
	if config == nil {
		config = DefaultDispatchConfig()
	}
	return &Dispatcher{
		config:            config,
		executor:          executor,
		breakerCheck:      breakerCheck,
		scopeCheck:        scopeCheck,
		completionOrderCh: make(chan *InstanceCompletion, 100),
	}
}

// Dispatch executes the fan-out request across all instances.
//
// The dispatch process:
// 1. Filter instances with open breakers or scope constraints (BEFORE stage 10)
// 2. Execute stages 10-11 concurrently for surviving instances
// 3. Admit results in completion order (first-completed-first-served)
// 4. Truncate responses that would exceed envelope size limits
//
// Returns an envelope with per-instance results and derived status.
func (d *Dispatcher) Dispatch(ctx context.Context, instances []InstanceDispatch) *Envelope {
	if len(instances) == 0 {
		return &Envelope{
			Instances: map[string]*InstanceResult{},
			Summary:   &Summary{Total: 0},
		}
	}

	envelope := NewEnvelope(len(instances))

	// Stage 1: Filter instances that should NOT be dispatched
	dispatchable := d.filterDispatchableInstances(instances, envelope)

	// Stage 2: Execute concurrent requests with completion-order admission
	d.dispatchConcurrently(ctx, dispatchable, envelope)

	return envelope
}

// filterDispatchableInstances filters out instances that should not be dispatched.
// Open breakers and scope-withheld instances are added to the envelope as non-dispatched.
func (d *Dispatcher) filterDispatchableInstances(instances []InstanceDispatch, envelope *Envelope) []InstanceDispatch {
	dispatchable := make([]InstanceDispatch, 0, len(instances))

	for _, instance := range instances {
		// Check circuit breaker - skip if open
		if d.breakerCheck != nil && d.breakerCheck(instance.InstanceID, instance.Target) {
			envelope.AddBreakerRefused(instance.InstanceID)
			continue
		}

		// Check scope constraints - skip if withheld
		if d.scopeCheck != nil && !d.scopeCheck(instance.InstanceID) {
			envelope.AddScopeWithheld(instance.InstanceID)
			continue
		}

		dispatchable = append(dispatchable, instance)
	}

	return dispatchable
}

// dispatchConcurrently executes requests for dispatchable instances with completion-order admission.
func (d *Dispatcher) dispatchConcurrently(ctx context.Context, instances []InstanceDispatch, envelope *Envelope) {
	if len(instances) == 0 {
		return
	}

	// Create a context with timeout for all requests
	timeoutCtx, cancel := context.WithTimeout(ctx, d.config.Timeout)
	defer cancel()

	// Semaphore for concurrent limit
	sem := make(chan struct{}, d.config.ConcurrentLimit)
	if d.config.ConcurrentLimit <= 0 {
		sem = make(chan struct{}, len(instances)) // No limit
	}

	var wg sync.WaitGroup
	completions := make(chan *InstanceCompletion, len(instances))

	// Launch goroutines for each instance
	for _, instance := range instances {
		wg.Add(1)
		go func(inst InstanceDispatch) {
			defer wg.Done()

			// Acquire semaphore slot
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-timeoutCtx.Done():
				completions <- &InstanceCompletion{
					InstanceID: inst.InstanceID,
					Error:      fmt.Errorf("dispatch canceled before start"),
				}
				return
			}

			// Execute the request
			statusCode, body, err := d.executor.ExecuteRequest(timeoutCtx, &inst)

			// Report completion
			completions <- &InstanceCompletion{
				InstanceID:  inst.InstanceID,
				StatusCode:  statusCode,
				Body:        body,
				Error:       err,
				CompletedAt: time.Now(),
			}
		}(instance)
	}

	// Wait for all goroutines to start
	go func() {
		wg.Wait()
		close(completions)
	}()

	// Admit completions as they arrive (completion-order admission)
	for completion := range completions {
		d.admitCompletion(completion, envelope)
	}
}

// admitCompletion admits a single instance completion into the envelope
// with truncation checking for oversized responses.
func (d *Dispatcher) admitCompletion(completion *InstanceCompletion, envelope *Envelope) {
	instanceID := completion.InstanceID

	if completion.Error != nil {
		// Check for cancellation first (parent context cancelled)
		if completion.Error == context.Canceled {
			envelope.AddResult(instanceID, &InstanceResult{
				Status:   InstanceStatusError,
				Instance: instanceID,
				Error:    "Request canceled by client",
			})
			return
		}

		// Check for timeout
		if completion.Error == context.DeadlineExceeded {
			envelope.AddResult(instanceID, TimeoutResult(instanceID, int(d.config.Timeout.Milliseconds())))
			return
		}

		// Check for dispatch canceled before start (also treated as cancellation)
		if completion.Error.Error() == "dispatch canceled before start" {
			envelope.AddResult(instanceID, &InstanceResult{
				Status:   InstanceStatusError,
				Instance: instanceID,
				Error:    "Dispatch canceled before request started",
			})
			return
		}

		// Generic error
		envelope.AddResult(instanceID, &InstanceResult{
			Status:   InstanceStatusError,
			Instance: instanceID,
			Error:    completion.Error.Error(),
		})
		return
	}

	// Successful completion - check if body fits in envelope
	bodySize := int64(len(completion.Body))
	if envelope.ShouldTruncate(bodySize, d.config.MaxEnvelopeBytes) {
		// Truncate the response
		envelope.AddResult(instanceID, TruncateResponse(
			instanceID,
			completion.StatusCode,
			int(bodySize),
			fmt.Sprintf("Response body truncated to stay within %d byte envelope limit", d.config.MaxEnvelopeBytes),
		))
		return
	}

	// Admit the full response
	if isSuccessfulStatus(completion.StatusCode) {
		envelope.AddResult(instanceID, OKResult(instanceID, completion.StatusCode, completion.Body))
	} else {
		envelope.AddResult(instanceID, ErrorResult(instanceID, completion.StatusCode, completion.Body))
	}
}

// isSuccessfulStatus reports whether the HTTP status code is a 2xx success.
func isSuccessfulStatus(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}

// IsAllParameter checks if the given instance parameter value is the special "_all" value.
func IsAllParameter(instanceParam string, value string) bool {
	return instanceParam != "" && value == "_all"
}

// ExtractInstanceParam extracts the instance parameter value from path parameters.
// Returns empty string if the parameter is not set.
func ExtractInstanceParam(instanceParam string, pathParams map[string]string) string {
	if instanceParam == "" {
		return ""
	}
	return pathParams[instanceParam]
}
