package tailscale

import "fmt"

// Error types
var (
	ErrNoAPIKey        = fmt.Errorf("tailscale API key is required")
	ErrNoTailnet       = fmt.Errorf("tailnet name is required")
	ErrInvalidResponse = fmt.Errorf("invalid API response")
	ErrRateLimited     = fmt.Errorf("rate limited by Tailscale API")
	ErrAuthFailed      = fmt.Errorf("authentication failed")
	ErrKeyCreation     = fmt.Errorf("failed to create ephemeral key")
	ErrCacheHoldDown   = fmt.Errorf("cache in hold-down period")
	ErrInvalidExpiry   = fmt.Errorf("invalid expiry duration")
)

// APIError represents a Tailscale API error
type APIError struct {
	StatusCode int
	Message    string
	Err        error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Tailscale API error (status %d): %s", e.StatusCode, e.Message)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// NewAPIError creates a new API error
func NewAPIError(statusCode int, message string, err error) *APIError {
	return &APIError{
		StatusCode: statusCode,
		Message:    message,
		Err:        err,
	}
}
